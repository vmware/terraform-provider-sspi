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

func writeJSONHelper(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// TestUnitPlatformsDataSource exercises data.sspi_platforms' Read against a
// mocked SSPI appliance API.
func TestUnitPlatformsDataSource(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/platforms", func(w http.ResponseWriter, r *http.Request) {
		writeJSONHelper(w, http.StatusOK, map[string]any{
			"configs": []map[string]any{
				{
					"id":            "platform-1",
					"service":       map[string]any{"instance_name": "tf-unit-platform", "ingress_fqdn": "x", "kafka_fqdn": "x"},
					"system":        map[string]any{"ssp_type": "ATP", "form_factor": "MEDIUM"},
					"desired_state": "START",
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitSSPIProviderConfig(srv.URL) + `
data "sspi_platforms" "test" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.sspi_platforms.test", "results.#", "1"),
					resource.TestCheckResourceAttr("data.sspi_platforms.test", "results.0.id", "platform-1"),
					resource.TestCheckResourceAttr("data.sspi_platforms.test", "results.0.instance_name", "tf-unit-platform"),
					resource.TestCheckResourceAttr("data.sspi_platforms.test", "results.0.ssp_type", "ATP"),
				),
			},
		},
	})
}

// TestUnitVsphereProvidersDataSource exercises data.sspi_vsphere_providers'
// Read against a mocked SSPI appliance API.
func TestUnitVsphereProvidersDataSource(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/providers", func(w http.ResponseWriter, r *http.Request) {
		writeJSONHelper(w, http.StatusOK, map[string]any{
			"configs": []map[string]any{
				{"id": "provider-1", "server": "vc01.corp.local", "user": "admin@vsphere.local"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitSSPIProviderConfig(srv.URL) + `
data "sspi_vsphere_providers" "test" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.sspi_vsphere_providers.test", "results.#", "1"),
					resource.TestCheckResourceAttr("data.sspi_vsphere_providers.test", "results.0.server", "vc01.corp.local"),
				),
			},
		},
	})
}

// TestUnitLdapIdentitySourcesDataSource exercises
// data.sspi_ldap_identity_sources' Read against a mocked SSPI appliance API.
func TestUnitLdapIdentitySourcesDataSource(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/iam/ldap-identity-sources", func(w http.ResponseWriter, r *http.Request) {
		writeJSONHelper(w, http.StatusOK, map[string]any{
			"results": []map[string]any{
				{
					"id":                      "ldap-1",
					"domain_name":             "corp.example.com",
					"ldap_type":               "ACTIVE_DIRECTORY",
					"base_distinguished_name": "dc=corp,dc=example,dc=com",
					"ldap_server":             map[string]any{"url": "ldaps://ldap.corp.example.com:636"},
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitSSPIProviderConfig(srv.URL) + `
data "sspi_ldap_identity_sources" "test" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.sspi_ldap_identity_sources.test", "results.#", "1"),
					resource.TestCheckResourceAttr("data.sspi_ldap_identity_sources.test", "results.0.domain_name", "corp.example.com"),
					resource.TestCheckResourceAttr("data.sspi_ldap_identity_sources.test", "results.0.ldap_type", "ACTIVE_DIRECTORY"),
				),
			},
		},
	})
}

// TestUnitUsersDataSource exercises data.sspi_users' Read against a mocked
// SSPI appliance API.
func TestUnitUsersDataSource(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/iam/users", func(w http.ResponseWriter, r *http.Request) {
		writeJSONHelper(w, http.StatusOK, map[string]any{
			"results": []map[string]any{
				{"name": "admin", "display_name": "Administrator", "user_type": "LOCAL_USER"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitSSPIProviderConfig(srv.URL) + `
data "sspi_users" "test" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.sspi_users.test", "results.#", "1"),
					resource.TestCheckResourceAttr("data.sspi_users.test", "results.0.name", "admin"),
					resource.TestCheckResourceAttr("data.sspi_users.test", "results.0.user_type", "LOCAL_USER"),
				),
			},
		},
	})
}

// TestUnitRolesDataSource exercises data.sspi_roles' Read against a mocked
// SSPI appliance API.
func TestUnitRolesDataSource(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/iam/roles", func(w http.ResponseWriter, r *http.Request) {
		writeJSONHelper(w, http.StatusOK, map[string]any{
			"results": []map[string]any{
				{"role": "AUDITOR", "display_name": "Auditor"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitSSPIProviderConfig(srv.URL) + `
data "sspi_roles" "test" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.sspi_roles.test", "results.#", "1"),
					resource.TestCheckResourceAttr("data.sspi_roles.test", "results.0.role", "AUDITOR"),
				),
			},
		},
	})
}

// TestUnitRoleBindingsDataSource exercises data.sspi_role_bindings' Read
// against a mocked SSPI appliance API.
func TestUnitRoleBindingsDataSource(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/iam/role-bindings", func(w http.ResponseWriter, r *http.Request) {
		writeJSONHelper(w, http.StatusOK, map[string]any{
			"results": []map[string]any{
				{
					"id":          "binding-1",
					"entity_name": "sspuser1@corp.example.com",
					"user_type":   "REMOTE_USER",
					"roles":       []map[string]any{{"role": "AUDITOR"}},
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitSSPIProviderConfig(srv.URL) + `
data "sspi_role_bindings" "test" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.sspi_role_bindings.test", "results.#", "1"),
					resource.TestCheckResourceAttr("data.sspi_role_bindings.test", "results.0.entity_name", "sspuser1@corp.example.com"),
					resource.TestCheckResourceAttr("data.sspi_role_bindings.test", "results.0.roles.0", "AUDITOR"),
				),
			},
		},
	})
}

// TestUnitRoleBindingsDataSource_NoLdapConfigured verifies the documented
// 204 "no LDAP identity source configured yet" response is treated as an
// empty list, not an error.
func TestUnitRoleBindingsDataSource_NoLdapConfigured(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/iam/role-bindings", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitSSPIProviderConfig(srv.URL) + `
data "sspi_role_bindings" "test" {}
`,
				Check: resource.TestCheckResourceAttr("data.sspi_role_bindings.test", "results.#", "0"),
			},
		},
	})
}
