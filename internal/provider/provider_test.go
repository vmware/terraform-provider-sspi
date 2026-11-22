// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

// Package provider_test holds this provider's acceptance and unit tests.
//
// Acceptance test scope: the data sources (platform, vsphere_provider,
// bundle, role_binding, upgrade_packages) are read-only lookups against
// whatever exists in the SSPI lab CI points at. Most resources also have
// live lifecycle tests (create, update, import, idempotency, destroy), each
// skipped unless its own env vars are set; destructive ones (platform,
// vsphere_provider, restore, upgrade, user_password, role_binding) need an
// explicit SSP_TEST_ALLOW_* opt-in. See the doc comment on each test for its
// prerequisites and side effects.
//
// Known gaps: sspi_platform scaling and force_delete, sspi_restore
// replacement and force_restore, and a LOCAL_USER sspi_role_binding resource
// are not covered because they would be destructive or take hours. The
// mocked-HTTP unit tests (TestUnit*) in this package cover full CRUD against
// a fake server.
package provider_test

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/vmware/terraform-provider-sspi/internal/provider"
)

// testAccProtoV6ProviderFactories passes the provider factory into test assertions.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"sspi": providerserver.NewProtocol6WithError(provider.New("test")()),
}

func testAccPreCheck(t *testing.T) {
	if v := os.Getenv("SSPI_HOST"); v == "" {
		if v2 := os.Getenv("SSPI_ENDPOINT"); v2 == "" {
			t.Skip("SSPI_HOST or SSPI_ENDPOINT must be set for SSPI acceptance tests")
		}
	}
	if v := os.Getenv("SSPI_USERNAME"); v == "" {
		t.Skip("SSPI_USERNAME must be set for SSPI acceptance tests")
	}
	if v := os.Getenv("SSPI_PASSWORD"); v == "" {
		t.Skip("SSPI_PASSWORD must be set for SSPI acceptance tests")
	}
}
