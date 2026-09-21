// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
)

// withFastProviderPolling shrinks providerPollInterval/providerPollTimeout for the
// duration of a test, restoring the real 15s/90min production values on cleanup.
func withFastProviderPolling(t *testing.T, timeout time.Duration) {
	t.Helper()
	origInterval, origTimeout := providerPollInterval, providerPollTimeout
	providerPollInterval = 10 * time.Millisecond
	providerPollTimeout = timeout
	t.Cleanup(func() {
		providerPollInterval, providerPollTimeout = origInterval, origTimeout
	})
}

// TestWaitForProviderStatus_ConnectedWithNotStartedWorkflow verifies that a
// NOT_STARTED workflow (e.g. an update that finished so fast there's no residual
// workflow left to observe) is treated as "nothing in flight" and falls through to
// the authoritative ConnectionStatus check, rather than polling for the full
// providerPollTimeout because NOT_STARTED matched neither the active nor the
// terminal state lists.
func TestWaitForProviderStatus_ConnectedWithNotStartedWorkflow(t *testing.T) {
	withFastProviderPolling(t, time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/providers/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"provider_id": "provider-1",
			"connection_status": map[string]any{
				"state":   "CONNECTED",
				"message": "Connected to the vCenter",
			},
			"workflow_results": map[string]any{
				"display_name": "vCenter Update Workflow",
				"state":        "NOT_STARTED",
				"jobs":         []any{},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := api_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create api client: %s", err)
	}
	r := &ProviderResource{client: client}

	if err := r.waitForProviderStatus(context.Background(), "provider-1"); err != nil {
		t.Fatalf("expected success once ConnectionStatus is CONNECTED, got: %s", err)
	}
}

// TestWaitForProviderStatus_MultiIteration verifies the poll loop actually iterates
// while a workflow is genuinely active (RUNNING), only returning once it settles.
func TestWaitForProviderStatus_MultiIteration(t *testing.T) {
	withFastProviderPolling(t, time.Second)

	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/providers/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		calls++
		state := "RUNNING"
		if calls >= 3 {
			state = "COMPLETED"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"provider_id":       "provider-1",
			"connection_status": map[string]any{"state": "CONNECTED", "message": "Connected to the vCenter"},
			"workflow_results":  map[string]any{"display_name": "vCenter Update Workflow", "state": state},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := api_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create api client: %s", err)
	}
	r := &ProviderResource{client: client}

	if err := r.waitForProviderStatus(context.Background(), "provider-1"); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if calls < 3 {
		t.Fatalf("expected at least 3 poll calls (loop must iterate while RUNNING), got %d", calls)
	}
}

// TestWaitForProviderStatus_NotConnected verifies a settled (non-in-flight)
// workflow with a NOT_CONNECTED connection status is reported as an error.
func TestWaitForProviderStatus_NotConnected(t *testing.T) {
	withFastProviderPolling(t, time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/providers/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"provider_id":       "provider-1",
			"connection_status": map[string]any{"state": "NOT_CONNECTED", "message": "Cannot reach vCenter"},
			"workflow_results":  map[string]any{"display_name": "vCenter Update Workflow", "state": "COMPLETED"},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := api_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create api client: %s", err)
	}
	r := &ProviderResource{client: client}

	if err := r.waitForProviderStatus(context.Background(), "provider-1"); err == nil {
		t.Fatal("expected an error for a NOT_CONNECTED provider, got nil")
	}
}
