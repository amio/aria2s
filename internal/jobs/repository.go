// Package jobs owns durable managed-download control facts. Manifests are the
// authority for stable user identity, payload location, current native
// execution binding, activity intent, and the current actionable issue. Live
// aria2 progress never becomes repository state.
package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/amio/aria2s/internal/atomicfile"
	"golang.org/x/sys/unix"
)

const (
	CurrentManifestVersion = 2
	CurrentStorageVersion  = 1
)

const (
	legacyPhasePending    = "pending"
	legacyPhaseStaged     = "staged"
	legacyPhasePublishing = "publishing"
	legacyPhasePublished  = "published"
	legacyPhaseRemoved    = "removed"
)

type ActivityIntent string

const (
	ActivityRunning ActivityIntent = "running"
	ActivityStopped ActivityIntent = "stopped"
)

type PayloadLocation string

const (
	PayloadStaging   PayloadLocation = "staging"
	PayloadPublished PayloadLocation = "published"
)

type ObjectIdentity struct {
	MountID              uint64 `json:"mountId"`
	ObjectID             uint64 `json:"objectId"`
	ReliableAcrossRename bool   `json:"reliableAcrossRename"`
}

type StorageScope struct {
	Version       int            `json:"version"`
	ID            string         `json:"id"`
	MountPoint    string         `json:"mountPoint"`
	StagingAnchor string         `json:"stagingAnchor"`
	Marker        ObjectIdentity `json:"marker"`
}

type PayloadState struct {
	Location  PayloadLocation `json:"location"`
	Root      string          `json:"root,omitempty"`
	FinalRoot string          `json:"finalRoot,omitempty"`
	Identity  ObjectIdentity  `json:"identity,omitempty"`
	Length    *int64          `json:"length,omitempty"`
}

type ExecutionBinding struct {
	GID string `json:"gid"`
}

type JobIssue struct {
	Code string `json:"code"`
}

