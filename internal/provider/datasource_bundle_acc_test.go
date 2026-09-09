// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccBundleDataSource(t *testing.T) {
	resourceName := "data.sspi_bundle.test"

	// SSP_TEST_BUNDLE_ID lets this test target a bundle ID that already
	// exists in the depot of whichever SSPI lab environment CI points at,
	// instead of being tied to one specific lab's snapshot.
	id := os.Getenv("SSP_TEST_BUNDLE_ID")
	if id == "" {
		id = "1aa48d82-7ebc-4a7b-a08f-f79371e799c9"
	}

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if os.Getenv("SSP_TEST_BUNDLE_ID") == "" {
				t.Log("SSP_TEST_BUNDLE_ID not set; falling back to a hardcoded lab bundle ID that may not exist in this environment")
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccBundleDataSourceConfig(id),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", id),
				),
			},
		},
	})
}

func testAccBundleDataSourceConfig(id string) string {
	return fmt.Sprintf(`
data "sspi_bundle" "test" {
  id = %q
}
`, id)
}
