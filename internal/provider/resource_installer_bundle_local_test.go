// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// bundleLocalMockAPI simulates the subset of the SSPI appliance Depot API
// (POST /sspi/bundles/local, GET/DELETE /sspi/bundles/{id}) needed to drive
// ssp_installer_bundle_local through a Create/Read/Delete cycle without a
// live SSPI appliance.
type bundleLocalMockAPI struct {
	mu     sync.Mutex
	exists bool
}

func newBundleLocalMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	m := &bundleLocalMockAPI{}

	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sspi/bundles/local", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil { //nolint:gosec // local httptest mock server, not internet-facing
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		m.mu.Lock()
		m.exists = true
		m.mu.Unlock()

		writeJSON(w, http.StatusAccepted, map[string]any{
			"id":         "bundle-1",
			"status_url": "/sspi/bundles/bundle-1",
		})
	})
	mux.HandleFunc("GET /sspi/bundles/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":             "bundle-1",
			"status":         "READY",
			"bundle_version": "1.0.0",
			"addons":         []any{},
			"packaged_time":  0,
		})
	})
	mux.HandleFunc("DELETE /sspi/bundles/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.exists = false
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitBundleLocalConfig(host, filePath string) string {
	return testUnitSSPIProviderConfig(host) + fmt.Sprintf(`
resource "sspi_installer_bundle_local" "test" {
  file_path = %q
}
`, filePath)
}

// TestUnitBundleLocalResource exercises ssp_installer_bundle_local's Create
// and Read against a mocked SSPI appliance Depot API.
func TestUnitBundleLocalResource(t *testing.T) {
	srv := newBundleLocalMockServer(t)

	bundleFile := filepath.Join(t.TempDir(), "bundle.tar")
	if err := os.WriteFile(bundleFile, []byte("dummy bundle contents"), 0o600); err != nil {
		t.Fatalf("failed to write dummy bundle file: %s", err)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitBundleLocalConfig(srv.URL, bundleFile),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("sspi_installer_bundle_local.test", "id"),
					resource.TestCheckResourceAttrSet("sspi_installer_bundle_local.test", "package_id"),
					resource.TestCheckResourceAttr("sspi_installer_bundle_local.test", "status", "READY"),
					resource.TestCheckResourceAttr("sspi_installer_bundle_local.test", "version", "1.0.0"),
				),
			},
		},
	})
}
