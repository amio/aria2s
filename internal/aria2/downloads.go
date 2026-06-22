package aria2

import (
	"context"
	"path/filepath"
	"strconv"
)

/** ListOptions bounds aria2 history reads for the interactive console. */
type ListOptions struct {
	WaitingLimit  int
	StoppedOffset int
	StoppedLimit  int
}

/** DownloadSnapshot groups the live and recent stopped task windows shown by the console. */
type DownloadSnapshot struct {
	Active  []Download
	Waiting []Download
	Stopped []Download
}

/** Download is the compact task row used by list views. */
type Download struct {
	GID             string
	Status          string
	Name            string
	CompletedLength int64
	TotalLength     int64
	DownloadSpeed   int64
	UploadSpeed     int64
}

/** DownloadDetail is the selected task payload fetched on demand. */
type DownloadDetail struct {
	GID             string
	Status          string
	Name            string
	CompletedLength int64
	TotalLength     int64
	DownloadSpeed   int64
	UploadSpeed     int64
	PrimaryURI      string
	Connections     int64
	ErrorCode       string
	ErrorMessage    string
	Files           []DownloadFile
}

/** DownloadFile is a single file entry inside a task detail payload. */
type DownloadFile struct {
	Path            string
	Name            string
	Length          int64
	CompletedLength int64
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
		Stopped: mapDownloads(stopped),
	}, nil
}

func (client *RPCClient) TaskDetail(ctx context.Context, gid string) (DownloadDetail, error) {
	var raw rawDownload
	if err := client.call(ctx, "aria2.tellStatus", []any{gid, detailFields()}, &raw); err != nil {
		return DownloadDetail{}, err
	}
	download := raw.toDownload()
	detail := DownloadDetail{
		GID:             download.GID,
		Status:          download.Status,
		Name:            download.Name,
		CompletedLength: download.CompletedLength,
		TotalLength:     download.TotalLength,
		DownloadSpeed:   download.DownloadSpeed,
		UploadSpeed:     download.UploadSpeed,
		PrimaryURI:      raw.primaryURI(),
		Connections:     parseInt(raw.Connections),
		ErrorCode:       raw.ErrorCode,
		ErrorMessage:    raw.ErrorMessage,
	}
	for _, file := range raw.Files {
		detail.Files = append(detail.Files, DownloadFile{
			Path:            file.Path,
			Name:            fileName(file.Path),
			Length:          parseInt(file.Length),
			CompletedLength: parseInt(file.CompletedLength),
		})
	}
	return detail, nil
}

func (client *RPCClient) Pause(ctx context.Context, gid string) error {
	var ignored string
	return client.call(ctx, "aria2.pause", []any{gid}, &ignored)
}

func (client *RPCClient) Resume(ctx context.Context, gid string) error {
	var ignored string
	return client.call(ctx, "aria2.unpause", []any{gid}, &ignored)
}

func (client *RPCClient) Remove(ctx context.Context, gid string) error {
	var ignored string
	return client.call(ctx, "aria2.remove", []any{gid}, &ignored)
}

func (client *RPCClient) RemoveDownloadResult(ctx context.Context, gid string) error {
	var ignored string
	return client.call(ctx, "aria2.removeDownloadResult", []any{gid}, &ignored)
}

type rawDownload struct {
	GID             string    `json:"gid"`
	Status          string    `json:"status"`
	CompletedLength string    `json:"completedLength"`
	TotalLength     string    `json:"totalLength"`
	DownloadSpeed   string    `json:"downloadSpeed"`
	UploadSpeed     string    `json:"uploadSpeed"`
	Connections     string    `json:"connections"`
	ErrorCode       string    `json:"errorCode"`
	ErrorMessage    string    `json:"errorMessage"`
	Files           []rawFile `json:"files"`
	Bittorrent      struct {
		Info struct {
			Name string `json:"name"`
		} `json:"info"`
	} `json:"bittorrent"`
}

type rawFile struct {
	Path            string   `json:"path"`
	Length          string   `json:"length"`
	CompletedLength string   `json:"completedLength"`
	URIs            []rawURI `json:"uris"`
}

type rawURI struct {
	URI string `json:"uri"`
}

func (raw rawDownload) toDownload() Download {
	return Download{
		GID:             raw.GID,
		Status:          raw.Status,
		Name:            raw.name(),
		CompletedLength: parseInt(raw.CompletedLength),
		TotalLength:     parseInt(raw.TotalLength),
		DownloadSpeed:   parseInt(raw.DownloadSpeed),
		UploadSpeed:     parseInt(raw.UploadSpeed),
	}
}

func (raw rawDownload) name() string {
	if raw.Bittorrent.Info.Name != "" {
		return raw.Bittorrent.Info.Name
	}
	for _, file := range raw.Files {
		if file.Path != "" {
			return fileName(file.Path)
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
	return []string{"gid", "status", "files", "bittorrent", "completedLength", "totalLength", "downloadSpeed", "uploadSpeed"}
}

func detailFields() []string {
	return []string{"gid", "status", "files", "bittorrent", "completedLength", "totalLength", "downloadSpeed", "uploadSpeed", "connections", "errorCode", "errorMessage"}
}
