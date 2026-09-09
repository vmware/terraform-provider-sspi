// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// ldapIdentitySourceMockAPI simulates the subset of the SSPI appliance IAM
// API (POST/GET/PUT/DELETE /sspi/iam/ldap-identity-sources[/{id}]) needed to
// drive ssp_ldap_identity_source through a full create/read/teardown cycle
// without a live SSPI appliance.
//
// Create/Update bodies are stored and echoed back verbatim on GET (with "id"
// injected), so every Optional+Computed attribute the resource round-trips
// stays consistent between apply and the post-apply plan-convergence check.
type ldapIdentitySourceMockAPI struct {
	mu                 sync.Mutex
	exists             bool
	body               map[string]any
	connectivityResult map[string]any
}

func newLdapIdentitySourceMockServer(t *testing.T, connectivityResult map[string]any) *httptest.Server {
	t.Helper()
	m := &ldapIdentitySourceMockAPI{connectivityResult: connectivityResult}

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

	responseBody := func() map[string]any {
		resp := map[string]any{}
		m.mu.Lock()
		defer m.mu.Unlock()
		for k, v := range m.body {
			resp[k] = v
		}
		resp["id"] = "ldap-1"
		resp["connectivity_result"] = m.connectivityResult
		return resp
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sspi/iam/ldap-identity-sources", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.exists = true
		m.body = readBody(r)
		m.body["_revision"] = 0
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, responseBody())
	})
	mux.HandleFunc("GET /sspi/iam/ldap-identity-sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		exists := m.exists
		m.mu.Unlock()
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, responseBody())
	})
	mux.HandleFunc("PUT /sspi/iam/ldap-identity-sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		body := readBody(r)
		rev := 1
		if r, ok := m.body["_revision"].(float64); ok {
			rev = int(r) + 1
		}
		body["_revision"] = rev
		m.body = body
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, responseBody())
	})
	mux.HandleFunc("DELETE /sspi/iam/ldap-identity-sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.exists = false
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitLdapIdentitySourceConfig(host, ldapType string) string {
	return testUnitSSPIProviderConfig(host) + fmt.Sprintf(`
resource "sspi_ldap_identity_source" "test" {
  domain       = "corp.local"
  server       = "ldap.corp.local"
  port         = 636
  admin_dn     = "cn=admin,dc=corp,dc=local"
  password     = "s3cr3t"
  base_dn      = "dc=corp,dc=local"
  verify_ssl   = true
  ldap_type    = %q
  certificates = ["-----BEGIN CERTIFICATE-----\nMIIB...fake...\n-----END CERTIFICATE-----"]
}
`, ldapType)
}

// TestUnitLdapIdentitySourceResource exercises ssp_ldap_identity_source's
// Create, Read, and Update (base_dn change, exercising the _revision-aware
// PUT) against a mocked SSPI appliance API, confirming that server/port
// correctly round-trip from the parsed ldap_server.url field and that
// ldap_type/certificates round-trip too.
func TestUnitLdapIdentitySourceResource(t *testing.T) {
	srv := newLdapIdentitySourceMockServer(t, map[string]any{"result": "SUCCESS"})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitLdapIdentitySourceConfig(srv.URL, "ACTIVE_DIRECTORY"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("sspi_ldap_identity_source.test", "id"),
					resource.TestCheckResourceAttr("sspi_ldap_identity_source.test", "domain", "corp.local"),
					resource.TestCheckResourceAttr("sspi_ldap_identity_source.test", "server", "ldap.corp.local"),
					resource.TestCheckResourceAttr("sspi_ldap_identity_source.test", "port", "636"),
					resource.TestCheckResourceAttr("sspi_ldap_identity_source.test", "base_dn", "dc=corp,dc=local"),
					resource.TestCheckResourceAttr("sspi_ldap_identity_source.test", "ldap_type", "ACTIVE_DIRECTORY"),
					resource.TestCheckResourceAttr("sspi_ldap_identity_source.test", "certificates.#", "1"),
				),
			},
			// Update: change base_dn; requires the mock's PUT handler to
			// accept the _revision the resource fetched via GET beforehand.
			{
				Config: testUnitSSPIProviderConfig(srv.URL) + `
resource "sspi_ldap_identity_source" "test" {
  domain       = "corp.local"
  server       = "ldap.corp.local"
  port         = 636
  admin_dn     = "cn=admin,dc=corp,dc=local"
  password     = "s3cr3t"
  base_dn      = "dc=corp,dc=local,dc=updated"
  verify_ssl   = true
  ldap_type    = "ACTIVE_DIRECTORY"
  certificates = ["-----BEGIN CERTIFICATE-----\nMIIB...fake...\n-----END CERTIFICATE-----"]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_ldap_identity_source.test", "base_dn", "dc=corp,dc=local,dc=updated"),
				),
			},
		},
	})
}

// TestUnitLdapIdentitySourceResource_OpenLdap confirms ldap_type=OPEN_LDAP is
// accepted and sent to the API (the resource previously hardcoded
// ACTIVE_DIRECTORY, making OpenLDAP directories unreachable).
func TestUnitLdapIdentitySourceResource_OpenLdap(t *testing.T) {
	srv := newLdapIdentitySourceMockServer(t, map[string]any{"result": "SUCCESS"})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitLdapIdentitySourceConfig(srv.URL, "OPEN_LDAP"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_ldap_identity_source.test", "ldap_type", "OPEN_LDAP"),
				),
			},
		},
	})
}

// TestUnitLdapIdentitySourceResource_ConnectivityFailure confirms that a
// FAILURE connectivity_result from the API on Create surfaces as a hard
// error instead of silently succeeding.
func TestUnitLdapIdentitySourceResource_ConnectivityFailure(t *testing.T) {
	srv := newLdapIdentitySourceMockServer(t, map[string]any{
		"result": "FAILURE",
		"errors": []map[string]any{
			{"error_type": "INVALID_CREDENTIALS", "message": "bad creds"},
		},
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testUnitLdapIdentitySourceConfig(srv.URL, "ACTIVE_DIRECTORY"),
				ExpectError: regexp.MustCompile(regexp.QuoteMeta("Connectivity Check Failed")),
			},
		},
	})
}
