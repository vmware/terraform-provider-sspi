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

// newUpgradePackagesDataSourceMockServer simulates the subset of the SSPI
// Upgrade API (GET /sspi/upgrade/packages) needed to drive the
// sspi_upgrade_packages data source's Read without a live SSPI appliance.
func newUpgradePackagesDataSourceMockServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/upgrade/packages", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{
				map[string]any{
					"package_name": "upgrade-bundle-5.2.0.0.0.29219600.sub",
					"package_size": "4.94G",
					"version":      "5.2.0.0.0.29219600",
				},
			},
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestUnitUpgradePackagesDataSource exercises sspi_upgrade_packages' Read
// against a mocked SSPI Upgrade API.
func TestUnitUpgradePackagesDataSource(t *testing.T) {
	srv := newUpgradePackagesDataSourceMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitSSPIProviderConfig(srv.URL) + `
data "sspi_upgrade_packages" "test" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.sspi_upgrade_packages.test", "packages.#", "1"),
					resource.TestCheckResourceAttr("data.sspi_upgrade_packages.test", "packages.0.package_name", "upgrade-bundle-5.2.0.0.0.29219600.sub"),
					resource.TestCheckResourceAttr("data.sspi_upgrade_packages.test", "packages.0.package_size", "4.94G"),
					resource.TestCheckResourceAttr("data.sspi_upgrade_packages.test", "packages.0.version", "5.2.0.0.0.29219600"),
				),
			},
		},
	})
}
