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

func TestLifecycleStatusIncludesNativeDisplayName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeRPCCall(t, r)
		assertRPCRequest(t, call, "aria2.tellStatus", "token:secret-token", "a1")
		assertRequestIncludesField(t, call, "bittorrent")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":{"gid":"a1","status":"active","dir":"/tmp","bittorrent":{"info":{"name":"Readable Release"}},"files":[{"path":"/tmp/internal-name"}]}}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "secret-token", server.Client())

	status, err := client.LifecycleStatus(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if status.Name != "Readable Release" {
		t.Fatalf("lifecycle display name = %q", status.Name)
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

func TestRetryPrimitivesReadSourceAndAddBeforeCleanup(t *testing.T) {
	var requests []rpcCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeRPCCall(t, r)
		requests = append(requests, call)
		switch call.Method {
		case "aria2.tellStatus":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":{"gid":"e1","status":"error","dir":"/data/downloads","files":[{"path":"/tmp/failed.iso","length":"1000","completedLength":"250","uris":[{"uri":"https://example.com/failed.iso"}]}],"completedLength":"250","totalLength":"1000","downloadSpeed":"0","uploadSpeed":"0","connections":"0","errorCode":"18","errorMessage":"disk error"}}`)
		case "aria2.getUris":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[{"uri":"https://example.com/failed.iso","status":"used"}]}`)
		case "aria2.removeDownloadResult":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":"OK"}`)
		case "aria2.addUri":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":"e2"}`)
		default:
			t.Fatalf("unexpected method %s", call.Method)
		}
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "secret-token", server.Client())

	source, err := client.RetrySource(context.Background(), "e1")
	if err != nil {
		t.Fatalf("retry source: %v", err)
	}
	newGID, err := client.AddURIs(context.Background(), source.URIs, aria2.AddOptions{Dir: source.Dir})
	if err != nil {
		t.Fatalf("add replacement: %v", err)
	}
	if err := client.RemoveDownloadResult(context.Background(), "e1"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if newGID != "e2" {
		t.Fatalf("new gid got %q, want e2", newGID)
	}
	if len(requests) != 4 {
		t.Fatalf("expected 4 RPC calls, got %d: %#v", len(requests), requests)
	}
	assertRPCRequest(t, requests[0], "aria2.tellStatus", "token:secret-token", "e1")
	assertRPCRequest(t, requests[1], "aria2.getUris", "token:secret-token", "e1")
	assertRPCRequest(t, requests[2], "aria2.addUri", "token:secret-token")
	assertRPCRequest(t, requests[3], "aria2.removeDownloadResult", "token:secret-token", "e1")
	if len(requests[2].Params) < 3 {
		t.Fatalf("addUri params got %#v, want uri list and options", requests[2].Params)
	}
	uris, ok := requests[2].Params[1].([]any)
	if !ok || len(uris) != 1 || uris[0] != "https://example.com/failed.iso" {
		t.Fatalf("addUri uris got %#v", requests[2].Params[1])
	}
	opts, ok := requests[2].Params[2].(map[string]any)
	if !ok || opts["dir"] != "/data/downloads" {
		t.Fatalf("addUri options got %#v", requests[2].Params[2])
	}
}

func TestRetrySourceBuildsMagnetFromInfoHashWhenURIsMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeRPCCall(t, r)
		switch call.Method {
		case "aria2.tellStatus":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":{"gid":"e1","status":"error","dir":"/data/downloads","files":[{"path":"/tmp/torrent","length":"0","completedLength":"0","uris":[]}],"bittorrent":{"info":{"name":"Movie"}},"infoHash":"ABCDEF0123456789ABCDEF0123456789ABCDEF","completedLength":"0","totalLength":"0","downloadSpeed":"0","uploadSpeed":"0","connections":"0","errorCode":"1","errorMessage":"timeout"}}`)
		case "aria2.getUris":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[]}`)
		default:
			t.Fatalf("unexpected method %s", call.Method)
		}
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "secret-token", server.Client())

	source, err := client.RetrySource(context.Background(), "e1")
	if err != nil {
		t.Fatalf("retry source: %v", err)
	}
	if len(source.URIs) != 1 {
		t.Fatalf("retry source got %#v, want one URI", source)
	}
	if source.URIs[0] != "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("magnet uri got %q", source.URIs[0])
	}
}

func TestSaveSessionUsesExpectedAria2Call(t *testing.T) {
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

	if len(requests) != 1 {
		t.Fatalf("expected 1 RPC call, got %d", len(requests))
	}
	assertRPCRequest(t, requests[0], "aria2.saveSession", "token:secret-token")
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
	if len(call.Params) == 0 {
		t.Fatalf("params got %#v, want field list", call.Params)
	}
	fieldParam := call.Params[len(call.Params)-1]
	fields, ok := fieldParam.([]any)
	if !ok {
		t.Fatalf("field params got %#v, want []any", fieldParam)
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

func TestDiagnosticCensusReadsEveryPageAndNativeErrorEvidence(t *testing.T) {
	var waitingOffsets, stoppedOffsets []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeRPCCall(t, r)
		if call.Params[0] != "token:secret-token" {
			t.Errorf("census request missing authentication: %+v", call)
		}
		rows := []map[string]string{}
		switch call.Method {
		case "aria2.tellActive":
			rows = append(rows, map[string]string{"gid": "active", "status": "active"})
		case "aria2.tellWaiting", "aria2.tellStopped":
			offset := int(call.Params[1].(float64))
			if call.Method == "aria2.tellWaiting" {
				waitingOffsets = append(waitingOffsets, offset)
			} else {
				stoppedOffsets = append(stoppedOffsets, offset)
			}
			for i := offset; i < offset+100 && i < 205; i++ {
				rows = append(rows, map[string]string{"gid": fmt.Sprintf("%s-%d", call.Method, i), "status": "error", "errorCode": "3", "errorMessage": "resource not found"})
			}
		default:
			t.Errorf("unexpected method %s", call.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "1", "result": rows})
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "secret-token", server.Client())
	tasks, err := client.CompleteCensus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 411 || !reflect.DeepEqual(waitingOffsets, []int{0, 100, 200}) || !reflect.DeepEqual(stoppedOffsets, []int{0, 100, 200}) {
		t.Fatalf("incomplete census: %d tasks; waiting=%v stopped=%v", len(tasks), waitingOffsets, stoppedOffsets)
	}
	if task := tasks[len(tasks)-1]; task.ErrorCode != "3" || task.ErrorMessage != "resource not found" {
		t.Fatalf("lost native failure evidence: %+v", task)
	}
}
