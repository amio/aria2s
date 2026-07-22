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
	if len(data) == 0 || data[0] != 'd' {
		return "", errors.New("torrent metainfo is not a dictionary")
	}
	index := 1
	var info []byte
	for index < len(data) && data[index] != 'e' {
		key, next, err := parseBString(data, index)
		if err != nil {
			return "", err
		}
		index = next
		start := index
		index, err = skipBencode(data, index, 0)
		if err != nil {
			return "", err
		}
		if string(key) == "info" {
			if len(info) != 0 || data[start] != 'd' {
				return "", errors.New("torrent info dictionary is invalid or duplicated")
			}
			info = data[start:index]
		}
	}
	if index != len(data)-1 || data[index] != 'e' || len(info) == 0 {
		return "", errors.New("torrent metainfo has trailing data or no info dictionary")
	}
	hash := sha1.Sum(info)
	return hex.EncodeToString(hash[:]), nil
}

func skipBencode(data []byte, index, depth int) (int, error) {
	if depth > 64 || index >= len(data) {
		return 0, errors.New("invalid bencode nesting")
	}
	switch data[index] {
	case 'i':
		end := index + 1
		for end < len(data) && data[end] != 'e' {
			end++
		}
		if end >= len(data) || end == index+1 {
			return 0, errors.New("invalid bencode integer")
		}
		if _, err := strconv.ParseInt(string(data[index+1:end]), 10, 64); err != nil {
			return 0, err
		}
		return end + 1, nil
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
