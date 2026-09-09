// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// upgradeMockAPI simulates the subset of the SSPI appliance self-upgrade API
// (GET /sspi/upgrade/status, POST /sspi/upgrade) needed to drive
// sspi_upgrade through a full pre-checks-then-upgrade cycle without a live
// SSPI appliance. Every action immediately settles the mock into the
// terminal state a real appliance would eventually reach, so the resource's
// poll loop returns on its first iteration.
type upgradeMockAPI struct {
	mu              sync.Mutex
	overallStatus   string
	preChecksStatus string
	stepStatus      string
	currentVersion  string
	targetVersion   string
}

func newUpgradeMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	m := &upgradeMockAPI{
		overallStatus:   "NOT_STARTED",
		preChecksStatus: "NOT_STARTED",
		stepStatus:      "NOT_STARTED",
		currentVersion:  "5.1.0",
		targetVersion:   "5.2.0",
	}

	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/upgrade/status", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"overall_status":  m.overallStatus,
			"current_version": m.currentVersion,
			"target_version":  m.targetVersion,
			"pre_checks_status": map[string]any{
				"overall_status": m.preChecksStatus,
				"pre_checks":     []any{},
			},
			"upgrade_steps": []map[string]any{
				{"id": "step-1", "display_name": "Upgrade Appliance", "status": m.stepStatus},
			},
		})
	})
	mux.HandleFunc("POST /sspi/upgrade", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		switch r.URL.Query().Get("action") {
		case "PRECHECKS_ONLY":
			m.preChecksStatus = "SUCCESS"
		case "CONTINUE", "START", "RETRY":
			m.overallStatus = "SUCCESS"
			m.stepStatus = "SUCCESS"
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"id": "upgrade-op-1", "status_url": "/sspi/upgrade/status"})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitUpgradeConfig(host string) string {
	return testUnitSSPIProviderConfig(host) + `
resource "sspi_upgrade" "test" {
}
`
}

// TestUnitUpgradeResource exercises sspi_upgrade's default create path
// (run_prechecks defaults to true: PRECHECKS_ONLY then CONTINUE) against a
// mocked SSPI appliance API.
func TestUnitUpgradeResource(t *testing.T) {
	srv := newUpgradeMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitUpgradeConfig(srv.URL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_upgrade.test", "id", "upgrade"),
					resource.TestCheckResourceAttr("sspi_upgrade.test", "run_prechecks", "true"),
					resource.TestCheckResourceAttr("sspi_upgrade.test", "status", "SUCCESS"),
					resource.TestCheckResourceAttr("sspi_upgrade.test", "current_version", "5.1.0"),
					resource.TestCheckResourceAttr("sspi_upgrade.test", "target_version", "5.2.0"),
					resource.TestCheckResourceAttr("sspi_upgrade.test", "progress", "100"),
				),
			},
		},
	})
}
