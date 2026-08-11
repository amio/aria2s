package aria2_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
)

func TestReadBatchUsesAuthenticatedNestedMulticall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Method != "system.multicall" || len(request.Params) != 1 {
			t.Fatalf("unexpected outer request: %#v", request)
		}
		var calls []struct {
			Method string `json:"methodName"`
			Params []any  `json:"params"`
		}
		if err := json.Unmarshal(request.Params[0], &calls); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 5 {
			t.Fatalf("got %d nested calls", len(calls))
		}
		for _, call := range calls {
			if len(call.Params) == 0 || call.Params[0] != "token:secret" {
				t.Fatalf("missing nested token: %#v", call)
			}
		}
		for index := 0; index < 3; index++ {
			fields, ok := calls[index].Params[len(calls[index].Params)-1].([]any)
			if !ok {
				t.Fatalf("row fields = %#v", calls[index].Params)
			}
			for _, field := range fields {
				if field == "files" {
					t.Fatalf("row call %d requested complete file arrays", index)
				}
			}
		}
		if calls[2].Params[1] != float64(-21) {
			t.Fatalf("stopped offset = %#v, want newest-first page offset -21", calls[2].Params[1])
		}
		detailFields, ok := calls[3].Params[len(calls[3].Params)-1].([]any)
		if !ok || !containsJSONValue(detailFields, "files") {
			t.Fatalf("detail call lost files: %#v", calls[3].Params)
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[[[]],[[]],[[]],[{"gid":"a","status":"active","files":[]}],[[{"uri":"https://example.com/a"}]]]}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "secret", server.Client())
	read, err := client.ReadBatch(context.Background(), aria2.ReadBatchQuery{List: aria2.ListOptions{WaitingLimit: 10, StoppedOffset: 20, StoppedLimit: 10}, DetailGID: "a", ResolveDetailSource: true})
	if err != nil {
		t.Fatal(err)
	}
	if read.Detail == nil || read.Detail.GID != "a" || read.Detail.PrimaryURI != "https://example.com/a" {
		t.Fatalf("unexpected detail: %#v", read.Detail)
	}
}

func TestReadBatchKeepsDetailWhenNestedListCallFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[[[]],{"code":1,"message":"waiting failed"},[[]],[{"gid":"a","status":"active","files":[]}]]}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "", server.Client())
	read, err := client.ReadBatch(context.Background(), aria2.ReadBatchQuery{List: aria2.ListOptions{StoppedLimit: 10}, DetailGID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if read.ListErr == nil || read.Detail == nil {
		t.Fatalf("partial validity lost: %#v", read)
	}
	var rpcErr *aria2.RPCError
	if !errors.As(read.ListErr, &rpcErr) || rpcErr.Method != "aria2.tellWaiting" {
		t.Fatalf("nested context lost: %v", read.ListErr)
	}
}

func TestReadBatchResolvesEveryObservedGIDAndTreatsOnlyNotFoundAsAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[[[]],[[]],[[]],[{"gid":"0123456789abcdef","status":"paused","dir":"/stage/job","bittorrent":{"info":{"name":"job"}}}],{"code":1,"message":"GID not found"}]}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "", server.Client())
	read, err := client.ReadBatch(context.Background(), aria2.ReadBatchQuery{ObserveGIDs: []string{"0123456789abcdef", "fedcba9876543210"}})
	if err != nil || read.ListErr != nil {
		t.Fatalf("observed GID resolution failed: read=%#v err=%v", read, err)
	}
	if row := read.Observed["0123456789abcdef"]; row == nil || row.Dir != "/stage/job" {
		t.Fatalf("observed live row = %#v", row)
	}
	if row, ok := read.Observed["fedcba9876543210"]; !ok || row != nil {
		t.Fatalf("observed absence was not proven: row=%#v ok=%v", row, ok)
	}
}

func TestReadBatchHydratesOnlyRowsWithoutTorrentNames(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var request struct {
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		var calls []struct {
			Method string `json:"methodName"`
			Params []any  `json:"params"`
		}
		if err := json.Unmarshal(request.Params[0], &calls); err != nil {
			t.Fatal(err)
		}
		switch requestCount {
		case 1:
			if len(calls) != 3 {
				t.Fatalf("initial calls = %d", len(calls))
			}
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[[[{"gid":"http","status":"active"},{"gid":"torrent","status":"active","bittorrent":{"info":{"name":"Large Torrent"}}}]],[[]],[[{"gid":"metadata","status":"complete"}]]]}`)
		case 2:
			if len(calls) != 2 || calls[0].Method != "aria2.tellStatus" || calls[1].Method != "aria2.tellStatus" {
				t.Fatalf("identity calls = %#v", calls)
			}
			for _, call := range calls {
				fields, ok := call.Params[len(call.Params)-1].([]any)
				if !ok || len(fields) != 2 || !containsJSONValue(fields, "gid") || !containsJSONValue(fields, "files") {
					t.Fatalf("identity fields = %#v", call.Params)
				}
			}
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[[{"gid":"http","files":[{"path":"/tmp/asset.iso"}]}],[{"gid":"metadata","files":[{"path":"[METADATA]Example"}]}]]}`)
		default:
			t.Fatalf("unexpected request %d", requestCount)
		}
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "", server.Client())

	read, err := client.ReadBatch(context.Background(), aria2.ReadBatchQuery{})
	if err != nil || read.ListErr != nil {
		t.Fatalf("compact read failed: read=%#v err=%v", read, err)
	}
	if len(read.Downloads.Active) != 2 || read.Downloads.Active[0].Name != "asset.iso" || read.Downloads.Active[1].Name != "Large Torrent" {
		t.Fatalf("row names = %#v", read.Downloads.Active)
	}
	if len(read.Downloads.Stopped) != 0 {
		t.Fatalf("metadata result was not filtered: %#v", read.Downloads.Stopped)
	}
}

