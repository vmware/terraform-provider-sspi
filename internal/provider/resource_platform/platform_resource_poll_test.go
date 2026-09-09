// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
)

// withFastPlatformPolling shrinks platformPollInterval/platformPollTimeout for
// the duration of a test, restoring the real 15s/90min production values on
// cleanup. This is what makes multi-iteration polling loops in waitForWorkflow
// (and the Delete teardown poller) testable at all without a real
// 15-second-per-iteration wait.
func withFastPlatformPolling(t *testing.T, timeout time.Duration) {
	t.Helper()
	origInterval, origTimeout := platformPollInterval, platformPollTimeout
	platformPollInterval = 10 * time.Millisecond
	platformPollTimeout = timeout
	t.Cleanup(func() {
		platformPollInterval, platformPollTimeout = origInterval, origTimeout
	})
}

// TestWaitForWorkflow_MultiIteration verifies waitForWorkflow's poll loop
// actually iterates: the mock reports a RUNNING workflow for the first two
// calls and only resolves to a terminal COMPLETED state on the third.
func TestWaitForWorkflow_MultiIteration(t *testing.T) {
	withFastPlatformPolling(t, time.Second)

	var calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/platforms/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		state := "RUNNING"
		if n >= 3 {
			state = "COMPLETED"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"phase": "DEPLOYMENT",
			"workflow_results": []map[string]any{
				{"display_name": "Deploy", "state": state},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := api_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create api client: %s", err)
	}
	r := &PlatformResource{client: client}

	result, err := r.waitForWorkflow(context.Background(), "platform-1")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if result == nil || result.State == nil || *result.State != api_client.WorkflowResultStateCOMPLETED {
		t.Fatalf("expected a COMPLETED workflow result, got %+v", result)
	}
	if got := atomic.LoadInt32(&calls); got < 3 {
		t.Fatalf("expected at least 3 poll calls (loop must iterate), got %d", got)
	}
}

// TestWaitForWorkflow_Failure verifies a terminal FAILED workflow state is
// returned (not swallowed as success) so the caller's checkWorkflowResult can
// surface it as an error.
func TestWaitForWorkflow_Failure(t *testing.T) {
	withFastPlatformPolling(t, time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/platforms/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"phase": "DEPLOYMENT",
			"workflow_results": []map[string]any{
				{"display_name": "Deploy", "state": "FAILED"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := api_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create api client: %s", err)
	}
	r := &PlatformResource{client: client}

	result, err := r.waitForWorkflow(context.Background(), "platform-1")
	if err != nil {
		t.Fatalf("waitForWorkflow itself should not error on a terminal FAILED state: %s", err)
	}
	if err := checkWorkflowResult(result); err == nil {
		t.Fatal("expected checkWorkflowResult to report an error for a FAILED workflow, got nil")
	}
}

// TestWaitForWorkflow_Timeout verifies the deadline logic: if the workflow
// never reaches a terminal state, the poller returns a timeout error rather
// than blocking forever.
func TestWaitForWorkflow_Timeout(t *testing.T) {
	withFastPlatformPolling(t, 50*time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/platforms/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"phase": "DEPLOYMENT",
			"workflow_results": []map[string]any{
				{"display_name": "Deploy", "state": "RUNNING"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := api_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create api client: %s", err)
	}
	r := &PlatformResource{client: client}

	_, err = r.waitForWorkflow(context.Background(), "platform-1")
	if err == nil {
		t.Fatal("expected a timeout error when the workflow never reaches a terminal state, got nil")
	}
}
