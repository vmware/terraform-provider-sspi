// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestWaitForWorkflow_JobFailureWithoutTerminalState verifies that a failed
// job (e.g. a precheck's "Check vCenter") is caught even when the appliance
// leaves the overall WorkflowResult.State stuck at RUNNING - which is what
// the real SSPI appliance does for a precheck that's awaiting a manual
// "rerun precheck" rather than auto-transitioning to a terminal FAILED
// state. Without this check waitForWorkflow would poll for the full
// platformPollTimeout despite the failure already being visible per-job.
func TestWaitForWorkflow_JobFailureWithoutTerminalState(t *testing.T) {
	withFastPlatformPolling(t, time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/platforms/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"phase": "PRECHECK",
			"workflow_results": []map[string]any{
				{
					"display_name": "Pre Check Workflow",
					"state":        "RUNNING",
					"jobs": []map[string]any{
						{"display_name": "Check SSPI basic infra", "state": "COMPLETED"},
						{"display_name": "Check vCenter", "state": "FAILED", "details": "Insufficient per-host memory"},
						{"display_name": "Check compatibility", "state": "NOT_STARTED"},
					},
				},
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
		t.Fatalf("waitForWorkflow itself should not error when a job fails: %s", err)
	}
	if result == nil {
		t.Fatal("expected a non-nil result once a job fails")
	}
	if err := checkWorkflowResult(result); err == nil {
		t.Fatal("expected checkWorkflowResult to report an error for a failed job, got nil")
	} else if !strings.Contains(err.Error(), "Check vCenter") || !strings.Contains(err.Error(), "Insufficient per-host memory") {
		t.Fatalf("expected error to name the failed job and its details, got: %s", err)
	}
}

// TestWaitForWorkflow_FailureBehindNotStartedPlaceholder verifies a FAILED
// precheck is caught even when it's NOT the last entry in workflow_results:
// the real SSPI appliance appends a placeholder "Deployment Workflow" entry
// at NOT_STARTED as soon as a platform is created, even if the precheck that
// must precede it has already failed. Checking only the last entry (as
// waitForWorkflow used to) would see NOT_STARTED - not terminal, no failed
// jobs - and poll for the full platformPollTimeout despite the real failure
// sitting in the first entry.
func TestWaitForWorkflow_FailureBehindNotStartedPlaceholder(t *testing.T) {
	withFastPlatformPolling(t, time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/platforms/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"phase": "PRECHECK",
			"workflow_results": []map[string]any{
				{
					"display_name": "Pre Check Workflow",
					"state":        "FAILED",
					"jobs": []map[string]any{
						{"display_name": "Check network configuration", "state": "FAILED", "details": "FQDN mismatch"},
					},
				},
				{
					"display_name": "Deployment Workflow",
					"state":        "NOT_STARTED",
					"jobs": []map[string]any{
						{"display_name": "vCenter Configuration", "state": "NOT_STARTED"},
					},
				},
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
		t.Fatal("expected checkWorkflowResult to report an error for the failed precheck, got nil")
	} else if !strings.Contains(err.Error(), "Check network configuration") {
		t.Fatalf("expected error to name the failed precheck job, got: %s", err)
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
