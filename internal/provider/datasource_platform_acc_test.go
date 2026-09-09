// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccPlatformDataSource is a read-only lookup of a pre-existing
// platform ID in whichever SSPI lab environment CI points at. It does NOT
// exercise sspi_platform's Create/Update/Delete lifecycle — see the package
// doc comment in provider_test.go for why full resource CRUD acceptance
// testing is not implemented for sspi_platform (or any sspi_* resource).
func TestAccPlatformDataSource(t *testing.T) {
	resourceName := "data.sspi_platform.test"

	// SSP_TEST_PLATFORM_ID lets this test target a platform ID that already
	// exists in whichever SSPI lab environment CI points at, instead of
	// being tied to one specific lab's snapshot.
	id := os.Getenv("SSP_TEST_PLATFORM_ID")
	if id == "" {
		id = "1643d7da-b8a0-4e13-952c-5fce290c2b8f"
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPlatformDataSourceConfig(id),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", id),
				),
			},
		},
	})
}

func testAccPlatformDataSourceConfig(id string) string {
	return fmt.Sprintf(`
data "sspi_platform" "test" {
  id = %q
}
`, id)
}
