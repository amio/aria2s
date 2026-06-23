package aria2_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
)

func TestListDownloadsFetchesActiveWaitingAndStoppedWindows(t *testing.T) {
	var requests []rpcCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeRPCCall(t, r)
		requests = append(requests, call)
		switch call.Method {
		case "aria2.tellActive":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[{"gid":"a1","status":"active","files":[{"path":"/tmp/a.iso"}],"completedLength":"25","totalLength":"100","downloadSpeed":"5"}]}`)
		case "aria2.tellWaiting":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[{"gid":"w1","status":"waiting","files":[{"path":"/tmp/w.iso"}],"completedLength":"0","totalLength":"200"}]}`)
		case "aria2.tellStopped":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[{"gid":"s1","status":"complete","files":[{"path":"/tmp/s.iso"}],"completedLength":"300","totalLength":"300"}]}`)
		default:
			t.Fatalf("unexpected method %s", call.Method)
		}
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "secret-token", server.Client())

	snapshot, err := client.ListDownloads(context.Background(), aria2.ListOptions{WaitingLimit: 10, StoppedOffset: 20, StoppedLimit: 30})
	if err != nil {
		t.Fatalf("list downloads: %v", err)
	}

	if len(snapshot.Active) != 1 || snapshot.Active[0].GID != "a1" || snapshot.Active[0].Name != "a.iso" {
		t.Fatalf("unexpected active downloads: %#v", snapshot.Active)
	}
	if len(snapshot.Waiting) != 1 || snapshot.Waiting[0].GID != "w1" || snapshot.Waiting[0].Status != "waiting" {
		t.Fatalf("unexpected waiting downloads: %#v", snapshot.Waiting)
	}
	if len(snapshot.Stopped) != 1 || snapshot.Stopped[0].GID != "s1" || snapshot.Stopped[0].CompletedLength != 300 {
		t.Fatalf("unexpected stopped downloads: %#v", snapshot.Stopped)
	}
	assertRPCRequest(t, requests[0], "aria2.tellActive", "token:secret-token")
	assertRPCRequest(t, requests[1], "aria2.tellWaiting", "token:secret-token", float64(0), float64(10))
	assertRPCRequest(t, requests[2], "aria2.tellStopped", "token:secret-token", float64(20), float64(30))
}

func TestListDownloadsFiltersCompletedMetadataFromStopped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeRPCCall(t, r)
		switch call.Method {
		case "aria2.tellActive":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[
				{"gid":"m1","status":"active","files":[{"path":"[METADATA]GIRLT.No.017.7z"}],"completedLength":"0","totalLength":"0","downloadSpeed":"0"},
				{"gid":"a1","status":"active","files":[{"path":"/tmp/movie.mkv"}],"bittorrent":{"info":{"name":"Movie"}},"completedLength":"100","totalLength":"200","downloadSpeed":"5"}
			]}`)
		case "aria2.tellWaiting":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[]}`)
		case "aria2.tellStopped":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[
				{"gid":"m2","status":"complete","files":[{"path":"[METADATA]The+New+York+Times"}],"completedLength":"20480","totalLength":"20480"},
				{"gid":"s1","status":"complete","files":[{"path":"/tmp/done.iso"}],"completedLength":"300","totalLength":"300"}
			]}`)
		default:
			t.Fatalf("unexpected method %s", call.Method)
		}
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "secret-token", server.Client())

	snapshot, err := client.ListDownloads(context.Background(), aria2.ListOptions{})
	if err != nil {
		t.Fatalf("list downloads: %v", err)
	}

	if len(snapshot.Active) != 2 {
		t.Fatalf("active count got %d, want 2", len(snapshot.Active))
	}
	if !snapshot.Active[0].IsMetadata {
		t.Fatalf("first active entry should be metadata: %#v", snapshot.Active[0])
	}
	if snapshot.Active[0].Name != "GIRLT.No.017.7z" {
		t.Fatalf("metadata name got %q, want GIRLT.No.017.7z", snapshot.Active[0].Name)
	}
	if snapshot.Active[1].Name != "Movie" {
		t.Fatalf("active name got %q, want Movie", snapshot.Active[1].Name)
	}

	if len(snapshot.Stopped) != 1 {
		t.Fatalf("stopped count got %d, want 1 (completed metadata should be filtered)", len(snapshot.Stopped))
	}
	if snapshot.Stopped[0].GID != "s1" {
		t.Fatalf("stopped entry got %s, want s1", snapshot.Stopped[0].GID)
	}
}

