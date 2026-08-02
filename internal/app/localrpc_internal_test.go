package app

import (
	"net/http"
	"testing"
	"time"
)

func TestLocalRPCUsesDedicatedProxyFreeTransport(t *testing.T) {
	client := (&LocalRPC{}).httpClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	if transport == http.DefaultTransport {
		t.Fatal("local RPC shares the mutable default transport")
	}
	if transport.Proxy != nil {
		t.Fatal("loopback RPC must not inherit proxy configuration")
	}
	if client.Timeout != localRPCTransportTimeout || client.Timeout <= defaultRPCOperationTimeout {
		t.Fatalf("transport timeout = %s", client.Timeout)
	}
}

func TestDefaultRPCTimeBudgetsCoverBoundedStorageStalls(t *testing.T) {
	application := New(Options{})

	if application.options.RPCReadyTimeout != 30*time.Second ||
		application.options.RPCProbeTimeout != 30*time.Second ||
		application.options.DashboardReadTimeout != 30*time.Second ||
		application.options.DashboardMutationTimeout != 30*time.Second {
		t.Fatalf("operation timeouts = ready:%s probe:%s read:%s mutation:%s",
			application.options.RPCReadyTimeout,
			application.options.RPCProbeTimeout,
			application.options.DashboardReadTimeout,
			application.options.DashboardMutationTimeout,
		)
	}
	if application.options.RPCSlowThreshold != 2*time.Second {
		t.Fatalf("slow threshold = %s", application.options.RPCSlowThreshold)
	}
}
