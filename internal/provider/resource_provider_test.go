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
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// vsphereProviderMockAPI simulates the subset of the SSPI appliance vSphere
// provider API (POST/GET/PUT/DELETE /sspi/providers[/{id}]) needed to drive
// ssp_vsphere_provider through a full create/read/update/delete cycle
// without a live SSPI appliance.
type vsphereProviderMockAPI struct {
	mu     sync.Mutex
	exists bool
	body   map[string]any
}

func newVsphereProviderMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	m := &vsphereProviderMockAPI{}

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
	mux.HandleFunc("POST /sspi/providers", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.exists = true
		m.body = readBody(r)
		m.body["id"] = "provider-1"
		m.body["_revision"] = 0
		now := time.Now().UnixMilli()
		m.body["_create_time"] = now
		m.body["_last_modified_time"] = now
		writeJSON(w, http.StatusOK, m.body)
	})
	mux.HandleFunc("GET /sspi/providers/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, m.body)
	})
	mux.HandleFunc("PUT /sspi/providers/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		updated := readBody(r)
		updated["id"] = "provider-1"
		updated["_create_time"] = m.body["_create_time"]
		updated["_last_modified_time"] = time.Now().UnixMilli()
		if rev, ok := updated["_revision"].(float64); ok {
			updated["_revision"] = rev + 1
		} else {
			updated["_revision"] = 1
		}
		m.body = updated
		writeJSON(w, http.StatusAccepted, map[string]any{"id": "provider-1"})
	})
	mux.HandleFunc("GET /sspi/providers/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"provider_id":       "provider-1",
			"connection_status": map[string]any{"state": "CONNECTED", "message": "OK"},
			"workflow_results":  map[string]any{"state": "COMPLETED"},
		})
	})
	mux.HandleFunc("DELETE /sspi/providers/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.exists = false
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitVsphereProviderConfig(host, user string) string {
	return testUnitSSPIProviderConfig(host) + fmt.Sprintf(`
resource "sspi_vsphere_provider" "test" {
  server      = %q
  user        = %q
  password    = "VMware1!"
  certificate = "-----BEGIN CERTIFICATE-----\nMIIB...fake...\n-----END CERTIFICATE-----"
}
`, host, user)
}

// TestUnitVsphereProviderResource exercises ssp_vsphere_provider's Create,
// Read, and Update (user change, exercising the _revision-aware PUT and the
// post-update status poll) against a mocked SSPI appliance API.
func TestUnitVsphereProviderResource(t *testing.T) {
	srv := newVsphereProviderMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitVsphereProviderConfig(srv.URL, "administrator@vsphere.local"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_vsphere_provider.test", "id", "provider-1"),
					resource.TestCheckResourceAttr("sspi_vsphere_provider.test", "server", srv.URL),
					resource.TestCheckResourceAttr("sspi_vsphere_provider.test", "user", "administrator@vsphere.local"),
					resource.TestCheckResourceAttrSet("sspi_vsphere_provider.test", "certificate"),
				),
			},
			// Update: change user in place; requires the mock's PUT handler to
			// accept the _revision the resource fetched via GET beforehand.
			{
				Config: testUnitVsphereProviderConfig(srv.URL, "svc-account@vsphere.local"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_vsphere_provider.test", "id", "provider-1"),
					resource.TestCheckResourceAttr("sspi_vsphere_provider.test", "user", "svc-account@vsphere.local"),
				),
			},
			{
				Config:   testUnitVsphereProviderConfig(srv.URL, "svc-account@vsphere.local"),
				PlanOnly: true,
			},
		},
	})
}
