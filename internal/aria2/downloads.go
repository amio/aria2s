package aria2

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

/** ListOptions bounds aria2 history reads for the interactive dashboard. */
type ListOptions struct {
	WaitingLimit  int
	StoppedOffset int
	StoppedLimit  int
}

/** ListQuery identifies the bounded list window in a dashboard snapshot. */
type ListQuery = ListOptions

/** DashboardQuery describes one immutable batched dashboard read. */
type DashboardQuery struct {
	List                ListQuery
	DetailGID           string
	ResolveDetailSource bool
	ManagedGIDs         []string
}

/** DashboardRead contains independently valid list and detail portions. */
type DashboardRead struct {
	Downloads       DownloadSnapshot
	ListErr         error
	Detail          *DownloadDetail
	DetailErr       error
	DetailSourceErr error
	Managed         map[string]*Download
}

/** DownloadSnapshot groups the live and recent stopped task windows shown by the dashboard. */
type DownloadSnapshot struct {
	Active  []Download
	Waiting []Download
	Stopped []Download
}

/** Download is the compact task row used by list views. */
type Download struct {
	GID               string
	Status            string
	Dir               string
	Name              string
	IsMetadata        bool
	CompletedLength   int64
	TotalLength       int64
	LengthKnown       bool
	DownloadSpeed     int64
	UploadSpeed       int64
	UploadLength      int64
	UploadLengthKnown bool
	NumSeeders        int64
	Connections       int64
	Seeder            bool
	InfoHash          string
	AddedAt           time.Time
	CanonicalStatus   string
	Ownership         string
	IssueCode         string
	IssueText         string
	Actions           []string
}

/** DownloadDetail is the selected task payload fetched on demand. */
type DownloadDetail struct {
	GID                    string
	Status                 string
	Name                   string
	IsMetadata             bool
	CompletedLength        int64
	TotalLength            int64
	LengthKnown            bool
	DownloadSpeed          int64
	UploadSpeed            int64
	UploadLength           int64
	VerifiedLength         int64
	VerifyIntegrityPending bool
	InfoHash               string
	NumSeeders             int64
	Seeder                 bool
	PieceLength            int64
	NumPieces              int64
	PrimaryURI             string
	DownloadDir            string
	Connections            int64
	ErrorCode              string
	ErrorMessage           string
	Files                  []DownloadFile
	CanonicalStatus        string
	Ownership              string
	IssueCode              string
	IssueText              string
	Actions                []string
}

/** DownloadFile is a single file entry inside a task detail payload. */
type DownloadFile struct {
	Path            string
	Name            string
	Length          int64
	CompletedLength int64
	Selected        bool
}

func (client *RPCClient) ListDownloads(ctx context.Context, options ListOptions) (DownloadSnapshot, error) {
	if options.WaitingLimit <= 0 {
		options.WaitingLimit = 100
	}
	if options.StoppedLimit <= 0 {
		options.StoppedLimit = 100
	}
	var active, waiting, stopped []rawDownload
	if err := client.call(ctx, "aria2.tellActive", []any{downloadFields()}, &active); err != nil {
		return DownloadSnapshot{}, err
	}
	if err := client.call(ctx, "aria2.tellWaiting", []any{0, options.WaitingLimit, downloadFields()}, &waiting); err != nil {
		return DownloadSnapshot{}, err
	}
	if err := client.call(ctx, "aria2.tellStopped", []any{options.StoppedOffset, options.StoppedLimit, downloadFields()}, &stopped); err != nil {
		return DownloadSnapshot{}, err
	}
	return DownloadSnapshot{
		Active:  mapDownloads(active),
		Waiting: mapDownloads(waiting),
		Stopped: filterMetadataStopped(mapDownloads(stopped)),
	}, nil
}

func (client *RPCClient) TaskDetail(ctx context.Context, gid string) (DownloadDetail, error) {
	var raw rawDownload
	if err := client.call(ctx, "aria2.tellStatus", []any{gid, detailFields()}, &raw); err != nil {
		return DownloadDetail{}, err
	}
	detail := raw.toDetail()
	primaryURI := raw.primaryURI()
	if primaryURI == "" {
		if uris, err := client.GetURIs(ctx, gid); err == nil && len(uris) > 0 {
			primaryURI = uris[0].URI
		}
	}
	if primaryURI == "" && raw.InfoHash != "" {
		primaryURI = magnetURI(raw.InfoHash)
	}
	detail.PrimaryURI = primaryURI
	return detail, nil
}

