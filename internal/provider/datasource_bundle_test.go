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

// newBundleDataSourceMockServer simulates the subset of the SSPI Depot API
// (GET /sspi/bundles/{id}) needed to drive the ssp_bundle data source's Read
// without a live SSPI Depot.
func newBundleDataSourceMockServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/bundles/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":             "bundle-1",
			"status":         "READY",
			"bundle_version": "1.2.3",
			"packaged_time":  1700000000000,
			"addons":         []any{},
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitBundleDataSourceConfig(host string) string {
	return testUnitSSPIProviderConfig(host) + `
data "sspi_bundle" "test" {
  id = "bundle-1"
}
`
}

// TestUnitBundleDataSource exercises ssp_bundle data source's Read against a
// mocked SSPI Depot API.
func TestUnitBundleDataSource(t *testing.T) {
	srv := newBundleDataSourceMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitBundleDataSourceConfig(srv.URL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.sspi_bundle.test", "id", "bundle-1"),
					resource.TestCheckResourceAttr("data.sspi_bundle.test", "status", "READY"),
					resource.TestCheckResourceAttr("data.sspi_bundle.test", "version", "1.2.3"),
				),
			},
		},
	})
}
