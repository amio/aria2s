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

func TestDashboardSnapshotUsesAuthenticatedNestedMulticall(t *testing.T) {
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
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[[[]],[[]],[[]],[{"gid":"a","status":"active","files":[]}],[[{"uri":"https://example.com/a"}]]]}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "secret", server.Client())
	read, err := client.DashboardSnapshot(context.Background(), aria2.DashboardQuery{List: aria2.ListQuery{WaitingLimit: 10, StoppedLimit: 10}, DetailGID: "a", ResolveDetailSource: true})
	if err != nil {
		t.Fatal(err)
	}
	if read.Detail == nil || read.Detail.GID != "a" || read.Detail.PrimaryURI != "https://example.com/a" {
		t.Fatalf("unexpected detail: %#v", read.Detail)
	}
}

func TestDashboardSnapshotKeepsDetailWhenNestedListCallFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[[[]],{"code":1,"message":"waiting failed"},[[]],[{"gid":"a","status":"active","files":[]}]]}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "", server.Client())
	read, err := client.DashboardSnapshot(context.Background(), aria2.DashboardQuery{List: aria2.ListQuery{StoppedLimit: 10}, DetailGID: "a"})
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

func TestDashboardSnapshotResolvesEveryManagedGIDAndTreatsOnlyNotFoundAsAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[[[]],[[]],[[]],[{"gid":"0123456789abcdef","status":"paused","dir":"/stage/job","files":[]}],{"code":1,"message":"GID not found"}]}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "", server.Client())
	read, err := client.DashboardSnapshot(context.Background(), aria2.DashboardQuery{ManagedGIDs: []string{"0123456789abcdef", "fedcba9876543210"}})
	if err != nil || read.ListErr != nil {
		t.Fatalf("managed resolution failed: read=%#v err=%v", read, err)
	}
	if row := read.Managed["0123456789abcdef"]; row == nil || row.Dir != "/stage/job" {
		t.Fatalf("managed live row = %#v", row)
	}
	if row, ok := read.Managed["fedcba9876543210"]; !ok || row != nil {
		t.Fatalf("managed absence was not proven: row=%#v ok=%v", row, ok)
	}
}

func TestMutationTransportFailureIsOutcomeUnknownAndKeepsCause(t *testing.T) {
	client := aria2.NewRPCClient("http://127.0.0.1:1", "", nil)
	_, err := client.AddURI(context.Background(), "https://example.com/a", aria2.AddOptions{})
	if !errors.Is(err, aria2.ErrOutcomeUnknown) || !errors.Is(err, aria2.ErrTransportUnavailable) {
		t.Fatalf("unexpected identities: %v", err)
	}
}

func TestDashboardSnapshotRejectsMalformedResultCountAndShape(t *testing.T) {
	responses := []string{
		`{"jsonrpc":"2.0","id":"1","result":[[[]]]}`,
		`{"jsonrpc":"2.0","id":"1","result":[[],[[]],[[]]]}`,
	}
	for _, response := range responses {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) }))
			defer server.Close()
			client := aria2.NewRPCClient(server.URL, "", server.Client())
			if _, err := client.DashboardSnapshot(context.Background(), aria2.DashboardQuery{}); err == nil {
				t.Fatal("malformed whole read was accepted")
			}
		})
	}
}

func TestDashboardSnapshotJoinsAllNestedListFaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":[{"code":1,"message":"active"},{"code":2,"message":"waiting"},[[]]]}`)
	}))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "", server.Client())
	read, err := client.DashboardSnapshot(context.Background(), aria2.DashboardQuery{})
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

func TestMutationMalformedResponseIsOutcomeUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `not-json`) }))
	defer server.Close()
	client := aria2.NewRPCClient(server.URL, "", server.Client())
	_, err := client.AddURI(context.Background(), "https://example.com/a", aria2.AddOptions{})
	if !errors.Is(err, aria2.ErrOutcomeUnknown) {
		t.Fatalf("malformed mutation response was retryable: %v", err)
	}
}
