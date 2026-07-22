package aria2

import (
	"strings"
	"testing"
)

func TestParseSessionIsolatesInvalidBlockAndRoundTripsValidOptions(t *testing.T) {
	input := "https://example.test/one\n gid=0123456789abcdef\n header=opaque:value\n" +
		"https://example.test/bad\n gid=bad\n gid=duplicate\n" +
		"magnet:?xt=urn:btih:abc\n gid=fedcba9876543210\n pause=true\n"
	blocks, problems := ParseSession([]byte(input))
	if len(blocks) != 2 || len(problems) != 1 {
		t.Fatalf("blocks=%d problems=%v", len(blocks), problems)
	}
	encoded, err := EncodeSession(blocks)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "header=opaque:value") || strings.Contains(string(encoded), "duplicate") {
		t.Fatalf("encoded = %s", encoded)
	}
}
