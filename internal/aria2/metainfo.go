package aria2

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"strconv"
)

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
	info, err := metainfoInfo(data)
	if err != nil {
		return 0, err
	}
	index := 1
	var singleLength, filesLength int64
	var hasSingle, hasFiles bool
	for index < len(info) && info[index] != 'e' {
		key, next, parseErr := parseBString(info, index)
		if parseErr != nil {
			return 0, parseErr
		}
		index = next
		switch string(key) {
		case "length":
			if hasSingle {
				return 0, errors.New("torrent info has duplicate length")
			}
			singleLength, index, err = parseNonnegativeBInteger(info, index)
			if err != nil {
				return 0, err
			}
			hasSingle = true
		case "files":
			if hasFiles {
				return 0, errors.New("torrent info has duplicate files")
			}
			filesLength, index, err = parseFileLengths(info, index)
			if err != nil {
				return 0, err
			}
			hasFiles = true
		default:
			index, err = skipBencode(info, index, 0)
			if err != nil {
				return 0, err
			}
		}
	}
	switch {
	case hasSingle && hasFiles:
		return 0, errors.New("torrent info mixes single-file and multi-file layouts")
	case hasSingle:
		return singleLength, nil
	case hasFiles:
		return filesLength, nil
	default:
		return 0, errors.New("torrent info has no v1 payload length")
	}
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

func parseFileLengths(data []byte, index int) (int64, int, error) {
	if index >= len(data) || data[index] != 'l' {
		return 0, 0, errors.New("torrent files is not a list")
	}
	index++
	var total int64
	for index < len(data) && data[index] != 'e' {
		if data[index] != 'd' {
			return 0, 0, errors.New("torrent file entry is not a dictionary")
		}
		index++
		var length int64
		hasLength := false
		for index < len(data) && data[index] != 'e' {
			key, next, err := parseBString(data, index)
			if err != nil {
				return 0, 0, err
			}
			index = next
			if string(key) == "length" {
				if hasLength {
					return 0, 0, errors.New("torrent file has duplicate length")
				}
				length, index, err = parseNonnegativeBInteger(data, index)
				if err != nil {
					return 0, 0, err
				}
				hasLength = true
				continue
			}
			index, err = skipBencode(data, index, 0)
			if err != nil {
				return 0, 0, err
			}
		}
		if index >= len(data) || !hasLength {
			return 0, 0, errors.New("torrent file has no length")
		}
		index++
		if length > (1<<63-1)-total {
			return 0, 0, errors.New("torrent payload length overflows int64")
		}
		total += length
	}
	if index >= len(data) {
		return 0, 0, errors.New("unterminated torrent files list")
	}
	return total, index + 1, nil
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
