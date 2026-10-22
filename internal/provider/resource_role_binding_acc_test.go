// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const (
	roleBindingAccAllowEnvVar  = "SSP_TEST_ALLOW_ROLE_BINDING"
	roleBindingAccEntityEnvVar = "SSP_TEST_ROLE_BINDING_ENTITY"
)

// TestAccRoleBindingDataSource_LocalUser is a read-only lookup of the role
// binding of the account the provider itself authenticates as (SSPI_USERNAME,
// a LOCAL_USER), by entity_name and then by id. It changes nothing on the
// appliance.
func TestAccRoleBindingDataSource_LocalUser(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
data "sspi_role_binding" "by_name" {
  entity_name = %q
  user_type   = "LOCAL_USER"
}

data "sspi_role_binding" "by_id" {
  id = data.sspi_role_binding.by_name.id
}
`, os.Getenv("SSPI_USERNAME")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.sspi_role_binding.by_name", "id"),
					resource.TestCheckResourceAttr("data.sspi_role_binding.by_name", "user_type", "LOCAL_USER"),
					resource.TestCheckResourceAttrSet("data.sspi_role_binding.by_name", "roles.#"),
					resource.TestCheckResourceAttrPair("data.sspi_role_binding.by_id", "entity_name", "data.sspi_role_binding.by_name", "entity_name"),
					resource.TestCheckResourceAttrPair("data.sspi_role_binding.by_id", "roles.#", "data.sspi_role_binding.by_name", "roles.#"),
				),
			},
			{
				Config: `
data "sspi_role_binding" "missing" {
  entity_name = "sspi-acc-test-no-such-entity"
}
`,
				ExpectError: regexp.MustCompile(`Role Binding Not Found`),
			},
		},
	})
}

func testAccRoleBindingConfig(entity, roles string) string {
	return fmt.Sprintf(`
resource "sspi_role_binding" "test" {
  entity_name = %q
  user_type   = "REMOTE_USER"
  roles       = %s
}

data "sspi_role_binding" "by_id" {
  id = sspi_role_binding.test.id
}

data "sspi_role_binding" "by_name" {
  entity_name = sspi_role_binding.test.entity_name
  user_type   = "REMOTE_USER"
  depends_on  = [sspi_role_binding.test]
}
`, entity, roles)
}

// TestAccRoleBindingResource_RemoteUser runs create, update (PUT with
// _revision), drift check, data source lookups and import of
// sspi_role_binding against a REAL SSPI appliance.
//
// Prerequisites and impact:
//   - An LDAP identity source must already be configured on the appliance, and
//     SSP_TEST_ROLE_BINDING_ENTITY must name an existing user in it
//     (user@domain).
//   - Some appliance builds (seen on 5.2.1.0.0.25800116) answer DELETE of a
//     remote-user role binding with 200 but keep the entry and its roles under
//     a new id, so the test can leave that binding behind. Because of that the
//     test only runs with SSP_TEST_ALLOW_ROLE_BINDING=true. CheckDestroy
//     checks the original id, so it passes either way; it cannot see a
//     leftover entry.
func TestAccRoleBindingResource_RemoteUser(t *testing.T) {
	entity := os.Getenv(roleBindingAccEntityEnvVar)
	name := "sspi_role_binding.test"

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if os.Getenv(roleBindingAccAllowEnvVar) != "true" {
				t.Skipf("%s=true is required: deleting a remote-user role binding can leave an entry behind on some appliance builds", roleBindingAccAllowEnvVar)
			}
			if entity == "" {
				t.Skipf("%s must be set (an existing LDAP user, user@domain)", roleBindingAccEntityEnvVar)
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRoleBindingDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccRoleBindingConfig(entity, `["AUDITOR"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(name, "id"),
					resource.TestCheckResourceAttr(name, "entity_name", entity),
					resource.TestCheckResourceAttr(name, "user_type", "REMOTE_USER"),
					resource.TestCheckResourceAttr(name, "roles.#", "1"),
					resource.TestCheckTypeSetElemAttr(name, "roles.*", "AUDITOR"),
					resource.TestCheckResourceAttrPair("data.sspi_role_binding.by_id", "id", name, "id"),
					resource.TestCheckResourceAttrPair("data.sspi_role_binding.by_name", "id", name, "id"),
					resource.TestCheckResourceAttr("data.sspi_role_binding.by_name", "roles.#", "1"),
				),
			},
			{
				// Re-applying the same config must plan no changes (no drift).
				Config:   testAccRoleBindingConfig(entity, `["AUDITOR"]`),
				PlanOnly: true,
			},
			{
				// An unknown role is rejected at plan time.
				Config:      testAccRoleBindingConfig(entity, `["NOT_A_ROLE"]`),
				ExpectError: regexp.MustCompile(`NOT_A_ROLE`),
			},
			{
				// Adding a role is an in-place update, not a replacement.
				Config: testAccRoleBindingConfig(entity, `["AUDITOR", "SUPPORT_BUNDLE_COLLECTOR"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "roles.#", "2"),
					resource.TestCheckTypeSetElemAttr(name, "roles.*", "AUDITOR"),
					resource.TestCheckTypeSetElemAttr(name, "roles.*", "SUPPORT_BUNDLE_COLLECTOR"),
					resource.TestCheckResourceAttr("data.sspi_role_binding.by_id", "roles.#", "2"),
				),
			},
			{
				ResourceName:      name,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// testAccCheckRoleBindingDestroy confirms every sspi_role_binding left in
// state no longer resolves by its id (GET returns 404).
func testAccCheckRoleBindingDestroy(s *terraform.State) error {
	client, err := testAccIAMClient()
	if err != nil {
		return err
	}
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "sspi_role_binding" {
			continue
		}
		resp, err := client.GetRoleBindingWithResponse(context.Background(), rs.Primary.ID)
		if err != nil {
			return fmt.Errorf("error checking sspi_role_binding %s was destroyed: %w", rs.Primary.ID, err)
		}
		if resp.StatusCode() != http.StatusNotFound {
			return fmt.Errorf("sspi_role_binding %s still exists on the SSPI appliance (HTTP %d)", rs.Primary.ID, resp.StatusCode())
		}
	}
	return nil
}
