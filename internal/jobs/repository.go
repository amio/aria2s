// Package jobs owns durable managed-download control facts. Manifests are the
// authority for ownership, activity intent, publication phase, staging/final
// payload roots, and final logical payload length; live aria2 status and
// transport progress never become repository state.
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

const CurrentVersion = 1

type JobPhase string

const (
	PhasePending    JobPhase = "pending"
	PhaseStaged     JobPhase = "staged"
	PhasePublishing JobPhase = "publishing"
	PhasePublished  JobPhase = "published"
	PhaseRemoved    JobPhase = "removed"
)

type ActivityIntent string

const (
	ActivityRunning ActivityIntent = "running"
	ActivityStopped ActivityIntent = "stopped"
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

type Job struct {
	Version         int            `json:"version"`
	ID              string         `json:"id"`
	Source          string         `json:"source"`
	TargetDir       string         `json:"targetDir"`
	TargetIdentity  ObjectIdentity `json:"targetIdentity"`
	StorageID       string         `json:"storageId"`
	Phase           JobPhase       `json:"phase"`
	ActivityIntent  ActivityIntent `json:"activityIntent"`
	PayloadRoot     string         `json:"payloadRoot,omitempty"`
	DestinationRoot string         `json:"destinationRoot,omitempty"`
	PayloadIdentity ObjectIdentity `json:"payloadIdentity,omitempty"`
	PayloadLength   *int64         `json:"payloadLength,omitempty"`
	ProblemCode     string         `json:"problemCode,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
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
	job.Version = CurrentVersion
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
	job.Version = CurrentVersion
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

// DeleteCorrupt removes only a manifest directory that cannot be decoded.
// Callers must separately prove that aria2 no longer owns the GID.
func (repository *Repository) DeleteCorrupt(id string) error {
	if !repository.Exists(id) {
		return os.ErrNotExist
	}
	if _, _, err := repository.Load(id); err == nil {
		return errors.New("refusing corrupt delete for a valid manifest")
	}
	tombstone := filepath.Join(repository.jobsDir, "."+id+".corrupt-deleting")
	if _, err := os.Lstat(tombstone); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("corrupt manifest tombstone already exists")
	}
	if err := os.Rename(repository.jobDir(id), tombstone); err != nil {
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
		scope.Version = CurrentVersion
	}
	if !ValidID(scope.ID) || scope.MountPoint == "" || scope.StagingAnchor == "" {
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
	if scope.Version != CurrentVersion || scope.ID != id {
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

// FinalRoot returns the durable publication destination. Empty
// DestinationRoot is the backward-compatible representation used by manifests
// created before conflict-free publication naming.
func (job Job) FinalRoot() string {
	if job.DestinationRoot != "" {
		return job.DestinationRoot
	}
	return job.PayloadRoot
}

func (job Job) PublicationRenamed() bool {
	return job.PayloadRoot != "" && job.FinalRoot() != job.PayloadRoot
}

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
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return nil, Token{}, err
	}
	data = append(data, '\n')
	return data, sha256.Sum256(data), nil
}

func decodeJob(id string, data []byte) (Job, error) {
	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		return Job{}, err
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
	if job.Version != 0 && job.Version != CurrentVersion {
		return errors.New("unsupported job manifest version")
	}
	if !ValidID(job.ID) || job.Source == "" || !filepath.IsAbs(job.TargetDir) || !ValidID(job.StorageID) || job.TargetIdentity.MountID == 0 || job.TargetIdentity.ObjectID == 0 {
		return errors.New("invalid job manifest")
	}
	switch job.Phase {
	case PhasePending, PhaseStaged, PhasePublishing, PhasePublished, PhaseRemoved:
	default:
		return errors.New("invalid job phase")
	}
	if job.ActivityIntent != ActivityRunning && job.ActivityIntent != ActivityStopped {
		return errors.New("invalid activity intent")
	}
	if job.PayloadRoot != "" && (filepath.IsAbs(job.PayloadRoot) || strings.HasPrefix(filepath.Clean(job.PayloadRoot), "..")) {
		return errors.New("invalid payload root")
	}
	if job.DestinationRoot != "" {
		clean := filepath.Clean(job.DestinationRoot)
		if filepath.IsAbs(job.DestinationRoot) || clean == "." || clean == ".." ||
			strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.Base(clean) != clean {
			return errors.New("invalid destination root")
		}
	}
	if job.Phase == PhasePublished && job.PublicationRenamed() && job.ActivityIntent != ActivityStopped {
		return errors.New("renamed published task must be stopped")
	}
	if (job.Phase == PhasePublishing || job.Phase == PhasePublished) && (job.PayloadRoot == "" || job.PayloadIdentity.MountID == 0 || job.PayloadIdentity.ObjectID == 0) {
		return errors.New("publication phase requires payload identity")
	}
	if job.PayloadLength != nil && *job.PayloadLength < 0 {
		return errors.New("invalid payload length")
	}
	return nil
}