func (client *RPCClient) GetURIs(ctx context.Context, gid string) ([]rawURI, error) {
	var uris []rawURI
	if err := client.call(ctx, "aria2.getUris", []any{gid}, &uris); err != nil {
		return nil, err
	}
	return uris, nil
}

func (client *RPCClient) Pause(ctx context.Context, gid string) error {
	var ignored string
	return client.mutation(ctx, "aria2.forcePause", []any{gid}, &ignored)
}

func (client *RPCClient) Resume(ctx context.Context, gid string) error {
	var ignored string
	return client.mutation(ctx, "aria2.unpause", []any{gid}, &ignored)
}

func (client *RPCClient) Remove(ctx context.Context, gid string) error {
	var ignored string
	return client.mutation(ctx, "aria2.remove", []any{gid}, &ignored)
}

func (client *RPCClient) ForceRemove(ctx context.Context, gid string) error {
	var ignored string
	return client.mutation(ctx, "aria2.forceRemove", []any{gid}, &ignored)
}

func (client *RPCClient) RemoveDownloadResult(ctx context.Context, gid string) error {
	var ignored string
	return client.mutation(ctx, "aria2.removeDownloadResult", []any{gid}, &ignored)
}

type LifecycleStatus struct {
	GID             string
	Status          string
	Dir             string
	InfoHash        string
	Seeder          bool
	CompletedLength int64
	TotalLength     int64
	Files           []DownloadFile
}

func (client *RPCClient) CompleteCensus(ctx context.Context) ([]LifecycleStatus, error) {
	var all []rawDownload
	var active []rawDownload
	if err := client.call(ctx, "aria2.tellActive", []any{detailFields()}, &active); err != nil {
		return nil, err
	}
	all = append(all, active...)
	for _, method := range []string{"aria2.tellWaiting", "aria2.tellStopped"} {
		for offset := 0; ; offset += 100 {
			var page []rawDownload
			if err := client.call(ctx, method, []any{offset, 100, detailFields()}, &page); err != nil {
				return nil, err
			}
			all = append(all, page...)
			if len(page) < 100 {
				break
			}
		}
	}
	result := make([]LifecycleStatus, 0, len(all))
	for _, raw := range all {
		detail := raw.toDetail()
		result = append(result, LifecycleStatus{GID: raw.GID, Status: raw.Status, Dir: raw.Dir, InfoHash: raw.InfoHash, Seeder: raw.Seeder == "true", CompletedLength: detail.CompletedLength, TotalLength: detail.TotalLength, Files: detail.Files})
	}
	return result, nil
}

func (client *RPCClient) LifecycleStatus(ctx context.Context, gid string) (LifecycleStatus, error) {
	var raw rawDownload
	if err := client.call(ctx, "aria2.tellStatus", []any{gid, []string{"gid", "status", "dir", "infoHash", "seeder", "completedLength", "totalLength", "files"}}, &raw); err != nil {
		return LifecycleStatus{}, err
	}
	detail := raw.toDetail()
	return LifecycleStatus{GID: raw.GID, Status: raw.Status, Dir: raw.Dir, InfoHash: raw.InfoHash, Seeder: raw.Seeder == "true", CompletedLength: detail.CompletedLength, TotalLength: detail.TotalLength, Files: detail.Files}, nil
}

func IsNotFound(err error) bool {
	var rpcErr *RPCError
	return errors.As(err, &rpcErr) && (rpcErr.Code == 1 || strings.Contains(strings.ToLower(rpcErr.Message), "not found"))
}

func (client *RPCClient) retryURIs(ctx context.Context, gid string, detail DownloadDetail) ([]string, error) {
	if uris, err := client.GetURIs(ctx, gid); err == nil {
		supported := make([]string, 0, len(uris))
		for _, uri := range uris {
			if uri.URI != "" && isSupportedURI(uri.URI) {
				supported = append(supported, uri.URI)
			}
		}
		if len(supported) > 0 {
			return supported, nil
		}
	}
	if detail.PrimaryURI != "" && isSupportedURI(detail.PrimaryURI) {
		return []string{detail.PrimaryURI}, nil
	}
	if detail.InfoHash != "" {
		return []string{magnetURI(detail.InfoHash)}, nil
	}
	return nil, fmt.Errorf("no supported URI found for download %s", gid)
}

