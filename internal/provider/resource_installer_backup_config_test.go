// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
)

// backupConfigMockAPI simulates the subset of the SSPI appliance backup
// configuration API (PUT/GET /sspi/backup/config) needed to drive
// ssp_installer_backup_config through a create/read cycle without a live
// SSPI appliance. This resource is a singleton with no create/delete API,
// only get/put, so the mock only needs to store and echo back one config.
type backupConfigMockAPI struct {
	mu     sync.Mutex
	config api_client.BackupConfig
}

func newBackupConfigMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	m := &backupConfigMockAPI{
		config: api_client.BackupConfig{
			Protocol: api_client.SFTP,
			Port:     22,
		},
	}

	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /sspi/backup/config", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body api_client.BackupConfig
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
	mux.HandleFunc("GET /sspi/backup/config", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		cfg := m.config
		m.mu.Unlock()

		writeJSON(w, http.StatusOK, cfg)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// testUnitBackupConfigConfig renders the ssp_installer_backup_config HCL
// used by TestUnitBackupConfigResource. protocol and port are pinned to the
// values the resource itself defaults to when unset, so the post-apply
// refresh (which always echoes the API's protocol/port back into state)
// doesn't disagree with the config for these Optional (non-Computed)
// attributes.
func testUnitBackupConfigConfig(host, serverAddress string) string {
	return testUnitSSPIProviderConfig(host) + fmt.Sprintf(`
resource "sspi_installer_backup_config" "test" {
  server_address  = %q
  protocol        = "SFTP"
  port            = 22
  username        = "backupuser"
  password        = "backuppass"
  backup_location = "/backups"
  passphrase      = "s3cr3t-passphrase"
}
`, serverAddress)
}

// TestUnitBackupConfigResource exercises ssp_installer_backup_config's
// Create, Read, and Update (server_address change, exercising the
// _revision-aware PUT) against a mocked SSPI appliance API. This resource is
// a singleton (id is always "singleton") with no create/delete API, only
// get/put.
func TestUnitBackupConfigResource(t *testing.T) {
	srv := newBackupConfigMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitBackupConfigConfig(srv.URL, "sftp.corp.local"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_installer_backup_config.test", "id", "singleton"),
					resource.TestCheckResourceAttr("sspi_installer_backup_config.test", "server_address", "sftp.corp.local"),
					resource.TestCheckResourceAttr("sspi_installer_backup_config.test", "username", "backupuser"),
					resource.TestCheckResourceAttr("sspi_installer_backup_config.test", "backup_location", "/backups"),
				),
			},
			// Update: change server_address; requires the mock's PUT handler
			// to accept the _revision the resource fetched via GET beforehand.
			{
				Config: testUnitBackupConfigConfig(srv.URL, "sftp2.corp.local"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_installer_backup_config.test", "server_address", "sftp2.corp.local"),
				),
			},
		},
	})
}

// TestUnitBackupConfigResource_TrailingSlashRejected verifies the
// noTrailingSlash() validator catches a backup_location ending in "/" at
// plan/validate time with a clear error, instead of letting it through to
// fail apply with a confusing "Provider produced inconsistent result after
// apply" (the API strips a trailing slash server-side, and backup_location
// is a Required, non-Computed attribute whose post-apply state must exactly
// equal its planned value).
func TestUnitBackupConfigResource_TrailingSlashRejected(t *testing.T) {
	srv := newBackupConfigMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitSSPIProviderConfig(srv.URL) + `
resource "sspi_installer_backup_config" "test" {
  server_address  = "sftp.corp.local"
  protocol        = "SFTP"
  port            = 22
  username        = "backupuser"
  password        = "backuppass"
  backup_location = "/backups/"
  passphrase      = "s3cr3t-passphrase"
}
`,
				ExpectError: regexp.MustCompile(`Trailing Slash Not Allowed`),
			},
		},
	})
}
