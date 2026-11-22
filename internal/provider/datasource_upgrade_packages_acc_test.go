// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccUpgradePackagesDataSource is a read-only listing of the staged
// upgrade packages (GET /sspi/upgrade/packages). It changes nothing on the
// appliance and works whether or not any package is staged: the list must
// always be present, and its length must match what the appliance reports
// directly. When packages are staged, every entry must have all three fields.
func TestAccUpgradePackagesDataSource(t *testing.T) {
	const resourceName = "data.sspi_upgrade_packages.test"

	var staged int
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			n, _, err := testAccApplianceUpgradeState()
			if err != nil {
				t.Fatalf("reading appliance upgrade state: %s", err)
			}
			staged = n
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "sspi_upgrade_packages" "test" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					func(s *terraform.State) error {
						return resource.TestCheckResourceAttr(resourceName, "packages.#", strconv.Itoa(staged))(s)
					},
					func(s *terraform.State) error {
						for i := 0; i < staged; i++ {
							for _, attr := range []string{"package_name", "package_size", "version"} {
								key := fmt.Sprintf("packages.%d.%s", i, attr)
								if err := resource.TestCheckResourceAttrSet(resourceName, key)(s); err != nil {
									return err
								}
							}
						}
						return nil
					},
				),
			},
		},
	})
}
