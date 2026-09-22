package aria2_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestAddOptionsShareRPCAndSessionEncoding(t *testing.T) {
	on, off := true, false
	for _, test := range []struct {
		name string
		opts aria2.AddOptions
		want map[string]string
	}{
		{name: "omitted", want: map[string]string{}},
		{name: "metadata", opts: aria2.AddOptions{Dir: "/data", Pause: &off, MetadataOnly: &on, SaveMetadata: &on},
			want: map[string]string{"dir": "/data", "pause": "false", "bt-metadata-only": "true", "bt-save-metadata": "true"}},
		{name: "seed", opts: aria2.AddOptions{SeedUnverified: &on, CheckIntegrity: &off, ForceSave: &off, RemoveControlFile: &on},
			want: map[string]string{"bt-seed-unverified": "true", "check-integrity": "false", "force-save": "false", "remove-control-file": "true"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got map[string]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Params []json.RawMessage `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if err := json.Unmarshal(request.Params[len(request.Params)-1], &got); err != nil {
					t.Error(err)
					return
				}
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":"0123456789abcdef"}`)
			}))
			defer server.Close()
			client := aria2.NewRPCClient(server.URL, "secret", server.Client())
			if _, err := client.AddTorrent(context.Background(), []byte("metainfo"), test.opts); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("RPC options = %v, want %v", got, test.want)
			}

			block := aria2.SessionBlock{URI: "/torrent", Options: []aria2.SessionOption{{Key: "split", Value: "3"}}}
			block.ApplyOptions(test.opts)
			encoded, err := aria2.EncodeSession([]aria2.SessionBlock{block})
			if err != nil {
				t.Fatal(err)
			}
			decoded, problems := aria2.ParseSession(encoded)
			if len(problems) != 0 || len(decoded) != 1 {
				t.Fatalf("session decode: %v, %v", decoded, problems)
			}
			for key, want := range test.want {
				if value, ok := decoded[0].Option(key); !ok || value != want {
					t.Errorf("session %s = %q, want %q", key, value, want)
				}
			}
			if len(decoded[0].Options) != len(test.want)+1 {
				t.Fatalf("session options = %v", decoded[0].Options)
			}
			if value, _ := decoded[0].Option("split"); value != "3" {
				t.Fatal("unrelated saved option changed")
			}
			fresh := aria2.SessionBlock{URI: "/torrent", Options: []aria2.SessionOption{{Key: "split", Value: "3"}}}
			fresh.ApplyOptions(test.opts)
			again, err := aria2.EncodeSession([]aria2.SessionBlock{fresh})
			if err != nil || string(again) != string(encoded) {
				t.Fatalf("option application changed encoding: %q, %v", again, err)
			}
		})
	}
}

func TestWrapTransportErrorMarksEOFAsTransportUnavailable(t *testing.T) {
	err := aria2.WrapTransportError(io.EOF)

	if !errors.Is(err, aria2.ErrTransportUnavailable) {
		t.Fatalf("expected ErrTransportUnavailable, got %v", err)
	}
}
