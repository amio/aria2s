package aria2

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"strconv"
)

// MetainfoLayout is the stable payload shape declared by a v1 torrent. Paths
// are kept as components so consumers can safely place them under their own
// authoritative payload root.
type MetainfoLayout struct {
	MultiFile bool
	Files     []MetainfoFile
}

type MetainfoFile struct {
	Path   []string
	Length int64
}

// ValidateMetainfo performs structural bencode validation and returns the
// canonical SHA-1 info hash. It intentionally does not interpret tracker or
// file policy; aria2 remains the transport owner.
func ValidateMetainfo(data []byte) (string, error) {
	info, err := metainfoInfo(data)
	if err != nil {
		return "", err
	}
	hash := sha1.Sum(info)
	return hex.EncodeToString(hash[:]), nil
}

// MetainfoTotalLength returns the exact logical payload length declared by a
// validated v1 torrent metainfo dictionary.
func MetainfoTotalLength(data []byte) (int64, error) {
	layout, err := MetainfoFileLayout(data)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, file := range layout.Files {
		if file.Length > (1<<63-1)-total {
			return 0, errors.New("torrent payload length overflows int64")
		}
		total += file.Length
	}
	return total, nil
}

// MetainfoFileLayout returns the single- or multi-file payload layout declared
// by validated v1 torrent metainfo. UTF-8 aliases take precedence when present.
func MetainfoFileLayout(data []byte) (MetainfoLayout, error) {
	info, err := metainfoInfo(data)
	if err != nil {
		return MetainfoLayout{}, err
	}
	index := 1
	var name, utf8Name string
	var singleLength int64
	var files []MetainfoFile
	var hasName, hasUTF8Name, hasSingle, hasFiles bool
	for index < len(info) && info[index] != 'e' {
		key, next, parseErr := parseBString(info, index)
		if parseErr != nil {
			return MetainfoLayout{}, parseErr
		}
		index = next
		switch string(key) {
		case "name":
			if hasName {
				return MetainfoLayout{}, errors.New("torrent info has duplicate name")
			}
			value, next, err := parseBString(info, index)
			if err != nil {
				return MetainfoLayout{}, err
			}
			name, index, hasName = string(value), next, true
		case "name.utf-8":
			if hasUTF8Name {
				return MetainfoLayout{}, errors.New("torrent info has duplicate UTF-8 name")
			}
			value, next, err := parseBString(info, index)
			if err != nil {
				return MetainfoLayout{}, err
			}
			utf8Name, index, hasUTF8Name = string(value), next, true
		case "length":
			if hasSingle {
				return MetainfoLayout{}, errors.New("torrent info has duplicate length")
			}
			singleLength, index, err = parseNonnegativeBInteger(info, index)
			if err != nil {
				return MetainfoLayout{}, err
			}
			hasSingle = true
		case "files":
			if hasFiles {
				return MetainfoLayout{}, errors.New("torrent info has duplicate files")
			}
			files, index, err = parseMetainfoFiles(info, index)
			if err != nil {
				return MetainfoLayout{}, err
			}
			hasFiles = true
		default:
			index, err = skipBencode(info, index, 0)
			if err != nil {
				return MetainfoLayout{}, err
			}
		}
	}
	if utf8Name != "" {
		name = utf8Name
	}
	if name == "" {
		return MetainfoLayout{}, errors.New("torrent info has no payload name")
	}
	switch {
	case hasSingle && hasFiles:
		return MetainfoLayout{}, errors.New("torrent info mixes single-file and multi-file layouts")
	case hasSingle:
		return MetainfoLayout{Files: []MetainfoFile{{Path: []string{name}, Length: singleLength}}}, nil
	case hasFiles:
		return MetainfoLayout{MultiFile: true, Files: files}, nil
	default:
		return MetainfoLayout{}, errors.New("torrent info has no v1 payload layout")
	}
}

func parseMetainfoFiles(data []byte, index int) ([]MetainfoFile, int, error) {
	if index >= len(data) || data[index] != 'l' {
		return nil, 0, errors.New("torrent files is not a list")
	}
	index++
	var files []MetainfoFile
	for index < len(data) && data[index] != 'e' {
		if data[index] != 'd' {
			return nil, 0, errors.New("torrent file entry is not a dictionary")
		}
		index++
		var length int64
		var path, utf8Path []string
		var hasLength, hasPath, hasUTF8Path bool
		for index < len(data) && data[index] != 'e' {
			key, next, err := parseBString(data, index)
			if err != nil {
				return nil, 0, err
			}
			index = next
			switch string(key) {
			case "length":
				if hasLength {
					return nil, 0, errors.New("torrent file has duplicate length")
				}
				length, index, err = parseNonnegativeBInteger(data, index)
				if err != nil {
					return nil, 0, err
				}
				hasLength = true
			case "path":
				if hasPath {
					return nil, 0, errors.New("torrent file has duplicate path")
				}
				path, index, err = parseMetainfoPath(data, index)
				if err != nil {
					return nil, 0, err
				}
				hasPath = true
			case "path.utf-8":
				if hasUTF8Path {
					return nil, 0, errors.New("torrent file has duplicate UTF-8 path")
				}
				utf8Path, index, err = parseMetainfoPath(data, index)
				if err != nil {
					return nil, 0, err
				}
				hasUTF8Path = true
			default:
				index, err = skipBencode(data, index, 0)
				if err != nil {
					return nil, 0, err
				}
			}
		}
		if index >= len(data) || !hasLength || (!hasPath && !hasUTF8Path) {
			return nil, 0, errors.New("torrent file has no length or path")
		}
		index++
		if hasUTF8Path {
			path = utf8Path
		}
		files = append(files, MetainfoFile{Path: path, Length: length})
	}
	if index >= len(data) {
		return nil, 0, errors.New("unterminated torrent files list")
	}
	return files, index + 1, nil
}

