// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// newBundleRemoteMockServer simulates the subset of the SSPI appliance Depot
// API (POST /sspi/bundles/remote, GET/DELETE /sspi/bundles/{id}) needed to
// drive sspi_installer_bundle_remote through Create/Read/Import/Delete. The
// bundle reports finalStatus once imported.
func newBundleRemoteMockServer(t *testing.T, finalStatus string) *httptest.Server {
	t.Helper()

	var (
		mu     sync.Mutex
		exists bool
	)

	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sspi/bundles/remote", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
			http.Error(w, "missing url", http.StatusBadRequest)
			return
		}

		mu.Lock()
		exists = true
		mu.Unlock()

		// "id" is a job-tracking ID, deliberately different from the bundle ID
		// carried by status_url, which is what the resource must use.
		writeJSON(w, http.StatusAccepted, map[string]any{
			"id":         "job-1",
			"status_url": "/sspi/bundles/bundle-1",
		})
	})
	mux.HandleFunc("GET /sspi/bundles/{id}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !exists || r.PathValue("id") != "bundle-1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":             "bundle-1",
			"status":         finalStatus,
			"bundle_version": "2.0.0",
			"addons":         []any{},
			"packaged_time":  0,
		})
	})
	mux.HandleFunc("DELETE /sspi/bundles/{id}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		exists = false
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitBundleRemoteConfig(host, bundleURL string) string {
	return testUnitSSPIProviderConfig(host) + fmt.Sprintf(`
resource "sspi_installer_bundle_remote" "test" {
  url = %q
}
`, bundleURL)
}

// TestUnitBundleRemoteResource exercises sspi_installer_bundle_remote's
// Create, Read, ImportState and Delete against a mocked SSPI Depot API.
func TestUnitBundleRemoteResource(t *testing.T) {
	srv := newBundleRemoteMockServer(t, "READY")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitBundleRemoteConfig(srv.URL, "https://example.com/bundle.tar"),
				Check: resource.ComposeAggregateTestCheckFunc(
					// ID must come from status_url, not the job "id" in the 202 body.
					resource.TestCheckResourceAttr("sspi_installer_bundle_remote.test", "id", "bundle-1"),
					resource.TestCheckResourceAttr("sspi_installer_bundle_remote.test", "package_id", "bundle-1"),
					resource.TestCheckResourceAttr("sspi_installer_bundle_remote.test", "status", "READY"),
					resource.TestCheckResourceAttr("sspi_installer_bundle_remote.test", "version", "2.0.0"),
				),
			},
			{
				ResourceName:      "sspi_installer_bundle_remote.test",
				ImportState:       true,
				ImportStateVerify: true,
				// url is not returned by the API, so it can't be recovered on import.
				ImportStateVerifyIgnore: []string{"url"},
			},
		},
	})
}

// TestUnitBundleRemoteResource_ImportFailed verifies Create surfaces an error
// when the appliance reports the import ended in a failure status.
func TestUnitBundleRemoteResource_ImportFailed(t *testing.T) {
	srv := newBundleRemoteMockServer(t, "FAILED")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testUnitBundleRemoteConfig(srv.URL, "https://example.com/bad.tar"),
				ExpectError: regexp.MustCompile(`ended in status FAILED`),
			},
		},
	})
}