func magnetURI(infoHash string) string {
	return "magnet:?xt=urn:btih:" + strings.ToLower(infoHash)
}

func (client *RPCClient) addURIs(ctx context.Context, uris []string, opts AddOptions) (string, error) {
	for _, uri := range uris {
		if !isSupportedURI(uri) {
			return "", fmt.Errorf("unsupported URI: %s", uri)
		}
	}
	params := []any{uris}
	if opts.Dir != "" {
		params = append(params, map[string]string{"dir": opts.Dir})
	}
	var newGID string
	if err := client.mutation(ctx, "aria2.addUri", params, &newGID); err != nil {
		return "", err
	}
	return newGID, nil
}

/** RetrySource is the immutable input required to safely recreate a failed task. */
type RetrySource struct {
	Status string
	Dir    string
	URIs   []string
}

func (client *RPCClient) RetrySource(ctx context.Context, gid string) (RetrySource, error) {
	detail, err := client.TaskDetail(ctx, gid)
	if err != nil {
		return RetrySource{}, err
	}
	if detail.Status != "error" {
		return RetrySource{}, fmt.Errorf("download status is %q, not error", detail.Status)
	}
	uris, err := client.retryURIs(ctx, gid, detail)
	if err != nil {
		return RetrySource{}, err
	}
	return RetrySource{Status: detail.Status, Dir: detail.DownloadDir, URIs: uris}, nil
}

func (client *RPCClient) AddURIs(ctx context.Context, uris []string, opts AddOptions) (string, error) {
	return client.addURIs(ctx, uris, opts)
}

func (client *RPCClient) SaveSession(ctx context.Context) error {
	var ignored string
	return client.call(ctx, "aria2.saveSession", nil, &ignored)
}

func (client *RPCClient) Shutdown(ctx context.Context) error {
	var ignored string
	return client.call(ctx, "aria2.shutdown", nil, &ignored)
}

type rawDownload struct {
	GID                    string    `json:"gid"`
	Status                 string    `json:"status"`
	Dir                    string    `json:"dir"`
	CompletedLength        string    `json:"completedLength"`
	TotalLength            string    `json:"totalLength"`
	DownloadSpeed          string    `json:"downloadSpeed"`
	UploadSpeed            string    `json:"uploadSpeed"`
	UploadLength           string    `json:"uploadLength"`
	VerifiedLength         string    `json:"verifiedLength"`
	VerifyIntegrityPending string    `json:"verifyIntegrityPending"`
	InfoHash               string    `json:"infoHash"`
	NumSeeders             string    `json:"numSeeders"`
	Seeder                 string    `json:"seeder"`
	PieceLength            string    `json:"pieceLength"`
	NumPieces              string    `json:"numPieces"`
	Connections            string    `json:"connections"`
	ErrorCode              string    `json:"errorCode"`
	ErrorMessage           string    `json:"errorMessage"`
	Files                  []rawFile `json:"files"`
	Bittorrent             struct {
		Info struct {
			Name string `json:"name"`
		} `json:"info"`
	} `json:"bittorrent"`
}

type rawFile struct {
	Path            string   `json:"path"`
	Length          string   `json:"length"`
	CompletedLength string   `json:"completedLength"`
	Selected        string   `json:"selected"`
	URIs            []rawURI `json:"uris"`
}

type rawURI struct {
	URI string `json:"uri"`
}

func (raw rawDownload) toDownload() Download {
	return Download{
		GID:               raw.GID,
		Status:            raw.Status,
		Dir:               raw.Dir,
		Name:              raw.name(),
		IsMetadata:        raw.isMetadata(),
		CompletedLength:   parseInt(raw.CompletedLength),
		TotalLength:       parseInt(raw.TotalLength),
		LengthKnown:       raw.TotalLength != "",
		DownloadSpeed:     parseInt(raw.DownloadSpeed),
		UploadSpeed:       parseInt(raw.UploadSpeed),
		UploadLength:      parseInt(raw.UploadLength),
		UploadLengthKnown: raw.UploadLength != "",
		NumSeeders:        parseInt(raw.NumSeeders),
		Connections:       parseInt(raw.Connections),
		Seeder:            raw.Seeder == "true",
		InfoHash:          raw.InfoHash,
	}
}