func TestListDownloadsDecodesMetadataDisplayName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeRPCCall(t, r)
		switch call.Method {
		case "aria2.tellActive":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[
				{"gid":"m1","status":"active","files":[{"path":"[METADATA]The+New+York+Times+Best+Sellers"}],"completedLength":"0","totalLength":"20480"}
			]}`)
		case "aria2.tellWaiting":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[]}`)
		case "aria2.tellStopped":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[]}`)
		default:
			t.Fatalf("unexpected method %s", call.Method)
		}
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "secret-token", server.Client())

	snapshot, err := client.ListDownloads(context.Background(), aria2.ListOptions{})
	if err != nil {
		t.Fatalf("list downloads: %v", err)
	}

	if len(snapshot.Active) != 1 {
		t.Fatalf("active count got %d, want 1", len(snapshot.Active))
	}
	if snapshot.Active[0].Name != "The New York Times Best Sellers" {
		t.Fatalf("metadata name got %q, want 'The New York Times Best Sellers'", snapshot.Active[0].Name)
	}
}

func TestTaskDetailParsesSelectedTaskPayload(t *testing.T) {
	var request rpcCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request = decodeRPCCall(t, r)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":{"gid":"a1","status":"active","dir":"/data/downloads","files":[{"path":"/tmp/movie.mkv","length":"1000","completedLength":"250","uris":[{"uri":"https://example.com/movie.mkv"}]}],"bittorrent":{"info":{"name":"Movie"}},"completedLength":"250","totalLength":"1000","downloadSpeed":"50","uploadSpeed":"10","connections":"3","errorCode":"0","errorMessage":""}}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "secret-token", server.Client())

	detail, err := client.TaskDetail(context.Background(), "a1")
	if err != nil {
		t.Fatalf("task detail: %v", err)
	}

	if detail.GID != "a1" || detail.Name != "Movie" || detail.PrimaryURI != "https://example.com/movie.mkv" {
		t.Fatalf("unexpected detail identity: %#v", detail)
	}
	if got := downloadDirField(t, detail); got != "/data/downloads" {
		t.Fatalf("download dir got %q, want /data/downloads", got)
	}
	if detail.CompletedLength != 250 || detail.TotalLength != 1000 || detail.DownloadSpeed != 50 || detail.UploadSpeed != 10 || detail.Connections != 3 {
		t.Fatalf("unexpected detail metrics: %#v", detail)
	}
	if len(detail.Files) != 1 || detail.Files[0].Name != "movie.mkv" || detail.Files[0].CompletedLength != 250 {
		t.Fatalf("unexpected detail files: %#v", detail.Files)
	}
	assertRPCRequest(t, request, "aria2.tellStatus", "token:secret-token", "a1")
	assertRequestIncludesField(t, request, "dir")
}

func TestSessionLifecycleRPCMethodsUseExpectedAria2Calls(t *testing.T) {
	var requests []rpcCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeRPCCall(t, r)
		requests = append(requests, call)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":"OK"}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "secret-token", server.Client())

	if err := client.SaveSession(context.Background()); err != nil {
		t.Fatalf("save session: %v", err)
	}
	if err := client.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("expected 2 RPC calls, got %d", len(requests))
	}
	assertRPCRequest(t, requests[0], "aria2.saveSession", "token:secret-token")
	assertRPCRequest(t, requests[1], "aria2.shutdown", "token:secret-token")
}

type rpcCall struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
}

func decodeRPCCall(t *testing.T, r *http.Request) rpcCall {
	t.Helper()
	if r.Method != http.MethodPost {
		t.Fatalf("method got %s, want POST", r.Method)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var call rpcCall
	if err := json.Unmarshal(body, &call); err != nil {
		t.Fatalf("decode body %s: %v", string(body), err)
	}
	return call
}

func assertRPCRequest(t *testing.T, call rpcCall, method string, params ...any) {
	t.Helper()
	if call.Method != method {
		t.Fatalf("method got %s, want %s", call.Method, method)
	}
	if len(call.Params) < len(params) {
		t.Fatalf("params got %#v, want prefix %#v", call.Params, params)
	}
	for index, want := range params {
		if call.Params[index] != want {
			t.Fatalf("param %d got %#v, want %#v in %#v", index, call.Params[index], want, call.Params)
		}
	}
}

func assertRequestIncludesField(t *testing.T, call rpcCall, field string) {
	t.Helper()
	if len(call.Params) < 3 {
		t.Fatalf("params got %#v, want detail field list", call.Params)
	}
	fields, ok := call.Params[2].([]any)
	if !ok {
		t.Fatalf("field params got %#v, want []any", call.Params[2])
	}
	for _, item := range fields {
		if item == field {
			return
		}
	}
	t.Fatalf("field %q missing from %#v", field, fields)
}

func downloadDirField(t *testing.T, detail aria2.DownloadDetail) string {
	t.Helper()
	field := reflect.ValueOf(detail).FieldByName("DownloadDir")
	if !field.IsValid() {
		t.Fatal("DownloadDetail is missing DownloadDir")
	}
	if field.Kind() != reflect.String {
		t.Fatalf("DownloadDetail.DownloadDir kind got %s, want string", field.Kind())
	}
	return field.String()
}
