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

// newUpgradeManagerMockServer simulates GET /sspi/upgrade/packages,
// POST /sspi/upgrade/manager and GET /sspi/upgrade/manager/status; the POST
// settles the mock straight into SUCCESS.
func newUpgradeManagerMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	overall, stepStatus, gotVersion := "NOT_STARTED", "NOT_STARTED", ""

	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/upgrade/packages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"results": []map[string]any{
				{"package_name": "upgrade-bundle-5.2.1.sub", "package_size": "4G", "version": "5.2.1"},
			},
		})
	})
	mux.HandleFunc("POST /sspi/upgrade/manager", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body struct {
			TargetVersion string `json:"target_version"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotVersion = body.TargetVersion
		if r.URL.Query().Get("action") != "START" || gotVersion == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "bad request"})
			return
		}
		overall, stepStatus = "SUCCESS", "SUCCESS"
		writeJSON(w, http.StatusAccepted, map[string]any{"id": "op-1", "status_url": "/sspi/upgrade/manager/status"})
	})
	mux.HandleFunc("GET /sspi/upgrade/manager/status", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		pct := 0
		if overall == "SUCCESS" {
			pct = 100
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"overall_status":      overall,
			"progress_percentage": pct,
			"upgrade_steps": []map[string]any{
				{"id": "s1", "display_name": "Upgrade Manager", "status": stepStatus},
			},
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestUnitUpgradeManagerResource verifies target_version defaults to the single
// staged package's version and the create runs to SUCCESS.
func TestUnitUpgradeManagerResource(t *testing.T) {
	srv := newUpgradeManagerMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitSSPIProviderConfig(srv.URL) + `
resource "sspi_upgrade_manager" "test" {
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_upgrade_manager.test", "id", "upgrade-manager"),
					resource.TestCheckResourceAttr("sspi_upgrade_manager.test", "target_version", "5.2.1"),
					resource.TestCheckResourceAttr("sspi_upgrade_manager.test", "status", "SUCCESS"),
					resource.TestCheckResourceAttr("sspi_upgrade_manager.test", "progress", "100"),
				),
			},
		},
	})
}