func (raw rawDownload) toDetail() DownloadDetail {
	download := raw.toDownload()
	detail := DownloadDetail{GID: download.GID, Status: download.Status, Name: download.Name, IsMetadata: download.IsMetadata, CompletedLength: download.CompletedLength, TotalLength: download.TotalLength, LengthKnown: download.LengthKnown, DownloadSpeed: download.DownloadSpeed, UploadSpeed: download.UploadSpeed, UploadLength: parseInt(raw.UploadLength), VerifiedLength: parseInt(raw.VerifiedLength), VerifyIntegrityPending: raw.VerifyIntegrityPending == "true", InfoHash: raw.InfoHash, NumSeeders: parseInt(raw.NumSeeders), Seeder: raw.Seeder == "true", PieceLength: parseInt(raw.PieceLength), NumPieces: parseInt(raw.NumPieces), PrimaryURI: raw.primaryURI(), DownloadDir: raw.Dir, Connections: parseInt(raw.Connections), ErrorCode: raw.ErrorCode, ErrorMessage: raw.ErrorMessage}
	if detail.PrimaryURI == "" && raw.InfoHash != "" {
		detail.PrimaryURI = magnetURI(raw.InfoHash)
	}
	for _, file := range raw.Files {
		detail.Files = append(detail.Files, DownloadFile{Path: file.Path, Name: fileName(file.Path), Length: parseInt(file.Length), CompletedLength: parseInt(file.CompletedLength), Selected: file.Selected != "false"})
	}
	return detail
}

func (raw rawDownload) isMetadata() bool {
	for _, file := range raw.Files {
		if strings.HasPrefix(file.Path, "[METADATA]") {
			return true
		}
	}
	return false
}

func (raw rawDownload) name() string {
	if raw.Bittorrent.Info.Name != "" {
		return raw.Bittorrent.Info.Name
	}
	for _, file := range raw.Files {
		if file.Path != "" {
			return displayName(file.Path)
		}
	}
	return raw.GID
}

func (raw rawDownload) primaryURI() string {
	for _, file := range raw.Files {
		for _, uri := range file.URIs {
			if uri.URI != "" {
				return uri.URI
			}
		}
	}
	return ""
}

func mapDownloads(raw []rawDownload) []Download {
	downloads := make([]Download, 0, len(raw))
	for _, item := range raw {
		downloads = append(downloads, item.toDownload())
	}
	return downloads
}

func filterMetadataStopped(downloads []Download) []Download {
	result := make([]Download, 0, len(downloads))
	for _, d := range downloads {
		if d.IsMetadata && isStoppedStatus(d.Status) {
			continue
		}
		result = append(result, d)
	}
	return result
}

func isStoppedStatus(status string) bool {
	return status == "complete" || status == "error" || status == "removed"
}

func displayName(path string) string {
	if strings.HasPrefix(path, "[METADATA]") {
		decoded := strings.TrimPrefix(path, "[METADATA]")
		if unescaped, err := url.QueryUnescape(decoded); err == nil {
			return unescaped
		}
		return decoded
	}
	return fileName(path)
}

func parseInt(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func fileName(path string) string {
	name := filepath.Base(path)
	if name == "." || name == "/" {
		return path
	}
	return name
}

func downloadFields() []string {
	return []string{"gid", "status", "dir", "files", "bittorrent", "completedLength", "totalLength", "downloadSpeed", "uploadSpeed", "uploadLength", "infoHash", "numSeeders", "connections", "seeder"}
}

func detailFields() []string {
	return []string{"gid", "status", "dir", "files", "bittorrent", "completedLength", "totalLength", "downloadSpeed", "uploadSpeed", "uploadLength", "verifiedLength", "verifyIntegrityPending", "infoHash", "numSeeders", "seeder", "pieceLength", "numPieces", "connections", "errorCode", "errorMessage"}
}
