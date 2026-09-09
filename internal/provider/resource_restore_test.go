// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// newRestoreMockServer simulates POST /sspi/restore (trigger) and
// GET /sspi/restore/status/{id} (poll), resolving to a terminal SUCCESS
// status on the first poll.
func newRestoreMockServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sspi/restore", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "restore-1", "status_url": "/sspi/restore/status/restore-1"})
	})
	mux.HandleFunc("GET /sspi/restore/status/restore-1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":        "restore-1",
			"backup_id": "backup-1",
			"status":    "SUCCESS",
			"progress":  100,
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitRestoreConfig(host string) string {
	return testUnitSSPIProviderConfig(host) + `
resource "sspi_restore" "test" {
  backup_id = "backup-1"
}
`
}

// TestUnitRestoreResource exercises sspi_restore's Create (trigger + poll to
// completion) and Read against a mocked SSPI appliance API.
func TestUnitRestoreResource(t *testing.T) {
	srv := newRestoreMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitRestoreConfig(srv.URL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_restore.test", "id", "restore-1"),
					resource.TestCheckResourceAttr("sspi_restore.test", "backup_id", "backup-1"),
					resource.TestCheckResourceAttr("sspi_restore.test", "action", "RESTORE"),
					resource.TestCheckResourceAttr("sspi_restore.test", "force_restore", "false"),
					resource.TestCheckResourceAttr("sspi_restore.test", "status", "SUCCESS"),
					resource.TestCheckResourceAttr("sspi_restore.test", "progress", "100"),
				),
			},
		},
	})
}
