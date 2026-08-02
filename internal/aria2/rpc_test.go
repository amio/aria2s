package aria2_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
)

func TestAddURIAddsTokenAndPayload(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method got %s, want POST", r.Method)
		}
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":"2089b05ecca3d829"}`)
	}))
	defer server.Close()

	client := aria2.NewRPCClient(server.URL, "secret-token", server.Client())
	result, err := client.AddURI(context.Background(), "https://example.com/file.zip", aria2.AddOptions{})
	if err != nil {
		t.Fatalf("add uri: %v", err)
	}
	if result != "2089b05ecca3d829" {
		t.Fatalf("unexpected gid: %s", result)
	}
	assertContains(t, string(body), `"method":"aria2.addUri"`)
	assertContains(t, string(body), `"token:secret-token"`)
	assertContains(t, string(body), `"https://example.com/file.zip"`)
	if strings.Contains(string(body), `"dir"`) {
		t.Fatalf("payload should omit dir when unset, got: %s", body)
	}
}

func TestAddURISendsDirOptionWhenSet(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":"2089b05ecca3d829"}`)
	}))
	defer server.Close()

	client := aria2.NewRPCClient(server.URL, "secret-token", server.Client())
	_, err := client.AddURI(context.Background(), "https://example.com/file.zip", aria2.AddOptions{Dir: "/data/Movies"})
	if err != nil {
		t.Fatalf("add uri: %v", err)
	}
	assertContains(t, string(body), `"dir":"/data/Movies"`)
}

func TestAddURIRejectsUnsupportedSchemes(t *testing.T) {
	client := aria2.NewRPCClient("http://127.0.0.1:6800/jsonrpc", "secret-token", http.DefaultClient)

	_, err := client.AddURI(context.Background(), "ftp://example.com/file.zip", aria2.AddOptions{})

	if err == nil {
		t.Fatal("expected unsupported URL to fail")
	}
	if !strings.Contains(err.Error(), "unsupported URI") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManagedTorrentOverridesUnverifiedSeedingByPublicationPhase(t *testing.T) {
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		bodies = append(bodies, body)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":"0123456789abcdef"}`)
	}))
	defer server.Close()

	client := aria2.NewRPCClient(server.URL, "secret-token", server.Client())
	if _, err := client.AddTorrent(
		context.Background(),
		[]byte("metainfo"),
		aria2.AddOptions{Managed: true},
	); err != nil {
		t.Fatalf("add staged torrent: %v", err)
	}
	forceSave := false
	removeControl := true
	if _, err := client.AddTorrent(
		context.Background(),
		[]byte("metainfo"),
		aria2.AddOptions{Managed: true, SeedUnverified: true, ForceSave: &forceSave, RemoveControlFile: &removeControl},
	); err != nil {
		t.Fatalf("add final seed: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("request count = %d, want 2", len(bodies))
	}
	assertContains(t, string(bodies[0]), `"bt-seed-unverified":"false"`)
	assertContains(t, string(bodies[0]), `"force-save":"true"`)
	assertContains(t, string(bodies[0]), `"remove-control-file":"false"`)
	assertContains(t, string(bodies[1]), `"bt-seed-unverified":"true"`)
	assertContains(t, string(bodies[1]), `"force-save":"false"`)
	assertContains(t, string(bodies[1]), `"remove-control-file":"true"`)
}

func TestWrapTransportErrorMarksEOFAsTransportUnavailable(t *testing.T) {
	err := aria2.WrapTransportError(io.EOF)

	if !errors.Is(err, aria2.ErrTransportUnavailable) {
		t.Fatalf("expected ErrTransportUnavailable, got %v", err)
	}
}
