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

// newVsphereProviderDataSourceMockServer simulates the subset of the SSPI
// appliance API (GET /sspi/providers/{id}) needed to drive the
// ssp_vsphere_provider data source's Read without a live SSPI appliance.
func newVsphereProviderDataSourceMockServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/providers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":                  "vsphere-provider-1",
			"server":              "vcenter.corp.local",
			"user":                "administrator@vsphere.local",
			"certificate":         "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----",
			"_create_time":        int64(1704067200000),
			"_last_modified_time": int64(1704153600000),
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitVsphereProviderDataSourceConfig(host string) string {
	return testUnitSSPIProviderConfig(host) + `
data "sspi_vsphere_provider" "test" {
  id = "vsphere-provider-1"
}
`
}

// TestUnitVsphereProviderDataSource exercises ssp_vsphere_provider data
// source's Read against a mocked SSPI appliance API.
func TestUnitVsphereProviderDataSource(t *testing.T) {
	srv := newVsphereProviderDataSourceMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitVsphereProviderDataSourceConfig(srv.URL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.sspi_vsphere_provider.test", "id", "vsphere-provider-1"),
					resource.TestCheckResourceAttr("data.sspi_vsphere_provider.test", "server", "vcenter.corp.local"),
					resource.TestCheckResourceAttr("data.sspi_vsphere_provider.test", "user", "administrator@vsphere.local"),
					resource.TestCheckResourceAttr("data.sspi_vsphere_provider.test", "created_at", "2024-01-01T00:00:00Z"),
					resource.TestCheckResourceAttr("data.sspi_vsphere_provider.test", "updated_at", "2024-01-02T00:00:00Z"),
				),
			},
		},
	})
}
