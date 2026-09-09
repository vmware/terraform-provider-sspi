// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import "fmt"

// testUnitSSPIProviderConfig renders a `provider "sspi"` block pointing the
// SSPI appliance client at a local httptest server, for unit tests that
// exercise installer resources (sspi_platform, ...) without any live
// SSPI appliance.
func testUnitSSPIProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "sspi" {
  host     = %q
  username = "admin"
  password = "admin"
  insecure = true
}
`, host)
}
