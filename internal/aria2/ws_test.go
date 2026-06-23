package aria2_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/amio/aria2s/internal/aria2"
)

func TestWSClientReceivesNotifications(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Logf("accept: %v", err)
			return
		}
		defer conn.CloseNow()

		events := []struct {
			method string
			gid    string
		}{
			{"aria2.onDownloadStart", "gid-1"},
			{"aria2.onDownloadComplete", "gid-1"},
			{"aria2.onDownloadStart", "gid-2"},
		}

		for _, ev := range events {
			payload, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"method":  ev.method,
				"params":  []map[string]string{{"gid": ev.gid}},
			})
			if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/jsonrpc"
	client, err := aria2.NewWSClient(wsURL)
	if err != nil {
		t.Fatalf("NewWSClient: %v", err)
	}
	client.Connect(ctx)

	var notifications []aria2.Notification
	timeout := time.After(2 * time.Second)
	for i := 0; i < 3; i++ {
		select {
		case notif, ok := <-client.Events():
			if !ok {
				t.Fatal("events channel closed unexpectedly")
			}
			notifications = append(notifications, notif)
		case <-timeout:
			t.Fatalf("timed out waiting for notification %d (got %d)", i+1, len(notifications))
		}
	}

	if len(notifications) != 3 {
		t.Fatalf("got %d notifications, want 3", len(notifications))
	}
	if notifications[0].Method != "aria2.onDownloadStart" || notifications[0].GID != "gid-1" {
		t.Fatalf("notification[0] = %+v", notifications[0])
	}
	if notifications[1].Method != "aria2.onDownloadComplete" || notifications[1].GID != "gid-1" {
		t.Fatalf("notification[1] = %+v", notifications[1])
	}
	if notifications[2].Method != "aria2.onDownloadStart" || notifications[2].GID != "gid-2" {
		t.Fatalf("notification[2] = %+v", notifications[2])
	}
}

func TestWSClientReconnectsAfterDrop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	connectCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectCount++
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}

		if connectCount == 1 {
			payload, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"method":  "aria2.onDownloadStart",
				"params":  []map[string]string{{"gid": "first"}},
			})
			conn.Write(ctx, websocket.MessageText, payload)
			time.Sleep(50 * time.Millisecond)
			conn.Close(websocket.StatusNormalClosure, "")
			return
		}
		payload, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"method":  "aria2.onDownloadComplete",
			"params":  []map[string]string{{"gid": "second"}},
		})
		conn.Write(ctx, websocket.MessageText, payload)
		<-ctx.Done()
	}))

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/jsonrpc"
	client, err := aria2.NewWSClient(wsURL)
	if err != nil {
		t.Fatalf("NewWSClient: %v", err)
	}
	client.Connect(ctx)

	var notifications []aria2.Notification
	timeout := time.After(5 * time.Second)
	for len(notifications) < 2 {
		select {
		case notif, ok := <-client.Events():
			if !ok {
				t.Fatal("events channel closed unexpectedly")
			}
			notifications = append(notifications, notif)
		case <-timeout:
			t.Fatalf("timed out after %d notifications (connectCount=%d)", len(notifications), connectCount)
		}
	}

	if connectCount < 2 {
		t.Fatalf("expected at least 2 connections, got %d", connectCount)
	}
	if notifications[0].GID != "first" {
		t.Fatalf("first notification GID = %s, want first", notifications[0].GID)
	}
	if notifications[1].GID != "second" {
		t.Fatalf("second notification GID = %s, want second", notifications[1].GID)
	}
}

func TestWSClientResetsBackoffAfterSuccessfulSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	connectCount := 0
	reconnectAfter := make(chan time.Time, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectCount++
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}

		switch connectCount {
		case 1, 2:
			// Abrupt close with no messages — builds exponential backoff (1s, then 2s).
			conn.CloseNow()
		case 3:
			payload, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"method":  "aria2.onDownloadStart",
				"params":  []map[string]string{{"gid": "first"}},
			})
			conn.Write(ctx, websocket.MessageText, payload)
			time.Sleep(20 * time.Millisecond)
			conn.Close(websocket.StatusNormalClosure, "")
			reconnectAfter <- time.Now()
		default:
			payload, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"method":  "aria2.onDownloadComplete",
				"params":  []map[string]string{{"gid": "second"}},
			})
			conn.Write(ctx, websocket.MessageText, payload)
			<-ctx.Done()
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/jsonrpc"
	client, err := aria2.NewWSClient(wsURL)
	if err != nil {
		t.Fatalf("NewWSClient: %v", err)
	}
	client.Connect(ctx)

	var notifications []aria2.Notification
	timeout := time.After(8 * time.Second)
	for len(notifications) < 2 {
		select {
		case notif, ok := <-client.Events():
			if !ok {
				t.Fatal("events channel closed unexpectedly")
			}
			notifications = append(notifications, notif)
		case <-timeout:
			t.Fatalf("timed out after %d notifications (connectCount=%d)", len(notifications), connectCount)
		}
	}

	select {
	case closedAt := <-reconnectAfter:
		// After a clean close, reconnect should be immediate (not the accumulated 4s backoff).
		if time.Since(closedAt) > 500*time.Millisecond {
			t.Fatalf("second reconnect took too long after clean close (%v)", time.Since(closedAt))
		}
	default:
		t.Fatal("server never reported clean close")
	}

	if notifications[0].GID != "first" || notifications[1].GID != "second" {
		t.Fatalf("notifications = %+v", notifications)
	}
}

func TestWSClientClosesCleanly(t *testing.T) {
	ctx := context.Background()

	connClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		<-connClosed
		conn.CloseNow()
	}))
	defer func() {
		close(connClosed)
		server.Close()
	}()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/jsonrpc"
	client, err := aria2.NewWSClient(wsURL)
	if err != nil {
		t.Fatalf("NewWSClient: %v", err)
	}
	client.Connect(ctx)

	time.Sleep(100 * time.Millisecond)
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case _, ok := <-client.Events():
		if ok {
			t.Fatal("events channel should be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for events channel to close")
	}
}

func TestNewWSClientFromHTTPEndpoint(t *testing.T) {
	client, err := aria2.NewWSClient("http://127.0.0.1:6800/jsonrpc")
	if err != nil {
		t.Fatalf("NewWSClient: %v", err)
	}
	if client == nil {
		t.Fatal("client is nil")
	}
	client.Close()
}

func TestNewWSClientRejectsInvalidURL(t *testing.T) {
	_, err := aria2.NewWSClient("://bad-url")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}
