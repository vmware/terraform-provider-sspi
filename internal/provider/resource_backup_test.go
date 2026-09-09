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

// newBackupMockServer simulates POST /sspi/backup (trigger) and
// GET /sspi/backup/status/{id} (poll), resolving to a terminal SUCCESS status
// on the first poll.
func newBackupMockServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sspi/backup", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "backup-1", "status_url": "/sspi/backup/status/backup-1"})
	})
	mux.HandleFunc("GET /sspi/backup/status/backup-1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          "backup-1",
			"status":      "SUCCESS",
			"backup_type": "FULL_BACKUP",
			"progress":    100,
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitBackupConfig(host string) string {
	return testUnitSSPIProviderConfig(host) + `
resource "sspi_backup" "test" {
  backup_type = "FULL_BACKUP"
  name        = "tf-unit-test-backup"
}
`
}

// TestUnitBackupResource exercises sspi_backup's Create (trigger + poll to
// completion) and Read against a mocked SSPI appliance API.
func TestUnitBackupResource(t *testing.T) {
	srv := newBackupMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitBackupConfig(srv.URL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_backup.test", "id", "backup-1"),
					resource.TestCheckResourceAttr("sspi_backup.test", "backup_type", "FULL_BACKUP"),
					resource.TestCheckResourceAttr("sspi_backup.test", "action", "BACKUP"),
					resource.TestCheckResourceAttr("sspi_backup.test", "status", "SUCCESS"),
					resource.TestCheckResourceAttr("sspi_backup.test", "progress", "100"),
				),
			},
		},
	})
}
