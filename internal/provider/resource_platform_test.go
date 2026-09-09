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
)

// platformMockAPI simulates the subset of the SSPI appliance platform-LCM
// API (POST/GET/PUT/DELETE /sspi/platforms[/{id}], GET
// /sspi/platforms/{id}/status) needed to drive ssp_platform through a full
// deploy/read/teardown cycle without a live SSPI appliance.
//
// Create/Update bodies are stored and echoed back verbatim on GET (with "id"
// injected), so every Optional+Computed attribute the resource round-trips
// stays consistent between apply and the post-apply plan-convergence check.
type platformMockAPI struct {
	mu     sync.Mutex
	exists bool
	body   map[string]any
}

func newPlatformMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	m := &platformMockAPI{}

	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}

	readBody := func(r *http.Request) map[string]any {
		raw, _ := io.ReadAll(r.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sspi/platforms", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.exists = true
		m.body = readBody(r)
		m.body["_revision"] = 0
		writeJSON(w, http.StatusAccepted, map[string]any{"id": "platform-1"})
	})
	mux.HandleFunc("GET /sspi/platforms/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		resp := map[string]any{}
		for k, v := range m.body {
			resp[k] = v
		}
		resp["id"] = "platform-1"
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("PUT /sspi/platforms/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		updated := readBody(r)
		if rev, ok := updated["_revision"].(float64); ok {
			updated["_revision"] = rev + 1
		} else {
			updated["_revision"] = 1
		}
		m.body = updated
		writeJSON(w, http.StatusAccepted, map[string]any{"id": "platform-1"})
	})
	mux.HandleFunc("DELETE /sspi/platforms/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.exists = false
		writeJSON(w, http.StatusAccepted, map[string]any{"id": "platform-1"})
	})
	mux.HandleFunc("GET /sspi/platforms/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"phase": "DEPLOYMENT",
			"workflow_results": []map[string]any{
				{"display_name": "Deploy", "state": "COMPLETED"},
			},
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitPlatformConfig(host string) string {
	return testUnitPlatformConfigWithWorkerCount(host, 3)
}

func testUnitPlatformConfigWithWorkerCount(host string, workerCount int) string {
	return testUnitSSPIProviderConfig(host) + fmt.Sprintf(`
resource "sspi_platform" "test" {
  provider_id              = "vsphere-provider-1"
  display_name             = "tf-unit-test-platform"
  form_factor              = "COMPACT"
  ssp_type                 = "ATP"
  worker_count             = %d
  controller_count         = 3
  datacenter_id             = "datacenter-1"
  cluster_id                = "cluster-1"
  content_datastore_id      = "datastore-1"
  domain                    = "corp.local"
  dns_servers               = ["10.0.0.1"]
  ntp_server                = "ntp.corp.local"
  network_id                = "network-1"
  portgroup_id              = "portgroup-1"
  platform_default_gateway  = "10.0.0.1"
  platform_subnet           = "10.0.0.0/24"
  node_ip_pool              = ["10.0.0.10-10.0.0.20"]
  service_ip_pool           = ["10.0.0.30-10.0.0.40"]
  ingress_fqdn              = "ssp.corp.local"
  kafka_fqdn                = "kafka.corp.local"
  ssp_bundle_id             = "bundle-1"
}
`, workerCount)
}

// TestUnitPlatformResource exercises ssp_platform's Create, Read, Update
// (worker_count change, exercising the _revision-aware PUT), and Delete
// against a mocked SSPI appliance API.
func TestUnitPlatformResource(t *testing.T) {
	srv := newPlatformMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitPlatformConfig(srv.URL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_platform.test", "id", "platform-1"),
					resource.TestCheckResourceAttr("sspi_platform.test", "provider_id", "vsphere-provider-1"),
					resource.TestCheckResourceAttr("sspi_platform.test", "form_factor", "COMPACT"),
					resource.TestCheckResourceAttr("sspi_platform.test", "worker_count", "3"),
					resource.TestCheckResourceAttr("sspi_platform.test", "status", "DEPLOYMENT"),
				),
			},
			// Update: change worker_count in place; requires the mock's PUT
			// handler to accept the _revision the resource fetched via GET
			// beforehand.
			{
				Config: testUnitPlatformConfigWithWorkerCount(srv.URL, 5),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_platform.test", "id", "platform-1"),
					resource.TestCheckResourceAttr("sspi_platform.test", "worker_count", "5"),
				),
			},
		},
	})
}