type Job struct {
	Version        int               `json:"version"`
	ID             string            `json:"id"`
	Source         string            `json:"source"`
	TargetDir      string            `json:"targetDir"`
	TargetIdentity ObjectIdentity    `json:"targetIdentity"`
	StorageID      string            `json:"storageId"`
	ActivityIntent ActivityIntent    `json:"activityIntent"`
	Removed        bool              `json:"removed,omitempty"`
	Payload        PayloadState      `json:"payload"`
	Execution      *ExecutionBinding `json:"execution,omitempty"`
	Issue          *JobIssue         `json:"issue,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
	legacyPending  bool
}

type Token [sha256.Size]byte

type ScannedJob struct {
	Job   Job
	Token Token
	Err   error
	ID    string
}

type Repository struct {
	root        string
	jobsDir     string
	storagesDir string
	locksDir    string
}

func New(root string) *Repository {
	return &Repository{root: root, jobsDir: filepath.Join(root, "jobs"), storagesDir: filepath.Join(root, "storages"), locksDir: filepath.Join(root, "job-locks")}
}

func (repository *Repository) Root() string { return repository.root }

func (repository *Repository) Create(job Job) (Token, error) {
	if err := validateJob(job); err != nil {
		return Token{}, err
	}
	directory := repository.jobDir(job.ID)
	if err := os.MkdirAll(repository.jobsDir, 0o700); err != nil {
		return Token{}, err
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return Token{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(directory)
		}
	}()
	job.Version = CurrentManifestVersion
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	data, token, err := encodeJob(job)
	if err != nil {
		return Token{}, err
	}
	if err := atomicfile.Write(repository.manifestPath(job.ID), data, 0o600); err != nil {
		return Token{}, err
	}
	if err := atomicfile.SyncDirectory(repository.jobsDir); err != nil {
		return Token{}, err
	}
	committed = true
	return token, nil
}

func (repository *Repository) Load(id string) (Job, Token, error) {
	if !ValidID(id) {
		return Job{}, Token{}, fmt.Errorf("invalid job id %q", id)
	}
	path := repository.manifestPath(id)
	info, err := os.Lstat(path)
	if err != nil {
		return Job{}, Token{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Job{}, Token{}, errors.New("job manifest is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Job{}, Token{}, err
	}
	job, err := decodeJob(id, data)
	if err != nil {
		return Job{}, Token{}, err
	}
	return job, sha256.Sum256(data), nil
}

func (repository *Repository) SaveCAS(job Job, expected Token) (Token, error) {
	current, token, err := repository.Load(job.ID)
	if err != nil {
		return Token{}, err
	}
	if token != expected {
		return Token{}, errors.New("job manifest changed concurrently")
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = current.CreatedAt
	}
	job.Version = CurrentManifestVersion
	job.UpdatedAt = time.Now().UTC()
	if err := validateJob(job); err != nil {
		return Token{}, err
	}
	data, next, err := encodeJob(job)
	if err != nil {
		return Token{}, err
	}
	if err := atomicfile.Write(repository.manifestPath(job.ID), data, 0o600); err != nil {
		return Token{}, err
	}
	return next, nil
}

func (repository *Repository) DeleteCAS(id string, expected Token) error {
	_, token, err := repository.Load(id)
	if err != nil {
		return err
	}
	if token != expected {
		return errors.New("job manifest changed concurrently")
	}
	jobDir := repository.jobDir(id)
	tombstone := filepath.Join(repository.jobsDir, fmt.Sprintf(".%s.deleting-%x", id, expected[:4]))
	if err := os.Rename(jobDir, tombstone); err != nil {
		return err
	}
	if err := atomicfile.SyncDirectory(repository.jobsDir); err != nil {
		return err
	}
	if err := os.RemoveAll(tombstone); err != nil {
		return err
	}
	return atomicfile.SyncDirectory(repository.jobsDir)
}

func (repository *Repository) Exists(id string) bool {
	if !ValidID(id) {
		return false
	}
	info, err := os.Lstat(repository.jobDir(id))
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func (repository *Repository) Scan() ([]ScannedJob, error) {
	entries, err := os.ReadDir(repository.jobsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]ScannedJob, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if !ValidID(id) {
			continue
		}
		job, token, loadErr := repository.Load(id)
		result = append(result, ScannedJob{ID: id, Job: job, Token: token, Err: loadErr})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (repository *Repository) SaveStorage(scope StorageScope) error {
	if scope.Version == 0 {
		scope.Version = CurrentStorageVersion
	}
	if scope.Version != CurrentStorageVersion || !ValidID(scope.ID) || scope.MountPoint == "" || scope.StagingAnchor == "" {
		return errors.New("invalid storage scope")
	}
	data, err := json.MarshalIndent(scope, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(repository.storagesDir, scope.ID+".json"), data, 0o600)
}

func (repository *Repository) LoadStorage(id string) (StorageScope, error) {
	if !ValidID(id) {
		return StorageScope{}, errors.New("invalid storage id")
	}
	data, err := os.ReadFile(filepath.Join(repository.storagesDir, id+".json"))
	if err != nil {
		return StorageScope{}, err
	}
	var scope StorageScope
	if err := json.Unmarshal(data, &scope); err != nil {
		return StorageScope{}, err
	}
	if scope.Version != CurrentStorageVersion || scope.ID != id {
		return StorageScope{}, errors.New("unsupported or mismatched storage scope")
	}
	return scope, nil
}

func (repository *Repository) ScanStorages() ([]StorageScope, error) {
	entries, err := os.ReadDir(repository.storagesDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var scopes []StorageScope
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		scope, loadErr := repository.LoadStorage(id)
		if loadErr != nil {
			return nil, loadErr
		}
		scopes = append(scopes, scope)
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].ID < scopes[j].ID })
	return scopes, nil
}

func (repository *Repository) WriteMetainfo(id string, data []byte) error {
	if !ValidID(id) || len(data) == 0 {
		return errors.New("invalid metainfo write")
	}
	return atomicfile.Write(repository.MetainfoPath(id), data, 0o600)
}

func (repository *Repository) ReadMetainfo(id string) ([]byte, error) {
	path := repository.MetainfoPath(id)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("retained metainfo is not a regular file")
	}
	return os.ReadFile(path)
}

func (repository *Repository) MetainfoPath(id string) string {
	return filepath.Join(repository.jobDir(id), "metainfo.torrent")
}

func WorkDir(scope StorageScope, jobID string) string {
	return filepath.Join(scope.StagingAnchor, ".aria2s_staging", scope.ID, jobID)
}

// FinalRoot returns the prepared or committed publication destination.
func (job Job) FinalRoot() string {
	if job.Payload.FinalRoot != "" {
		return job.Payload.FinalRoot
	}
	return job.Payload.Root
}

func (job Job) PublicationRenamed() bool {
	return job.Payload.Root != "" && job.FinalRoot() != job.Payload.Root
}

// LegacyPending reports the one migration-only distinction needed during the
// first startup reconciliation. It is intentionally not persisted in v2.
func (job Job) LegacyPending() bool { return job.legacyPending }

func (repository *Repository) Lock(ctx context.Context, id string) (func() error, error) {
	if !ValidID(id) {
		return nil, errors.New("invalid job id")
	}
	return repository.lockFile(ctx, id+".lock")
}

// LockPublication serializes the short allocation-and-rename transaction
// across hook processes. Publication is an atomic same-filesystem rename, so a
// single lock keeps the no-overwrite invariant without durable reservations.
func (repository *Repository) LockPublication(ctx context.Context) (func() error, error) {
	return repository.lockFile(ctx, "publication.lock")
}

func (repository *Repository) lockFile(ctx context.Context, name string) (func() error, error) {
	if err := os.MkdirAll(repository.locksDir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(repository.locksDir, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() error {
				unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
				closeErr := file.Close()
				if unlockErr != nil {
					return unlockErr
				}
				return closeErr
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func ValidID(id string) bool {
	if len(id) != 16 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

func (repository *Repository) jobDir(id string) string { return filepath.Join(repository.jobsDir, id) }
func (repository *Repository) manifestPath(id string) string {
	return filepath.Join(repository.jobDir(id), "manifest.json")
}

func encodeJob(job Job) ([]byte, Token, error) {
	job.Version = CurrentManifestVersion
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return nil, Token{}, err
	}
	data = append(data, '\n')
	return data, sha256.Sum256(data), nil
}

func decodeJob(id string, data []byte) (Job, error) {
	var envelope struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Job{}, err
	}
	var job Job
	switch envelope.Version {
	case 1:
		var legacy legacyJob
		if err := json.Unmarshal(data, &legacy); err != nil {
			return Job{}, err
		}
		if !validLegacyPhase(legacy.Phase) {
			return Job{}, errors.New("invalid legacy job phase")
		}
		job = migrateV1(legacy)
	case CurrentManifestVersion:
		if err := json.Unmarshal(data, &job); err != nil {
			return Job{}, err
		}
	default:
		return Job{}, errors.New("unsupported job manifest version")
	}
	if job.ID != id {
		return Job{}, errors.New("manifest id does not match directory")
	}
	if err := validateJob(job); err != nil {
		return Job{}, err
	}
	return job, nil
}

func validateJob(job Job) error {
	if job.Version != 0 && job.Version != CurrentManifestVersion {
		return errors.New("unsupported job manifest version")
	}
	if !ValidID(job.ID) || job.Source == "" || !filepath.IsAbs(job.TargetDir) || !ValidID(job.StorageID) || job.TargetIdentity.MountID == 0 || job.TargetIdentity.ObjectID == 0 {
		return errors.New("invalid job manifest")
	}
	switch job.Payload.Location {
	case PayloadStaging, PayloadPublished:
	default:
		return errors.New("invalid payload location")
	}
	if job.ActivityIntent != ActivityRunning && job.ActivityIntent != ActivityStopped {
		return errors.New("invalid activity intent")
	}
	if job.Removed && job.ActivityIntent != ActivityStopped {
		return errors.New("removed job must be stopped")
	}
	if job.Payload.Root != "" && !validPayloadRoot(job.Payload.Root) {
		return errors.New("invalid payload root")
	}
	if job.Payload.FinalRoot != "" {
		clean := filepath.Clean(job.Payload.FinalRoot)
		if filepath.IsAbs(job.Payload.FinalRoot) || clean == "." || clean == ".." ||
			strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.Base(clean) != clean {
			return errors.New("invalid destination root")
		}
	}
	if job.Payload.Location == PayloadPublished && job.PublicationRenamed() && job.ActivityIntent != ActivityStopped {
		return errors.New("renamed published task must be stopped")
	}
	if job.Payload.Location == PayloadPublished && job.PublicationRenamed() && job.Execution != nil {
		return errors.New("renamed published task cannot bind a seed execution")
	}
	if job.Payload.FinalRoot != "" || job.Payload.Location == PayloadPublished {
		if job.Payload.Root == "" || job.Payload.Identity.MountID == 0 || job.Payload.Identity.ObjectID == 0 {
			return errors.New("prepared or published payload requires identity")
		}
	}
	if job.Payload.Location == PayloadPublished && job.Payload.FinalRoot == "" {
		return errors.New("published payload requires final root")
	}
	if job.Payload.Length != nil && *job.Payload.Length < 0 {
		return errors.New("invalid payload length")
	}
	if job.Execution != nil && !ValidID(job.Execution.GID) {
		return errors.New("invalid execution binding")
	}
	if job.Issue != nil {
		if strings.TrimSpace(job.Issue.Code) == "" {
			return errors.New("invalid job issue")
		}
		if _, ok := LookupIssue(job.Issue.Code); !ok {
			return errors.New("unknown job issue code")
		}
	}
	return nil
}

func validPayloadRoot(root string) bool {
	clean := filepath.Clean(root)
	return !filepath.IsAbs(root) && clean != "." && clean != ".." &&
		!strings.HasPrefix(clean, ".."+string(filepath.Separator)) && filepath.Base(clean) == clean
}

type legacyJob struct {
	Version         int            `json:"version"`
	ID              string         `json:"id"`
	Source          string         `json:"source"`
	TargetDir       string         `json:"targetDir"`
	TargetIdentity  ObjectIdentity `json:"targetIdentity"`
	StorageID       string         `json:"storageId"`
	Phase           string         `json:"phase"`
	ActivityIntent  ActivityIntent `json:"activityIntent"`
	PayloadRoot     string         `json:"payloadRoot,omitempty"`
	DestinationRoot string         `json:"destinationRoot,omitempty"`
	PayloadIdentity ObjectIdentity `json:"payloadIdentity,omitempty"`
	PayloadLength   *int64         `json:"payloadLength,omitempty"`
	ProblemCode     string         `json:"problemCode,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

func migrateV1(old legacyJob) Job {
	location := PayloadStaging
	removed := old.Phase == legacyPhaseRemoved
	finalRoot := ""
	if old.Phase == legacyPhasePublishing {
		finalRoot = old.DestinationRoot
		if finalRoot == "" {
			finalRoot = old.PayloadRoot
		}
	}
	if old.Phase == legacyPhasePublished || (removed && old.PayloadRoot != "") {
		location = PayloadPublished
		finalRoot = old.DestinationRoot
		if finalRoot == "" {
			finalRoot = old.PayloadRoot
		}
	}
	job := Job{
		Version: CurrentManifestVersion, ID: old.ID, Source: old.Source,
		TargetDir: old.TargetDir, TargetIdentity: old.TargetIdentity,
		StorageID: old.StorageID, ActivityIntent: old.ActivityIntent,
		Removed: removed,
		Payload: PayloadState{Location: location, Root: old.PayloadRoot,
			FinalRoot: finalRoot, Identity: old.PayloadIdentity, Length: old.PayloadLength},
		CreatedAt: old.CreatedAt, UpdatedAt: old.UpdatedAt,
		legacyPending: old.Phase == legacyPhasePending,
	}
	// A v1 GID was the JobID. Keep it only as a candidate binding until the
	// reconciler proves authoritative absence.
	if old.Phase != legacyPhasePublished || (old.ActivityIntent == ActivityRunning && finalRoot == old.PayloadRoot) {
		job.Execution = &ExecutionBinding{GID: old.ID}
	}
	if old.ProblemCode != "" {
		job.Issue = &JobIssue{Code: old.ProblemCode}
	}
	return job
}

func validLegacyPhase(phase string) bool {
	switch phase {
	case legacyPhasePending, legacyPhaseStaged, legacyPhasePublishing, legacyPhasePublished, legacyPhaseRemoved:
		return true
	default:
		return false
	}
}
