// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/vmware/terraform-provider-sspi/internal/client/iam_client"
)

const (
	ldapAccDomainEnvVar   = "SSP_TEST_LDAP_DOMAIN"
	ldapAccServerEnvVar   = "SSP_TEST_LDAP_SERVER"
	ldapAccPortEnvVar     = "SSP_TEST_LDAP_PORT"
	ldapAccBaseDNEnvVar   = "SSP_TEST_LDAP_BASE_DN"
	ldapAccAdminDNEnvVar  = "SSP_TEST_LDAP_ADMIN_DN"
	ldapAccPasswordEnvVar = "SSP_TEST_LDAP_PASSWORD"
	ldapAccCertFileEnvVar = "SSP_TEST_LDAP_CERT_FILE"
)

type ldapAccConfig struct {
	domain, server, baseDN, adminDN, password, cert string
	port                                            int
}

// ldapAccConfigFromEnv skips the test unless every SSP_TEST_LDAP_* variable
// is set. The server must be an Active Directory reachable and resolvable
// from the SSPI appliance (the appliance resolves names itself, so an
// FQDN needs a DNS or hosts entry on the appliance's resolver), and
// SSP_TEST_LDAP_CERT_FILE must point at its PEM certificate.
// SSP_TEST_LDAP_ADMIN_DN must be a userPrincipalName (user@domain).
func ldapAccConfigFromEnv(t *testing.T) ldapAccConfig {
	t.Helper()
	cfg := ldapAccConfig{
		domain:   os.Getenv(ldapAccDomainEnvVar),
		server:   os.Getenv(ldapAccServerEnvVar),
		baseDN:   os.Getenv(ldapAccBaseDNEnvVar),
		adminDN:  os.Getenv(ldapAccAdminDNEnvVar),
		password: os.Getenv(ldapAccPasswordEnvVar),
		port:     636,
	}
	for name, v := range map[string]string{
		ldapAccDomainEnvVar: cfg.domain, ldapAccServerEnvVar: cfg.server, ldapAccBaseDNEnvVar: cfg.baseDN,
		ldapAccAdminDNEnvVar: cfg.adminDN, ldapAccPasswordEnvVar: cfg.password,
	} {
		if v == "" {
			t.Skipf("%s must be set to run the sspi_ldap_identity_source acceptance tests", name)
		}
	}
	if v := os.Getenv(ldapAccPortEnvVar); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("%s must be a number: %v", ldapAccPortEnvVar, err)
		}
		cfg.port = p
	}
	certFile := os.Getenv(ldapAccCertFileEnvVar)
	if certFile == "" {
		t.Skipf("%s must be set to run the sspi_ldap_identity_source acceptance tests", ldapAccCertFileEnvVar)
	}
	pem, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatalf("reading %s: %v", ldapAccCertFileEnvVar, err)
	}
	cfg.cert = string(pem)
	return cfg
}

func testAccLdapIdentitySourceConfig(cfg ldapAccConfig, adminDN string) string {
	return fmt.Sprintf(`
resource "sspi_ldap_identity_source" "test" {
  domain       = %q
  server       = %q
  port         = %d
  admin_dn     = %q
  password     = %q
  base_dn      = %q
  ldap_type    = "ACTIVE_DIRECTORY"
  certificates = [%q]
}
`, cfg.domain, cfg.server, cfg.port, adminDN, cfg.password, cfg.baseDN, cfg.cert)
}

