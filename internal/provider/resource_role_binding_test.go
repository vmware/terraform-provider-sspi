// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// roleBindingMockAPI simulates /sspi/iam/role-bindings[/{id}]. PUT enforces the
// appliance's optimistic locking (428 without _revision, 412 when stale) so the
// resource's GET-then-PUT update path is exercised for real.
type roleBindingMockAPI struct {
	mu       sync.Mutex
	bindings map[string]map[string]any
	nextID   int
}

func newRoleBindingMockServer(t *testing.T, seed ...map[string]any) *httptest.Server {
	t.Helper()
	m := &roleBindingMockAPI{bindings: map[string]map[string]any{}}
	for _, b := range seed {
		id := b["id"].(string)
		b["_revision"] = float64(0)
		m.bindings[id] = b
	}

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
	mux.HandleFunc("GET /sspi/iam/role-bindings", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		filter := r.URL.Query().Get("user_name")
		results := []map[string]any{}
		for _, b := range m.bindings {
			if filter == "" || strings.Contains(b["entity_name"].(string), filter) {
				results = append(results, b)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"number_of_results":  len(results),
			"total_result_count": len(results),
			"results":            results,
		})
	})
	mux.HandleFunc("POST /sspi/iam/role-bindings", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.nextID++
		body := readBody(r)
		id := "binding-" + string(rune('0'+m.nextID))
		body["id"] = id
		body["_revision"] = float64(0)
		m.bindings[id] = body
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("GET /sspi/iam/role-bindings/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		b, ok := m.bindings[r.PathValue("id")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, b)
	})
	mux.HandleFunc("PUT /sspi/iam/role-bindings/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		id := r.PathValue("id")
		cur, ok := m.bindings[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body := readBody(r)
		rev, has := body["_revision"].(float64)
		if !has {
			w.WriteHeader(http.StatusPreconditionRequired)
			return
		}
		if rev != cur["_revision"].(float64) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		body["id"] = id
		body["_revision"] = rev + 1
		m.bindings[id] = body
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("DELETE /sspi/iam/role-bindings/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.bindings, r.PathValue("id"))
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitRoleBindingConfig(host, roles string) string {
	return testUnitSSPIProviderConfig(host) + `
resource "sspi_role_binding" "test" {
  entity_name = "Administrators"
  user_type   = "REMOTE_GROUP"
  roles       = ` + roles + `
}
`
}

// TestUnitRoleBindingResource covers Create, Read, Update (roles replaced via
// the _revision-aware PUT) and import of sspi_role_binding.
func TestUnitRoleBindingResource(t *testing.T) {
	srv := newRoleBindingMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitRoleBindingConfig(srv.URL, `["ENTERPRISE_ADMIN"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("sspi_role_binding.test", "id"),
					resource.TestCheckResourceAttr("sspi_role_binding.test", "entity_name", "Administrators"),
					resource.TestCheckResourceAttr("sspi_role_binding.test", "user_type", "REMOTE_GROUP"),
					resource.TestCheckResourceAttr("sspi_role_binding.test", "roles.#", "1"),
					resource.TestCheckTypeSetElemAttr("sspi_role_binding.test", "roles.*", "ENTERPRISE_ADMIN"),
				),
			},
			{
				Config: testUnitRoleBindingConfig(srv.URL, `["ENTERPRISE_ADMIN", "AUDITOR"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_role_binding.test", "roles.#", "2"),
					resource.TestCheckTypeSetElemAttr("sspi_role_binding.test", "roles.*", "AUDITOR"),
				),
			},
			{
				ResourceName:      "sspi_role_binding.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestUnitRoleBindingResource_InvalidRole confirms an unknown role name is
// rejected at plan time instead of reaching the API.
func TestUnitRoleBindingResource_InvalidRole(t *testing.T) {
	srv := newRoleBindingMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testUnitRoleBindingConfig(srv.URL, `["NOT_A_ROLE"]`),
				ExpectError: regexp.MustCompile(`NOT_A_ROLE`),
			},
		},
	})
}

func testUnitRoleBindingDataSourceConfig(host, lookup string) string {
	return testUnitSSPIProviderConfig(host) + `
data "sspi_role_binding" "test" {
  ` + lookup + `
}
`
}

func roleBindingSeeds() []map[string]any {
	return []map[string]any{
		{
			"id": "rb-admins", "entity_name": "Administrators", "user_type": "REMOTE_GROUP",
			"roles": []map[string]any{{"role": "ENTERPRISE_ADMIN", "display_name": "Enterprise Admin"}},
		},
		{
			"id": "rb-admin-user", "entity_name": "Administrators", "user_type": "REMOTE_USER",
			"roles": []map[string]any{{"role": "AUDITOR"}},
		},
		{
			"id": "rb-local", "entity_name": "admin", "user_type": "LOCAL_USER",
			"roles": []map[string]any{{"role": "ENTERPRISE_ADMIN"}},
		},
	}
}

// TestUnitRoleBindingDataSource covers lookup by id, by entity_name +
// user_type, and the error cases (ambiguous name, no match).
func TestUnitRoleBindingDataSource(t *testing.T) {
	srv := newRoleBindingMockServer(t, roleBindingSeeds()...)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitRoleBindingDataSourceConfig(srv.URL, `id = "rb-admins"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.sspi_role_binding.test", "entity_name", "Administrators"),
					resource.TestCheckResourceAttr("data.sspi_role_binding.test", "user_type", "REMOTE_GROUP"),
					resource.TestCheckTypeSetElemAttr("data.sspi_role_binding.test", "roles.*", "ENTERPRISE_ADMIN"),
				),
			},
			{
				Config: testUnitRoleBindingDataSourceConfig(srv.URL, "entity_name = \"Administrators\"\n  user_type   = \"REMOTE_USER\""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.sspi_role_binding.test", "id", "rb-admin-user"),
					resource.TestCheckTypeSetElemAttr("data.sspi_role_binding.test", "roles.*", "AUDITOR"),
				),
			},
			{
				Config: testUnitRoleBindingDataSourceConfig(srv.URL, `entity_name = "admin"`),
				Check:  resource.TestCheckResourceAttr("data.sspi_role_binding.test", "id", "rb-local"),
			},
			{
				Config:      testUnitRoleBindingDataSourceConfig(srv.URL, `entity_name = "Administrators"`),
				ExpectError: regexp.MustCompile(`Multiple Role Bindings Found`),
			},
			{
				Config:      testUnitRoleBindingDataSourceConfig(srv.URL, `entity_name = "nobody"`),
				ExpectError: regexp.MustCompile(`Role Binding Not Found`),
			},
			{
				Config:      testUnitRoleBindingDataSourceConfig(srv.URL, "id = \"rb-admins\"\n  entity_name = \"Administrators\""),
				ExpectError: regexp.MustCompile(`only one`),
			},
			{
				Config: testUnitRoleBindingDataSourceConfig(srv.URL, `id = "rb-local"`),
				Check:  resource.TestCheckResourceAttr("data.sspi_role_binding.test", "entity_name", "admin"),
			},
		},
	})
}
