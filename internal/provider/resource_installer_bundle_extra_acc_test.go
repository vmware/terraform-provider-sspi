// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccBundleRemoteResource_Replace verifies that changing "url" (which is
// RequiresReplace; Update is unsupported) deletes the old bundle and imports
// the new one, leaving only the new bundle in the Depot.
//
// Required env vars (skipped if unset): SSP_TEST_BUNDLE_URL and
// SSP_TEST_BUNDLE_URL_ALT, two URLs of different bundles.
func TestAccBundleRemoteResource_Replace(t *testing.T) {
	resourceName := "sspi_installer_bundle_remote.test"
	urlA := os.Getenv("SSP_TEST_BUNDLE_URL")
	urlB := os.Getenv("SSP_TEST_BUNDLE_URL_ALT")

	var firstID string
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if urlA == "" || urlB == "" {
				t.Skip("SSP_TEST_BUNDLE_URL and SSP_TEST_BUNDLE_URL_ALT must be set to run TestAccBundleRemoteResource_Replace")
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBundleDestroy("sspi_installer_bundle_remote"),
		Steps: []resource.TestStep{
			{
				Config: testAccBundleRemoteConfig(urlA),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "status", "READY"),
					testAccCaptureAttr(resourceName, "id", &firstID),
				),
			},
			{
				Config: testAccBundleRemoteConfig(urlB),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "url", urlB),
					resource.TestCheckResourceAttr(resourceName, "status", "READY"),
					testAccCheckAttrChanged(resourceName, "id", &firstID),
				),
			},
		},
	})
}

// TestAccBundleLocalAndRemoteParallel applies a local upload and a remote
// import of two different bundles in one apply (Terraform runs independent
// resources concurrently) and destroys both. The appliance accepting
// concurrent bundle imports is what the per-file progress logging relies on.
//
// Required env vars (skipped if unset): SSP_TEST_BUNDLE_FILE and
// SSP_TEST_BUNDLE_URL, which must be different bundles.
func TestAccBundleLocalAndRemoteParallel(t *testing.T) {
	file := os.Getenv("SSP_TEST_BUNDLE_FILE")
	url := os.Getenv("SSP_TEST_BUNDLE_URL")
	local, remote := "sspi_installer_bundle_local.test", "sspi_installer_bundle_remote.test"

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if file == "" || url == "" {
				t.Skip("SSP_TEST_BUNDLE_FILE and SSP_TEST_BUNDLE_URL must be set to run TestAccBundleLocalAndRemoteParallel")
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(
			testAccCheckBundleDestroy("sspi_installer_bundle_local"),
			testAccCheckBundleDestroy("sspi_installer_bundle_remote"),
		),
		Steps: []resource.TestStep{
			{
				Config: testAccBundleLocalConfig(file) + testAccBundleRemoteConfig(url),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(local, "status", "READY"),
					resource.TestCheckResourceAttr(remote, "status", "READY"),
					func(s *terraform.State) error {
						l, r := s.RootModule().Resources[local], s.RootModule().Resources[remote]
						if l == nil || r == nil {
							return fmt.Errorf("expected both %s and %s in state", local, remote)
						}
						if l.Primary.ID == r.Primary.ID {
							return fmt.Errorf("local and remote both track bundle %s; they must be different bundles", l.Primary.ID)
						}
						return nil
					},
				),
			},
		},
	})
}

func testAccCaptureAttr(name, attr string, dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return fmt.Errorf("%s not found in state", name)
		}
		*dst = rs.Primary.Attributes[attr]
		return nil
	}
}

func testAccCheckAttrChanged(name, attr string, previous *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return fmt.Errorf("%s not found in state", name)
		}
		if got := rs.Primary.Attributes[attr]; got == *previous {
			return fmt.Errorf("%s.%s is still %q; expected the resource to be replaced", name, attr, got)
		}
		return nil
	}
}