func TestReadBatchKeepsDetailWhenRowIdentityHydrationFails(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[[[{"gid":"http","status":"active"}]],[[]],[[]],[{"gid":"detail","status":"active","files":[]}]]}`)
			return
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[]}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "", server.Client())

	read, err := client.ReadBatch(context.Background(), aria2.ReadBatchQuery{DetailGID: "detail"})
	if err != nil || read.ListErr == nil || read.Detail == nil || read.Detail.GID != "detail" {
		t.Fatalf("partial detail validity lost: read=%#v err=%v", read, err)
	}
}

func containsJSONValue(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestMutationTransportFailureIsOutcomeUnknownAndKeepsCause(t *testing.T) {
	client := aria2.NewRPCClient("http://127.0.0.1:1", "", nil)
	_, err := client.AddURI(context.Background(), "https://example.com/a", aria2.AddOptions{})
	if !errors.Is(err, aria2.ErrOutcomeUnknown) || !errors.Is(err, aria2.ErrTransportUnavailable) {
		t.Fatalf("unexpected identities: %v", err)
	}
}

func TestReadBatchRejectsMalformedResultCountAndShape(t *testing.T) {
	responses := []string{
		`{"jsonrpc":"2.0","id":"1","result":[[[]]]}`,
		`{"jsonrpc":"2.0","id":"1","result":[[],[[]],[[]]]}`,
	}
	for _, response := range responses {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) }))
			defer server.Close()
			client := aria2.NewRPCClient(server.URL, "", server.Client())
			if _, err := client.ReadBatch(context.Background(), aria2.ReadBatchQuery{}); err == nil {
				t.Fatal("malformed whole read was accepted")
			}
		})
	}
}

func TestReadBatchJoinsAllNestedListFaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[{"code":1,"message":"active"},{"code":2,"message":"waiting"},[[]]]}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "", server.Client())
	read, err := client.ReadBatch(context.Background(), aria2.ReadBatchQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if read.ListErr == nil || !strings.Contains(read.ListErr.Error(), "tellActive") || !strings.Contains(read.ListErr.Error(), "tellWaiting") {
		t.Fatalf("nested faults not joined: %v", read.ListErr)
	}
}

func TestMutationConfirmedRPCErrorIsDeterministic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","error":{"code":-1,"message":"rejected"}}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "", server.Client())
	_, err := client.AddURI(context.Background(), "https://example.com/a", aria2.AddOptions{})
	var rpcErr *aria2.RPCError
	if !errors.As(err, &rpcErr) || errors.Is(err, aria2.ErrOutcomeUnknown) {
		t.Fatalf("confirmed rejection misclassified: %v", err)
	}
}

func TestMutationHTTP400JSONRPCErrorIsDeterministic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","error":{"code":1,"message":"GID e1 is not found"}}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "", server.Client())
	err := client.RemoveDownloadResult(context.Background(), "e1")
	var rpcErr *aria2.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Method != "aria2.removeDownloadResult" || !aria2.IsNotFound(err) {
		t.Fatalf("HTTP 400 JSON-RPC fault was not preserved: %v", err)
	}
	if errors.Is(err, aria2.ErrOutcomeUnknown) {
		t.Fatalf("confirmed not-found was classified as unknown: %v", err)
	}
}

func TestMutationMalformedResponseIsOutcomeUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `not-json`) }))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "", server.Client())
	_, err := client.AddURI(context.Background(), "https://example.com/a", aria2.AddOptions{})
	if !errors.Is(err, aria2.ErrOutcomeUnknown) {
		t.Fatalf("malformed mutation response was retryable: %v", err)
	}
}

func TestMutationMalformedHTTP400RemainsOutcomeUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `not-json`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "", server.Client())
	err := client.RemoveDownloadResult(context.Background(), "e1")
	if !errors.Is(err, aria2.ErrOutcomeUnknown) || !errors.Is(err, aria2.ErrTransportUnavailable) {
		t.Fatalf("unconfirmed HTTP failure lost unknown/transport identity: %v", err)
	}
	if !strings.Contains(err.Error(), "aria2.removeDownloadResult") {
		t.Fatalf("unknown mutation diagnostic lost method: %v", err)
	}
}
