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

// TestAccBundleRemoteResource exercises sspi_installer_bundle_remote's
// Create -> Read -> Import -> Destroy lifecycle against a REAL SSPI
// appliance. The appliance itself downloads the bundle, so the URL must be
// reachable from the appliance without authentication.
//
// Required env vars (the test is skipped if unset):
//   - SSP_TEST_BUNDLE_URL: HTTP(S) URL of a valid bundle file.
//
// Importing a bundle into the Depot is slow for real bundles and is deleted
// again at the end of the test, so run it only against a disposable lab.
func TestAccBundleRemoteResource(t *testing.T) {
	resourceName := "sspi_installer_bundle_remote.test"
	bundleURL := os.Getenv("SSP_TEST_BUNDLE_URL")

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if bundleURL == "" {
				t.Skip("SSP_TEST_BUNDLE_URL must be set to run TestAccBundleRemoteResource")
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBundleDestroy("sspi_installer_bundle_remote"),
		Steps: []resource.TestStep{
			// Create + Read. id must be the real bundle ID (from status_url),
			// which the resource also exposes as package_id.
			{
				Config: testAccBundleRemoteConfig(bundleURL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttrPair(resourceName, "package_id", resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "status", "READY"),
					resource.TestCheckResourceAttrSet(resourceName, "version"),
				),
			},
			// Import by bundle ID; url is not returned by the API.
			{
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"url"},
			},
			// Re-applying the same config must be a no-op.
			{
				Config:   testAccBundleRemoteConfig(bundleURL),
				PlanOnly: true,
			},
		},
		// Destroy runs automatically after the last step; verified by CheckDestroy.
	})
}

// TestAccBundleRemoteResource_ImportFailed verifies a bundle URL the
// appliance cannot import surfaces as an error from Create rather than a
// hang or a half-created resource. Needs only a reachable appliance.
func TestAccBundleRemoteResource_ImportFailed(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Not a bundle; the appliance rejects it up front (4xx) or the
				// import job ends FAILED, and either way Create must error.
				Config:      testAccBundleRemoteConfig("https://invalid.example.invalid/not-a-bundle.tar"),
				ExpectError: regexp.MustCompile(`(?i)import`),
			},
		},
	})
}

// TestAccBundleLocalResource exercises sspi_installer_bundle_local's
// Create -> Read -> Import -> Destroy lifecycle against a REAL SSPI
// appliance, streaming a real bundle file through the (long-timeout) upload
// client.
//
// Required env vars (the test is skipped if unset):
//   - SSP_TEST_BUNDLE_FILE: path to a valid bundle file on the machine
//     running the test. Bundles can be many GB; expect a long upload.
func TestAccBundleLocalResource(t *testing.T) {
	resourceName := "sspi_installer_bundle_local.test"
	bundleFile := os.Getenv("SSP_TEST_BUNDLE_FILE")

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if bundleFile == "" {
				t.Skip("SSP_TEST_BUNDLE_FILE must be set to run TestAccBundleLocalResource")
			}
			if _, err := os.Stat(bundleFile); err != nil {
				t.Skipf("SSP_TEST_BUNDLE_FILE %q is not readable: %s", bundleFile, err)
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBundleDestroy("sspi_installer_bundle_local"),
		Steps: []resource.TestStep{
			// Create + Read.
			{
				Config: testAccBundleLocalConfig(bundleFile),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttrPair(resourceName, "package_id", resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "status", "READY"),
					resource.TestCheckResourceAttrSet(resourceName, "version"),
				),
			},
			// Import by bundle ID; file_path is not returned by the API.
			{
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"file_path"},
			},
			// Re-applying the same config must be a no-op.
			{
				Config:   testAccBundleLocalConfig(bundleFile),
				PlanOnly: true,
			},
		},
	})
}

func testAccBundleRemoteConfig(url string) string {
	return fmt.Sprintf(`
resource "sspi_installer_bundle_remote" "test" {
  url = %q
}
`, url)
}

func testAccBundleLocalConfig(filePath string) string {
	return fmt.Sprintf(`
resource "sspi_installer_bundle_local" "test" {
  file_path = %q
}
`, filePath)
}

// testAccCheckBundleDestroy confirms every bundle resource of the given type
// left in state was actually deleted from the Depot (GET returns 404), not
// just dropped from Terraform state.
func testAccCheckBundleDestroy(resourceType string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		client, err := testAccDepotClient()
		if err != nil {
			return err
		}
		for _, rs := range s.RootModule().Resources {
			if rs.Type != resourceType {
				continue
			}
			resp, err := client.GetBundleWithResponse(context.Background(), rs.Primary.ID)
			if err != nil {
				return fmt.Errorf("error checking %s %s was destroyed: %w", resourceType, rs.Primary.ID, err)
			}
			if resp.StatusCode() != http.StatusNotFound {
				return fmt.Errorf("%s %s still exists in the SSPI Depot (HTTP %d)", resourceType, rs.Primary.ID, resp.StatusCode())
			}
		}
		return nil
	}
}
