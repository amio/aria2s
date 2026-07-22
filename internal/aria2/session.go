package aria2

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// SessionBlock is one validated aria2 input/session entry. URI and transport
// options are preserved; app policy may clone and override only managed keys.
type SessionBlock struct {
	URI     string
	Options []SessionOption
}

type SessionOption struct {
	Key   string
	Value string
}

type SessionParseError struct {
	Line int
	Err  error
}

func (parseErr SessionParseError) Error() string {
	return fmt.Sprintf("session line %d: %v", parseErr.Line, parseErr.Err)
}

func ParseSession(data []byte) ([]SessionBlock, []SessionParseError) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var blocks []SessionBlock
	var problems []SessionParseError
	var current *SessionBlock
	invalid := false
	flush := func() {
		if current != nil && !invalid {
			blocks = append(blocks, *current)
		}
		current = nil
		invalid = false
	}
	for index, raw := range lines {
		lineNumber := index + 1
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), "#") {
			continue
		}
		if raw[0] != ' ' && raw[0] != '\t' {
			flush()
			uri := strings.TrimSpace(raw)
			if strings.ContainsAny(uri, "\x00\r\n") {
				problems = append(problems, SessionParseError{Line: lineNumber, Err: errors.New("invalid URI line")})
				invalid = true
				current = &SessionBlock{}
				continue
			}
			current = &SessionBlock{URI: uri}
			continue
		}
		if current == nil {
			problems = append(problems, SessionParseError{Line: lineNumber, Err: errors.New("option without URI")})
			continue
		}
		option := strings.TrimSpace(raw)
		key, value, ok := strings.Cut(option, "=")
		key = strings.TrimSpace(key)
		if !ok || !validOptionKey(key) || strings.ContainsAny(value, "\x00\r\n") {
			problems = append(problems, SessionParseError{Line: lineNumber, Err: errors.New("invalid option")})
			invalid = true
			continue
		}
		if _, exists := current.Option(key); exists {
			problems = append(problems, SessionParseError{Line: lineNumber, Err: fmt.Errorf("duplicate option %s", key)})
			invalid = true
			continue
		}
		current.Options = append(current.Options, SessionOption{Key: key, Value: value})
	}
	flush()
	return blocks, problems
}

func EncodeSession(blocks []SessionBlock) ([]byte, error) {
	var builder strings.Builder
	for _, block := range blocks {
		if strings.TrimSpace(block.URI) == "" || strings.ContainsAny(block.URI, "\x00\r\n") {
			return nil, errors.New("invalid session URI")
		}
		seen := make(map[string]struct{}, len(block.Options))
		builder.WriteString(block.URI)
		builder.WriteByte('\n')
		for _, option := range block.Options {
			if !validOptionKey(option.Key) || strings.ContainsAny(option.Value, "\x00\r\n") {
				return nil, errors.New("invalid session option")
			}
			if _, exists := seen[option.Key]; exists {
				return nil, fmt.Errorf("duplicate session option %s", option.Key)
			}
			seen[option.Key] = struct{}{}
			builder.WriteByte(' ')
			builder.WriteString(option.Key)
			builder.WriteByte('=')
			builder.WriteString(option.Value)
			builder.WriteByte('\n')
		}
	}
	return []byte(builder.String()), nil
}

func (block SessionBlock) Clone() SessionBlock {
	clone := block
	clone.Options = append([]SessionOption(nil), block.Options...)
	return clone
}

func (block SessionBlock) Option(key string) (string, bool) {
	for _, option := range block.Options {
		if option.Key == key {
			return option.Value, true
		}
	}
	return "", false
}

func (block *SessionBlock) SetOption(key, value string) {
	for index := range block.Options {
		if block.Options[index].Key == key {
			block.Options[index].Value = value
			return
		}
	}
	block.Options = append(block.Options, SessionOption{Key: key, Value: value})
}

func (block *SessionBlock) DeleteOption(key string) {
	filtered := block.Options[:0]
	for _, option := range block.Options {
		if option.Key != key {
			filtered = append(filtered, option)
		}
	}
	block.Options = filtered
}

func (block *SessionBlock) SortOptions() {
	sort.SliceStable(block.Options, func(i, j int) bool { return block.Options[i].Key < block.Options[j].Key })
}

func validOptionKey(key string) bool {
	if key == "" {
		return false
	}
	for _, char := range key {
		if char != '-' && char != '_' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}
