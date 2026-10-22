// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccBackupResource exercises sspi_backup against a REAL SSPI appliance:
// it triggers actual on-demand FULL_BACKUP jobs (POST /sspi/backup) and waits
// for each to reach SUCCESS (GET /sspi/backup/status/{id}).
//
// Prerequisites and side effects:
//   - A backup target must ALREADY be configured on the appliance
//     (sspi_installer_backup_config / PUT /sspi/backup/config). PreCheck
//     skips the test if none is, rather than overwriting an existing config.
//   - Each step that creates the resource ships a real backup bundle to that
//     target, and a backup can't run while another backup/restore/bundle
//     operation is active (the API returns 409).
//   - Backup records are immutable audit trails with no DELETE API, so Destroy
//     only drops them from state; the records remain on the appliance (see
//     testAccCheckBackupRecordRetained).
//   - sspi_backup has no ImportState, so there is no import step.
func TestAccBackupResource(t *testing.T) {
	resourceName := "sspi_backup.test"
	var firstID string

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			testAccRequireBackupConfigured(t)
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBackupRecordRetained,
		Steps: []resource.TestStep{
			// Create + Read: trigger the backup and poll it to SUCCESS.
			{
				Config: testAccBackupResourceConfig("tf-acc-test-backup", "created by TestAccBackupResource"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "backup_type", "FULL_BACKUP"),
					resource.TestCheckResourceAttr(resourceName, "action", "BACKUP"),
					resource.TestCheckResourceAttr(resourceName, "name", "tf-acc-test-backup"),
					resource.TestCheckResourceAttr(resourceName, "description", "created by TestAccBackupResource"),
					resource.TestCheckResourceAttr(resourceName, "status", "SUCCESS"),
					resource.TestCheckResourceAttr(resourceName, "progress", "100"),
					testAccCaptureResourceID(resourceName, &firstID),
				),
			},
			// Re-applying the same config must be a no-op: Read refreshes
			// status/progress but must not trigger another backup.
			{
				Config: testAccBackupResourceConfig("tf-acc-test-backup", "created by TestAccBackupResource"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Changing any user attribute (name) must force replacement, i.e.
			// run a brand-new backup job with a different ID, since the
			// resource has no Update.
			{
				Config: testAccBackupResourceConfig("tf-acc-test-backup-2", "created by TestAccBackupResource"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionReplace),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "name", "tf-acc-test-backup-2"),
					resource.TestCheckResourceAttr(resourceName, "status", "SUCCESS"),
					resource.TestCheckResourceAttr(resourceName, "progress", "100"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources[resourceName]
						if !ok {
							return fmt.Errorf("resource %s not found in state", resourceName)
						}
						if rs.Primary.ID == firstID {
							return fmt.Errorf("expected a new backup job after replacement, but ID is still %s", firstID)
						}
						return nil
					},
				),
			},
		},
		// Destroy runs automatically after the last step; verified by CheckDestroy.
	})
}

// TestAccBackupResource_InvalidBackupType confirms the backup_type validator
// rejects anything but FULL_BACKUP at plan time, before any API call. It needs
// no appliance configuration so it is safe to run anywhere, but still goes
// through testAccPreCheck to follow the other acceptance tests.
func TestAccBackupResource_InvalidBackupType(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "sspi_backup" "test" {
  backup_type = "INCREMENTAL_BACKUP"
}
`,
				ExpectError: regexp.MustCompile(`FULL_BACKUP`),
				PlanOnly:    true,
			},
		},
	})
}

func testAccBackupResourceConfig(name, description string) string {
	return fmt.Sprintf(`
resource "sspi_backup" "test" {
  backup_type = "FULL_BACKUP"
  name        = %q
  description = %q
}
`, name, description)
}

// testAccCaptureResourceID stores the named resource's ID into dst so later
// steps can compare against it.
func testAccCaptureResourceID(name string, dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return fmt.Errorf("resource %s not found in state", name)
		}
		*dst = rs.Primary.ID
		return nil
	}
}

// testAccRequireBackupConfigured skips the test unless the appliance already
// has a backup target configured, since triggering a backup without one fails.
func testAccRequireBackupConfigured(t *testing.T) {
	t.Helper()
	client, err := testAccAPIClient()
	if err != nil {
		t.Fatalf("building API client to check backup config: %s", err)
	}
	resp, err := client.GetBackupConfigWithResponse(context.Background())
	if err != nil {
		t.Fatalf("reading backup config: %s", err)
	}
	if resp.JSON200 == nil || resp.JSON200.ServerAddress == "" {
		t.Skip("no backup target is configured on the SSPI appliance (sspi_installer_backup_config); " +
			"configure one first, TestAccBackupResource will not overwrite it")
	}
}

// testAccCheckBackupRecordRetained documents the resource's no-remote-delete
// behavior: after Destroy, every backup job is gone from Terraform state but
// its record must still be readable on the appliance (HTTP 200).
func testAccCheckBackupRecordRetained(s *terraform.State) error {
	client, err := testAccAPIClient()
	if err != nil {
		return err
	}
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "sspi_backup" {
			continue
		}
		resp, err := client.GetBackupStatusWithResponse(context.Background(), rs.Primary.ID)
		if err != nil {
			return fmt.Errorf("error reading backup %s after destroy: %w", rs.Primary.ID, err)
		}
		if resp.StatusCode() != http.StatusOK {
			return fmt.Errorf("expected backup record %s to be retained on the appliance after destroy (HTTP 200), got HTTP %d",
				rs.Primary.ID, resp.StatusCode())
		}
	}
	return nil
}
