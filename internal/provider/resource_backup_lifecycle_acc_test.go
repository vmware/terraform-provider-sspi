// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Environment variables for the backup/restore acceptance tests. The SFTP
// target must be reachable FROM THE SSPI APPLIANCE (not just from the machine
// running the test): PUT /sspi/backup/config probes it and fails with error
// 972124 if the appliance cannot connect. Secrets have no defaults.
const (
	accBackupServerEnv     = "SSP_TEST_BACKUP_SERVER_ADDRESS"
	accBackupUsernameEnv   = "SSP_TEST_BACKUP_USERNAME"
	accBackupPasswordEnv   = "SSP_TEST_BACKUP_PASSWORD"
	accBackupLocationEnv   = "SSP_TEST_BACKUP_LOCATION"
	accBackupPassphraseEnv = "SSP_TEST_BACKUP_PASSPHRASE"
	accBackupSSHKeyEnv     = "SSP_TEST_BACKUP_SSH_PUBLIC_KEY"

	// accAllowRestoreEnv gates TestAccRestoreResource: a restore rolls the
	// appliance back to the backup's state.
	accAllowRestoreEnv = "SSP_TEST_ALLOW_RESTORE"
)

type backupAccConfig struct {
	Server, Username, Password, Location, Passphrase, SSHKey string
}

func newBackupAccConfig() backupAccConfig {
	return backupAccConfig{
		Server:     os.Getenv(accBackupServerEnv),
		Username:   os.Getenv(accBackupUsernameEnv),
		Password:   os.Getenv(accBackupPasswordEnv),
		Location:   envOrDefault(accBackupLocationEnv, "/home/worker"),
		Passphrase: os.Getenv(accBackupPassphraseEnv),
		SSHKey:     os.Getenv(accBackupSSHKeyEnv),
	}
}

func testAccBackupPreCheck(t *testing.T, cfg backupAccConfig) {
	t.Helper()
	testAccPreCheck(t)
	required := map[string]string{
		accBackupServerEnv:     cfg.Server,
		accBackupUsernameEnv:   cfg.Username,
		accBackupPasswordEnv:   cfg.Password,
		accBackupPassphraseEnv: cfg.Passphrase,
		// Optional in the schema, but the appliance rejects an empty value
		// (error 972004), so the test requires it.
		accBackupSSHKeyEnv: cfg.SSHKey,
	}
	for name, v := range required {
		if v == "" {
			t.Skipf("%s must be set to run this test (needs an SFTP server reachable from the SSPI appliance)", name)
		}
	}
}

// testAccBackupConfigHCL renders the singleton backup config. Protocol and
// port are pinned to their defaults because both are Optional and
// non-Computed, so the refresh would otherwise disagree with the config.
// backup_location must not end in "/": the API stores it with one, which the
// provider normalises back (see noTrailingSlash in validators.go).
func testAccBackupConfigHCL(cfg backupAccConfig) string {
	return fmt.Sprintf(`
resource "sspi_installer_backup_config" "test" {
  server_address  = %q
  protocol        = "SFTP"
  port            = 22
  username        = %q
  password        = %q
  backup_location = %q
  passphrase      = %q
  ssh_public_key  = %q
}
`, cfg.Server, cfg.Username, cfg.Password, cfg.Location, cfg.Passphrase, cfg.SSHKey)
}

const testAccManualBackupHCL = `
resource "sspi_backup" "test" {
  backup_type = "FULL_BACKUP"
  name        = "tf-acc-manual-backup"
  description = "Created by the sspi provider acceptance test"
  depends_on  = [sspi_installer_backup_config.test]
}
`

func testAccRecurringBackupHCL(enabled bool, hours int) string {
	return fmt.Sprintf(`
resource "sspi_installer_recurring_backup_config" "test" {
  enabled                  = %t
  backup_type              = "FULL_BACKUP"
  backup_schedule_type     = "INTERVAL"
  backup_schedule_interval = {
    hours_between_backups = %d
  }
  depends_on = [sspi_backup.test]
}
`, enabled, hours)
}

const testAccRestoreHCL = `
resource "sspi_restore" "test" {
  backup_id  = sspi_backup.test.id
  depends_on = [sspi_backup.test]
}
`