func parseMetainfoPath(data []byte, index int) ([]string, int, error) {
	if index >= len(data) || data[index] != 'l' {
		return nil, 0, errors.New("torrent file path is not a list")
	}
	index++
	var path []string
	for index < len(data) && data[index] != 'e' {
		component, next, err := parseBString(data, index)
		if err != nil {
			return nil, 0, err
		}
		path = append(path, string(component))
		index = next
	}
	if index >= len(data) || len(path) == 0 {
		return nil, 0, errors.New("torrent file path is empty or unterminated")
	}
	return path, index + 1, nil
}

func metainfoInfo(data []byte) ([]byte, error) {
	if len(data) == 0 || data[0] != 'd' {
		return nil, errors.New("torrent metainfo is not a dictionary")
	}
	index := 1
	var info []byte
	for index < len(data) && data[index] != 'e' {
		key, next, err := parseBString(data, index)
		if err != nil {
			return nil, err
		}
		index = next
		start := index
		index, err = skipBencode(data, index, 0)
		if err != nil {
			return nil, err
		}
		if string(key) == "info" {
			if len(info) != 0 || data[start] != 'd' {
				return nil, errors.New("torrent info dictionary is invalid or duplicated")
			}
			info = data[start:index]
		}
	}
	if index != len(data)-1 || data[index] != 'e' || len(info) == 0 {
		return nil, errors.New("torrent metainfo has trailing data or no info dictionary")
	}
	return info, nil
}

func parseNonnegativeBInteger(data []byte, index int) (int64, int, error) {
	value, next, err := parseBInteger(data, index)
	if err != nil {
		return 0, 0, err
	}
	if value < 0 {
		return 0, 0, errors.New("torrent length is negative")
	}
	return value, next, nil
}

func parseBInteger(data []byte, index int) (int64, int, error) {
	if index >= len(data) || data[index] != 'i' {
		return 0, 0, errors.New("invalid bencode integer")
	}
	end := index + 1
	for end < len(data) && data[end] != 'e' {
		end++
	}
	if end >= len(data) || end == index+1 {
		return 0, 0, errors.New("invalid bencode integer")
	}
	value, err := strconv.ParseInt(string(data[index+1:end]), 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return value, end + 1, nil
}

func skipBencode(data []byte, index, depth int) (int, error) {
	if depth > 64 || index >= len(data) {
		return 0, errors.New("invalid bencode nesting")
	}
	switch data[index] {
	case 'i':
		_, next, err := parseBInteger(data, index)
		return next, err
	case 'l':
		index++
		for index < len(data) && data[index] != 'e' {
			var err error
			index, err = skipBencode(data, index, depth+1)
			if err != nil {
				return 0, err
			}
		}
		if index >= len(data) {
			return 0, errors.New("unterminated bencode list")
		}
		return index + 1, nil
	case 'd':
		index++
		for index < len(data) && data[index] != 'e' {
			_, next, err := parseBString(data, index)
			if err != nil {
				return 0, err
			}
			index = next
			index, err = skipBencode(data, index, depth+1)
			if err != nil {
				return 0, err
			}
		}
		if index >= len(data) {
			return 0, errors.New("unterminated bencode dictionary")
		}
		return index + 1, nil
	default:
		_, next, err := parseBString(data, index)
		return next, err
	}
}

func parseBString(data []byte, index int) ([]byte, int, error) {
	colon := index
	for colon < len(data) && data[colon] != ':' {
		if data[colon] < '0' || data[colon] > '9' {
			return nil, 0, errors.New("invalid bencode string length")
		}
		colon++
	}
	if colon == index || colon >= len(data) {
		return nil, 0, errors.New("invalid bencode string")
	}
	length, err := strconv.Atoi(string(data[index:colon]))
	if err != nil || length < 0 {
		return nil, 0, errors.New("invalid bencode string length")
	}
	start, end := colon+1, colon+1+length
	if end < start || end > len(data) {
		return nil, 0, errors.New("truncated bencode string")
	}
	return data[start:end], end, nil
}
