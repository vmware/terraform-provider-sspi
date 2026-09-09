// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package resource_upgrade

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vmware/terraform-provider-sspi/internal/client/upgrade_client"
)

// withFastUpgradePolling shrinks upgradePollInterval/upgradePollTimeout for
// the duration of a test, restoring the real 15s/90min production values on
// cleanup. This is what makes multi-iteration polling loops in
// waitForOverallStatus/waitForPrechecksComplete testable at all without a
// real 15-second-per-iteration wait.
func withFastUpgradePolling(t *testing.T, timeout time.Duration) {
	t.Helper()
	origInterval, origTimeout := upgradePollInterval, upgradePollTimeout
	upgradePollInterval = 10 * time.Millisecond
	upgradePollTimeout = timeout
	t.Cleanup(func() {
		upgradePollInterval, upgradePollTimeout = origInterval, origTimeout
	})
}

// TestWaitForOverallStatus_MultiIteration verifies waitForOverallStatus's
// poll loop actually iterates: the mock reports IN_PROGRESS for the first
// two calls and only resolves to a terminal SUCCESS on the third.
func TestWaitForOverallStatus_MultiIteration(t *testing.T) {
	withFastUpgradePolling(t, time.Second)

	var calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/upgrade/status", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		status := "IN_PROGRESS"
		if n >= 3 {
			status = "SUCCESS"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"overall_status":  status,
			"current_version": "5.1.0",
			"target_version":  "5.2.0",
			"upgrade_steps":   []any{},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := upgrade_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create upgrade client: %s", err)
	}
	r := &UpgradeResource{client: client}

	status, err := r.waitForOverallStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if derefStatus(status.OverallStatus) != string(upgrade_client.SUCCESS) {
		t.Fatalf("expected SUCCESS, got %s", derefStatus(status.OverallStatus))
	}
	if got := atomic.LoadInt32(&calls); got < 3 {
		t.Fatalf("expected at least 3 poll calls (loop must iterate), got %d", got)
	}
}

// TestWaitForPrechecksComplete_MultiIteration verifies the pre-checks poll
// loop iterates on pre_checks_status.overall_status independently of the
// top-level overall_status.
func TestWaitForPrechecksComplete_MultiIteration(t *testing.T) {
	withFastUpgradePolling(t, time.Second)

	var calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/upgrade/status", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		preChecksStatus := "IN_PROGRESS"
		if n >= 3 {
			preChecksStatus = "SUCCESS"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"overall_status":  "NOT_STARTED",
			"current_version": "5.1.0",
			"target_version":  "5.2.0",
			"pre_checks_status": map[string]any{
				"overall_status": preChecksStatus,
				"pre_checks":     []any{},
			},
			"upgrade_steps": []any{},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := upgrade_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create upgrade client: %s", err)
	}
	r := &UpgradeResource{client: client}

	status, err := r.waitForPrechecksComplete(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if status.PreChecksStatus == nil || derefStatus(status.PreChecksStatus.OverallStatus) != string(upgrade_client.SUCCESS) {
		t.Fatalf("expected pre_checks_status.overall_status SUCCESS, got %+v", status.PreChecksStatus)
	}
	if got := atomic.LoadInt32(&calls); got < 3 {
		t.Fatalf("expected at least 3 poll calls (loop must iterate), got %d", got)
	}
}

// TestWaitForOverallStatus_Timeout verifies the deadline logic: if the
// upgrade never reaches a terminal state, the poller returns a timeout error
// rather than blocking forever.
func TestWaitForOverallStatus_Timeout(t *testing.T) {
	withFastUpgradePolling(t, 50*time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/upgrade/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"overall_status":  "IN_PROGRESS",
			"current_version": "5.1.0",
			"target_version":  "5.2.0",
			"upgrade_steps":   []any{},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := upgrade_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create upgrade client: %s", err)
	}
	r := &UpgradeResource{client: client}

	_, err = r.waitForOverallStatus(context.Background())
	if err == nil {
		t.Fatal("expected a timeout error when the upgrade never reaches a terminal state, got nil")
	}
}

// TestTriggerUpgrade_RetryAfterFailed verifies the FAILED-status branch:
// triggerUpgrade should issue action=RETRY (not PRECHECKS_ONLY/START) when
// the current overall_status is already FAILED, regardless of run_prechecks.
func TestTriggerUpgrade_RetryAfterFailed(t *testing.T) {
	withFastUpgradePolling(t, time.Second)

	var gotAction string
	status := "FAILED"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/upgrade/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"overall_status":  status,
			"current_version": "5.1.0",
			"target_version":  "5.2.0",
			"upgrade_steps":   []any{},
		})
	})
	mux.HandleFunc("POST /sspi/upgrade", func(w http.ResponseWriter, r *http.Request) {
		gotAction = r.URL.Query().Get("action")
		status = "SUCCESS"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "op-1"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := upgrade_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create upgrade client: %s", err)
	}
	r := &UpgradeResource{client: client}

	if _, err := r.triggerUpgrade(context.Background(), true); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if gotAction != "RETRY" {
		t.Fatalf("expected action=RETRY for a FAILED prior attempt, got %q", gotAction)
	}
}

// TestTriggerUpgrade_PrechecksFailedAbortsBeforeContinue verifies that when
// pre-checks fail, triggerUpgrade returns an error and never issues
// action=CONTINUE.
func TestTriggerUpgrade_PrechecksFailedAbortsBeforeContinue(t *testing.T) {
	withFastUpgradePolling(t, time.Second)

	var actionsPosted []string
	precheckStatus := "IN_PROGRESS"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/upgrade/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"overall_status":  "NOT_STARTED",
			"current_version": "5.1.0",
			"target_version":  "5.2.0",
			"pre_checks_status": map[string]any{
				"overall_status": precheckStatus,
				"pre_checks":     []any{},
			},
			"upgrade_steps": []any{},
		})
	})
	mux.HandleFunc("POST /sspi/upgrade", func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("action")
		actionsPosted = append(actionsPosted, action)
		if action == "PRECHECKS_ONLY" {
			precheckStatus = "FAILED"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "op-1"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := upgrade_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create upgrade client: %s", err)
	}
	r := &UpgradeResource{client: client}

	if _, err := r.triggerUpgrade(context.Background(), true); err == nil {
		t.Fatal("expected an error when pre-checks fail, got nil")
	}
	for _, a := range actionsPosted {
		if a == "CONTINUE" {
			t.Fatalf("action=CONTINUE must not be issued after a failed pre-check, actions posted: %v", actionsPosted)
		}
	}
}

// TestTriggerUpgrade_DirectStartWithoutPrechecks verifies run_prechecks=false
// issues action=START directly, without a PRECHECKS_ONLY/CONTINUE pair.
func TestTriggerUpgrade_DirectStartWithoutPrechecks(t *testing.T) {
	withFastUpgradePolling(t, time.Second)

	var actionsPosted []string
	status := "NOT_STARTED"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/upgrade/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"overall_status":  status,
			"current_version": "5.1.0",
			"target_version":  "5.2.0",
			"upgrade_steps":   []any{},
		})
	})
	mux.HandleFunc("POST /sspi/upgrade", func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("action")
		actionsPosted = append(actionsPosted, action)
		status = "SUCCESS"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "op-1"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := upgrade_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create upgrade client: %s", err)
	}
	r := &UpgradeResource{client: client}

	if _, err := r.triggerUpgrade(context.Background(), false); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if len(actionsPosted) != 1 || actionsPosted[0] != "START" {
		t.Fatalf("expected exactly one action=START, got %v", actionsPosted)
	}
}