// TestAccBackupLifecycle runs against a REAL SSPI appliance and covers
// sspi_installer_backup_config -> sspi_backup ->
// sspi_installer_recurring_backup_config (create, update, import).
//
// Things to know before running it:
//   - Terraform destroy of these resources is state-only: the API has no
//     DELETE for the backup config, the recurring config, or a backup, so
//     there is deliberately no CheckDestroy. The test leaves the backup
//     config, the recurring config and one backup on the appliance. It
//     overwrites any existing backup config, so use a disposable lab.
//   - The appliance refuses a backup with 409 (error 972088) while any
//     bundle upload is in progress; wait for uploads to finish first.
//   - sspi_backup polls to a terminal state, so the step returns only once
//     the backup is SUCCESS (or fails).
func TestAccBackupLifecycle(t *testing.T) {
	cfg := newBackupAccConfig()
	const (
		cfgRes = "sspi_installer_backup_config.test"
		bkpRes = "sspi_backup.test"
		recRes = "sspi_installer_recurring_backup_config.test"
	)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccBackupPreCheck(t, cfg) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create the backup config, trigger a manual backup, then enable
			// an INTERVAL recurring backup.
			{
				Config: testAccBackupConfigHCL(cfg) + testAccManualBackupHCL + testAccRecurringBackupHCL(true, 24),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(cfgRes, "id", "singleton"),
					resource.TestCheckResourceAttr(cfgRes, "server_address", cfg.Server),
					resource.TestCheckResourceAttr(cfgRes, "protocol", "SFTP"),
					resource.TestCheckResourceAttr(cfgRes, "port", "22"),
					resource.TestCheckResourceAttr(cfgRes, "username", cfg.Username),
					resource.TestCheckResourceAttr(cfgRes, "backup_location", cfg.Location),

					resource.TestCheckResourceAttrSet(bkpRes, "id"),
					resource.TestCheckResourceAttr(bkpRes, "backup_type", "FULL_BACKUP"),
					resource.TestCheckResourceAttr(bkpRes, "action", "BACKUP"),
					resource.TestCheckResourceAttr(bkpRes, "name", "tf-acc-manual-backup"),
					resource.TestCheckResourceAttr(bkpRes, "status", "SUCCESS"),
					resource.TestCheckResourceAttr(bkpRes, "progress", "100"),

					resource.TestCheckResourceAttr(recRes, "id", "singleton"),
					resource.TestCheckResourceAttr(recRes, "enabled", "true"),
					resource.TestCheckResourceAttr(recRes, "backup_type", "FULL_BACKUP"),
					resource.TestCheckResourceAttr(recRes, "backup_schedule_type", "INTERVAL"),
					resource.TestCheckResourceAttr(recRes, "backup_schedule_interval.hours_between_backups", "24"),
				),
			},
			// Update the recurring config in place (PUT with the current
			// _revision): disable it and change the interval.
			{
				Config: testAccBackupConfigHCL(cfg) + testAccManualBackupHCL + testAccRecurringBackupHCL(false, 12),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(recRes, "enabled", "false"),
					resource.TestCheckResourceAttr(recRes, "backup_schedule_interval.hours_between_backups", "12"),
				),
			},
			// Re-applying the same config must be a no-op (no drift from the
			// API's trailing slash on backup_location).
			{
				Config:   testAccBackupConfigHCL(cfg) + testAccManualBackupHCL + testAccRecurringBackupHCL(false, 12),
				PlanOnly: true,
			},
			// Import the singletons by id. Secrets are write-only, so the
			// API never returns them.
			{
				ResourceName:            cfgRes,
				ImportState:             true,
				ImportStateId:           "singleton",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"password", "passphrase"},
			},
			{
				ResourceName:      recRes,
				ImportState:       true,
				ImportStateId:     "singleton",
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccBackupConfigTrailingSlashRejected checks plan-time validation
// against a real provider build; it needs no appliance connectivity beyond
// the provider block, since validation fails before any API call.
func TestAccBackupConfigTrailingSlashRejected(t *testing.T) {
	cfg := newBackupAccConfig()
	cfg.Location = "/home/worker/"
	if cfg.Server == "" {
		cfg.Server = "sftp.example.invalid"
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccBackupConfigHCL(cfg),
				ExpectError: regexp.MustCompile(`Trailing Slash Not Allowed`),
			},
		},
	})
}

// TestAccRestoreResource restores the appliance from a backup this test
// creates itself. Restore is destructive (the appliance reverts to the
// backup's state), so it is skipped unless SSP_TEST_ALLOW_RESTORE=true.
// The recurring config is deliberately left out: enabling it makes the
// appliance scheduler start a backup immediately, and a restore started
// while that runs is refused with 409 (error 972022).
// sspi_restore is an immutable record: Delete is state-only and any change
// to backup_id forces replacement.
func TestAccRestoreResource(t *testing.T) {
	cfg := newBackupAccConfig()
	const resRestore = "sspi_restore.test"

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccBackupPreCheck(t, cfg)
			if os.Getenv(accAllowRestoreEnv) != "true" {
				t.Skipf("%s must be \"true\" to run this test: it restores the target SSPI appliance (SSPI_HOST) from a backup", accAllowRestoreEnv)
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccBackupConfigHCL(cfg) + testAccManualBackupHCL + testAccRestoreHCL,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resRestore, "id"),
					resource.TestCheckResourceAttrPair(resRestore, "backup_id", "sspi_backup.test", "id"),
					resource.TestCheckResourceAttr(resRestore, "action", "RESTORE"),
					resource.TestCheckResourceAttr(resRestore, "force_restore", "false"),
					resource.TestCheckResourceAttr(resRestore, "status", "SUCCESS"),
					resource.TestCheckResourceAttr(resRestore, "progress", "100"),
				),
			},
		},
	})
}