// TestAccLdapIdentitySourceResource runs create, read-back, drift check and
// import of sspi_ldap_identity_source against a REAL SSPI appliance and a
// real Active Directory, then destroys it and confirms the appliance
// returns 404 for the id.
//
// Prerequisites:
//   - The appliance must not already have an LDAP identity source for the same
//     domain, and no remote role binding may exist for it: the appliance
//     refuses to delete an identity source while remote users or groups
//     are bound ("ensure that all existing remote users are removed"), which
//     would make the final destroy fail.
//   - SSP_TEST_LDAP_DOMAIN, _SERVER, _BASE_DN, _ADMIN_DN (user@domain),
//     _PASSWORD, _CERT_FILE and optionally _PORT (default 636).
//
// A second step creates a source with a bind identity in DN format, which the
// appliance rejects, to confirm the API error is surfaced to the user.
func TestAccLdapIdentitySourceResource(t *testing.T) {
	cfg := ldapAccConfigFromEnv(t)
	name := "sspi_ldap_identity_source.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckLdapIdentitySourceDestroy,
		Steps: []resource.TestStep{
			{
				// An Active Directory bind identity must be user@domain.
				Config:      testAccLdapIdentitySourceConfig(cfg, "cn=administrator,cn=Users,dc=invalid,dc=test"),
				ExpectError: regexp.MustCompile(`userPrincipalName`),
			},
			{
				Config: testAccLdapIdentitySourceConfig(cfg, cfg.adminDN),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(name, "id"),
					resource.TestCheckResourceAttr(name, "domain", cfg.domain),
					resource.TestCheckResourceAttr(name, "server", cfg.server),
					resource.TestCheckResourceAttr(name, "port", strconv.Itoa(cfg.port)),
					resource.TestCheckResourceAttr(name, "base_dn", cfg.baseDN),
					resource.TestCheckResourceAttr(name, "admin_dn", cfg.adminDN),
					resource.TestCheckResourceAttr(name, "ldap_type", "ACTIVE_DIRECTORY"),
					resource.TestCheckResourceAttr(name, "certificates.#", "1"),
					testAccCheckLdapIdentitySourceConnected(name),
				),
			},
			{
				// Re-applying the same config must plan no changes (no drift).
				Config:   testAccLdapIdentitySourceConfig(cfg, cfg.adminDN),
				PlanOnly: true,
			},
			{
				ResourceName:      name,
				ImportState:       true,
				ImportStateVerify: true,
				// The API never returns the password, and verify_ssl has no
				// server-side equivalent.
				ImportStateVerifyIgnore: []string{"password", "verify_ssl"},
			},
		},
	})
}

// testAccCheckLdapIdentitySourceConnected asks the appliance to test the
// connection (check_connection_status=true) and requires SUCCESS.
func testAccCheckLdapIdentitySourceConnected(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource %s not found in state", resourceName)
		}
		client, err := testAccIAMClient()
		if err != nil {
			return err
		}
		check := true
		resp, err := client.GetLdapIdentitySourceWithResponse(context.Background(), rs.Primary.ID, &iam_client.GetLdapIdentitySourceParams{CheckConnectionStatus: &check})
		if err != nil {
			return err
		}
		if resp.JSON200 == nil {
			return fmt.Errorf("unexpected response reading %s: %d, body: %s", rs.Primary.ID, resp.StatusCode(), string(resp.Body))
		}
		if cr := resp.JSON200.ConnectivityResult; cr == nil || cr.Result != iam_client.SUCCESS {
			return fmt.Errorf("LDAP identity source %s connectivity is not SUCCESS: %s", rs.Primary.ID, string(resp.Body))
		}
		return nil
	}
}

// testAccCheckLdapIdentitySourceDestroy confirms every sspi_ldap_identity_source
// left in state was removed from the appliance (GET returns 404).
func testAccCheckLdapIdentitySourceDestroy(s *terraform.State) error {
	client, err := testAccIAMClient()
	if err != nil {
		return err
	}
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "sspi_ldap_identity_source" {
			continue
		}
		resp, err := client.GetLdapIdentitySourceWithResponse(context.Background(), rs.Primary.ID, nil)
		if err != nil {
			return fmt.Errorf("error checking sspi_ldap_identity_source %s was destroyed: %w", rs.Primary.ID, err)
		}
		if resp.StatusCode() != http.StatusNotFound {
			return fmt.Errorf("sspi_ldap_identity_source %s still exists on the SSPI appliance (HTTP %d)", rs.Primary.ID, resp.StatusCode())
		}
	}
	return nil
}
