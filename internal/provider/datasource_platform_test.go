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

// newPlatformDataSourceMockServer simulates the subset of the SSPI appliance
// platform API (GET /sspi/platforms/{id}, GET /sspi/platforms/{id}/status)
// needed to drive the ssp_platform data source's Read without a live SSPI
// appliance.
func newPlatformDataSourceMockServer(t *testing.T) *httptest.Server {
	t.Helper()

	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/platforms/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"provider_id": "vsphere-provider-1",
			"compute": map[string]any{
				"cluster_id":             "cluster-1",
				"cluster_name":           "tf-unit-cluster",
				"content_datastore_id":   "datastore-1",
				"content_datastore_name": "tf-unit-datastore",
				"datacenter_id":          "datacenter-1",
			},
			"network": map[string]any{
				"dns":             []string{"10.0.0.1"},
				"network_configs": []any{},
				"ntp":             "ntp.corp.local",
				"search_domain":   "corp.local",
			},
			"service": map[string]any{
				"ingress_fqdn":  "ssp.corp.local",
				"instance_name": "tf-unit-platform",
				"kafka_fqdn":    "kafka.corp.local",
			},
			"system": map[string]any{},
		})
	})
	mux.HandleFunc("GET /sspi/platforms/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"phase":            "DEPLOYMENT",
			"workflow_results": []any{},
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitPlatformDataSourceConfig(host string) string {
	return testUnitSSPIProviderConfig(host) + `
data "sspi_platform" "test" {
  id = "platform-1"
}
`
}

// TestUnitPlatformDataSource exercises ssp_platform data source's Read
// against a mocked SSPI appliance API.
func TestUnitPlatformDataSource(t *testing.T) {
	srv := newPlatformDataSourceMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitPlatformDataSourceConfig(srv.URL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.sspi_platform.test", "id", "platform-1"),
					resource.TestCheckResourceAttrSet("data.sspi_platform.test", "domain"),
					resource.TestCheckResourceAttrSet("data.sspi_platform.test", "status"),
				),
			},
		},
	})
}
