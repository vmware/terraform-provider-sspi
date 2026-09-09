// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
)

// recurringBackupConfigMockAPI simulates the subset of the SSPI appliance
// recurring backup configuration API (PUT/GET /sspi/backup/recurring/config)
// needed to drive ssp_installer_recurring_backup_config through a
// create/read cycle without a live SSPI appliance. This resource is a
// singleton with no create/delete API, only get/put, so the mock only needs
// to store and echo back one config.
type recurringBackupConfigMockAPI struct {
	mu     sync.Mutex
	config api_client.RecurringBackupConfig
}

func newRecurringBackupConfigMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	m := &recurringBackupConfigMockAPI{
		config: api_client.RecurringBackupConfig{
			BackupType:         api_client.FULLBACKUP,
			BackupScheduleType: api_client.WEEKLY,
		},
	}

	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /sspi/backup/recurring/config", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body api_client.RecurringBackupConfig
		_ = json.Unmarshal(raw, &body)

		m.mu.Lock()
		rev := 1
		if m.config.UnderscoreRevision != nil {
			rev = *m.config.UnderscoreRevision + 1
		}
		body.UnderscoreRevision = &rev
		m.config = body
		cfg := m.config
		m.mu.Unlock()

		writeJSON(w, http.StatusOK, cfg)
	})
	mux.HandleFunc("GET /sspi/backup/recurring/config", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		cfg := m.config
		m.mu.Unlock()

		writeJSON(w, http.StatusOK, cfg)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// testUnitRecurringBackupConfigConfig renders the
// ssp_installer_recurring_backup_config HCL used by
// TestUnitRecurringBackupConfigResource. backup_type is pinned to the value
// the resource itself defaults to when unset, since the API response is
// always echoed back into state for this Optional (non-Computed) attribute.
func testUnitRecurringBackupConfigConfig(host string, enabled bool) string {
	return testUnitSSPIProviderConfig(host) + fmt.Sprintf(`
resource "sspi_installer_recurring_backup_config" "test" {
  enabled              = %t
  backup_type          = "FULL_BACKUP"
  backup_schedule_type = "WEEKLY"
  backup_schedule_weekly = {
    days_of_week = ["MONDAY"]
  }
}
`, enabled)
}

// TestUnitRecurringBackupConfigResource exercises
// ssp_installer_recurring_backup_config's Create, Read, and Update (enabled
// flip, exercising the _revision-aware PUT) against a mocked SSPI appliance
// API. This resource is a singleton (id is always "singleton") with no
// create/delete API, only get/put.
func TestUnitRecurringBackupConfigResource(t *testing.T) {
	srv := newRecurringBackupConfigMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitRecurringBackupConfigConfig(srv.URL, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_installer_recurring_backup_config.test", "id", "singleton"),
					resource.TestCheckResourceAttr("sspi_installer_recurring_backup_config.test", "enabled", "true"),
					resource.TestCheckResourceAttr("sspi_installer_recurring_backup_config.test", "backup_schedule_type", "WEEKLY"),
				),
			},
			// Update: flip enabled; requires the mock's PUT handler to accept
			// the _revision the resource fetched via GET beforehand.
			{
				Config: testUnitRecurringBackupConfigConfig(srv.URL, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_installer_recurring_backup_config.test", "enabled", "false"),
				),
			},
		},
	})
}
