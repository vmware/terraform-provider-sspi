// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccVsphereProviderResource exercises sspi_vsphere_provider's full
// Create -> Read -> Update -> Destroy lifecycle against a REAL SSPI
// appliance and a REAL vCenter Server.
//
// This intentionally departs from the read-only-lookup policy documented in
// provider_test.go's package comment: unlike sspi_platform, registering or
// deregistering a vCenter provider does not provision a Kubernetes cluster
// and has no 30-90 minute LCM workflow, so full-CRUD acceptance testing is
// comparatively cheap here. It still mutates the target SSPI appliance's
// provider inventory for real and requires live vCenter credentials plus a
// full PEM certificate, so run it only against a disposable lab.
//
// Every SSPI appliance we've tried this against so far already has a
// vCenter provider registered - the appliance enforces one provider per
// vCenter server, so that alone makes Create fail with a 409 before this
// test exercises anything. So, gated behind SSP_TEST_ALLOW_DESTROY_EXISTING
// ("true"), PreCheck deletes every pre-existing sspi_platform (which may
// depend on that provider) and then every pre-existing sspi_vsphere_provider
// on the target appliance before Create runs. That is destructive to
// whatever was already deployed there - not just this test's own leftovers -
// so treat SSP_TEST_ALLOW_DESTROY_EXISTING=true as "this appliance's current
// provider/platform are disposable," and don't set it against a shared or
// otherwise load-bearing SSPI appliance.
//
// Every one of the env vars below is required; the test is skipped (not
// failed) if any are unset.
func TestAccVsphereProviderResource(t *testing.T) {
	resourceName := "sspi_vsphere_provider.test"

	envVars := map[string]string{
		"SSP_TEST_VSPHERE_SERVER":       os.Getenv("SSP_TEST_VSPHERE_SERVER"),
		"SSP_TEST_VSPHERE_USER":         os.Getenv("SSP_TEST_VSPHERE_USER"),
		"SSP_TEST_VSPHERE_PASSWORD":     os.Getenv("SSP_TEST_VSPHERE_PASSWORD"),
		"SSP_TEST_VSPHERE_CERTIFICATE":  os.Getenv("SSP_TEST_VSPHERE_CERTIFICATE"),
		"SSP_TEST_VSPHERE_USER_ALT":     os.Getenv("SSP_TEST_VSPHERE_USER_ALT"),
		"SSP_TEST_VSPHERE_PASSWORD_ALT": os.Getenv("SSP_TEST_VSPHERE_PASSWORD_ALT"),
	}
	server := envVars["SSP_TEST_VSPHERE_SERVER"]
	user := envVars["SSP_TEST_VSPHERE_USER"]
	password := envVars["SSP_TEST_VSPHERE_PASSWORD"]
	certificate := envVars["SSP_TEST_VSPHERE_CERTIFICATE"]
	altUser := envVars["SSP_TEST_VSPHERE_USER_ALT"]
	altPassword := envVars["SSP_TEST_VSPHERE_PASSWORD_ALT"]

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			// SSP_TEST_VSPHERE_USER_ALT/PASSWORD_ALT is a second real vCenter
			// service account on the same vCenter Server, used to exercise the
			// Update path (changing "user"/"password" in place, which drives the
			// resource's _revision-aware PUT and its post-update connection
			// re-validation poll against the live appliance).
			for name, v := range envVars {
				if v == "" {
					t.Skipf("%s must be set to run TestAccVsphereProviderResource (full CRUD against a real vCenter/SSPI appliance)", name)
				}
			}
			testAccRequireAllowDestroyExisting(t)
			testAccDeleteExistingPlatforms(t)
			testAccDeleteExistingVsphereProviders(t)
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckVsphereProviderDestroy,
		Steps: []resource.TestStep{
			// Create + Read.
			{
				Config: testAccVsphereProviderResourceConfig(server, user, password, certificate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "server", server),
					resource.TestCheckResourceAttr(resourceName, "user", user),
					resource.TestCheckResourceAttrSet(resourceName, "instance_id"),
					resource.TestCheckResourceAttrSet(resourceName, "created_at"),
					resource.TestCheckResourceAttrSet(resourceName, "updated_at"),
				),
			},
			// Import: re-read the same provider by ID and confirm the resulting
			// state matches, modulo the write-only/never-returned attributes.
			{
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"password"},
			},
			// Update: rotate to an alternate real vCenter service account.
			{
				Config: testAccVsphereProviderResourceConfig(server, altUser, altPassword, certificate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "server", server),
					resource.TestCheckResourceAttr(resourceName, "user", altUser),
				),
			},
			// Re-applying the same config should be a no-op (idempotency check).
			{
				Config:   testAccVsphereProviderResourceConfig(server, altUser, altPassword, certificate),
				PlanOnly: true,
			},
		},
		// Destroy runs automatically after the last step; verified by CheckDestroy.
	})
}

func testAccVsphereProviderResourceConfig(server, user, password, certificate string) string {
	return fmt.Sprintf(`
resource "sspi_vsphere_provider" "test" {
  server      = %q
  user        = %q
  password    = %q
  certificate = %q
}
`, server, user, password, certificate)
}

// testAccCheckVsphereProviderDestroy confirms every sspi_vsphere_provider
// left in state was actually deregistered from the SSPI appliance (GET
// returns 404), not just dropped from Terraform state.
func testAccCheckVsphereProviderDestroy(s *terraform.State) error {
	client, err := testAccAPIClient()
	if err != nil {
		return err
	}
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "sspi_vsphere_provider" {
			continue
		}
		resp, err := client.GetVsphereProviderWithResponse(context.Background(), rs.Primary.ID)
		if err != nil {
			return fmt.Errorf("error checking sspi_vsphere_provider %s was destroyed: %w", rs.Primary.ID, err)
		}
		if resp.StatusCode() != http.StatusNotFound {
			return fmt.Errorf("sspi_vsphere_provider %s still exists on the SSPI appliance (HTTP %d)", rs.Primary.ID, resp.StatusCode())
		}
	}
	return nil
}
