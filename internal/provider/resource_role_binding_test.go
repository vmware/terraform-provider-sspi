// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// roleBindingMockAPI simulates POST/GET/PUT/DELETE /sspi/iam/role-bindings[/{id}]
// needed to drive sspi_role_binding through a full lifecycle without a live
// SSPI appliance.
type roleBindingMockAPI struct {
	mu       sync.Mutex
	exists   bool
	revision int
	body     map[string]any
}

func newRoleBindingMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	m := &roleBindingMockAPI{}

	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sspi/iam/role-bindings", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.exists = true
		m.revision = 1
		body["id"] = "binding-1"
		body["_revision"] = m.revision
		m.body = body
		writeJSON(w, http.StatusOK, m.body)
	})
	mux.HandleFunc("GET /sspi/iam/role-bindings/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, m.body)
	})
	mux.HandleFunc("PUT /sspi/iam/role-bindings/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.revision++
		body["id"] = "binding-1"
		body["_revision"] = m.revision
		m.body = body
		writeJSON(w, http.StatusOK, m.body)
	})
	mux.HandleFunc("DELETE /sspi/iam/role-bindings/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.exists = false
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitRoleBindingConfig(host string, roles string) string {
	return testUnitSSPIProviderConfig(host) + `
resource "sspi_role_binding" "test" {
  entity_name = "sspuser1@corp.example.com"
  user_type   = "REMOTE_USER"
  roles       = ` + roles + `
}
`
}

// TestUnitRoleBindingResource exercises sspi_role_binding's Create, Read,
// and Update (full role-list replacement) against a mocked SSPI appliance
// API.
func TestUnitRoleBindingResource(t *testing.T) {
	srv := newRoleBindingMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitRoleBindingConfig(srv.URL, `["AUDITOR"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_role_binding.test", "id", "binding-1"),
					resource.TestCheckResourceAttr("sspi_role_binding.test", "entity_name", "sspuser1@corp.example.com"),
					resource.TestCheckResourceAttr("sspi_role_binding.test", "user_type", "REMOTE_USER"),
					resource.TestCheckResourceAttr("sspi_role_binding.test", "roles.#", "1"),
					resource.TestCheckResourceAttr("sspi_role_binding.test", "roles.0", "AUDITOR"),
				),
			},
			// Update: full replacement of roles.
			{
				Config: testUnitRoleBindingConfig(srv.URL, `["AUDITOR", "SUPPORT_BUNDLE_COLLECTOR"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_role_binding.test", "roles.#", "2"),
				),
			},
			// Delete Testing is handled automatically by terraform-plugin-testing
			// at the end of Test.
		},
	})
}
