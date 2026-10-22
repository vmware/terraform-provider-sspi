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

// TestAccPlatformResource exercises sspi_platform's full
// Create -> Read -> Update -> Destroy lifecycle against a REAL SSPI
// appliance, deploying an actual SSP instance on real vSphere
// infrastructure.
//
// This intentionally departs from the read-only-lookup policy documented in
// provider_test.go's package comment, at the user's explicit request. Be
// aware of what that means in practice:
//   - Every apply/destroy provisions/tears down a real Kubernetes cluster on
//     real vSphere infra. Per the FSDD's async-operations risk note, each of
//     Create/Update/Delete can individually take up to platformPollTimeout
//     (90 minutes in production), so a full run of this test can take
//     multiple hours.
//   - It consumes real compute/storage/IP/DNS resources in whichever lab
//     SSPI_HOST points at. The IP pools, FQDNs, and display name below MUST
//     be unique per run and clear of any other platform in that lab (see
//     testbed/main.tf's own "still UNVERIFIED as truly free" caveat about
//     this exact lab) - there are deliberately no built-in defaults for
//     those, to force the caller to supply fresh values instead of
//     colliding with a previous run or another deployed platform.
//   - Run only against a disposable lab, ideally one dedicated to this test.
//   - Every SSPI appliance we've tried this against so far already has a
//     platform deployed, and its VMs alone can consume all the per-host ESXi
//     memory those clusters have (confirmed against a real lab: a second,
//     minimum-sized MEDIUM/ATP platform failed the "Check vCenter" precheck
//     with "cannot place all 11 VMs ... across 5 ESXi hosts" even though
//     aggregate cluster memory looked sufficient). So, gated behind
//     SSP_TEST_ALLOW_DESTROY_EXISTING ("true"), PreCheck deletes every
//     pre-existing sspi_platform on the target appliance before Create runs.
//     That deletes whatever was already deployed there, not just this test's
//     own leftovers - only set SSP_TEST_ALLOW_DESTROY_EXISTING=true if this
//     appliance's current platform is disposable.
//
// SSP_TEST_PLATFORM_ADMIN_PASSWORD/AUDIT_PASSWORD have no defaults (secrets
// aren't hardcoded here); the rest fall back to the shared, safe-to-reuse
// vCenter infra references documented in testbed/main.tf, but every value
// can be overridden to point at a different lab.
func TestAccPlatformResource(t *testing.T) {
	resourceName := "sspi_platform.test"

	cfg := platformAccConfig{
		ProviderID:         envOrDefault("SSP_TEST_PLATFORM_PROVIDER_ID", "bbed6f48-368f-4ad6-a857-0bb494ba206a"),
		DisplayName:        os.Getenv("SSP_TEST_PLATFORM_DISPLAY_NAME"),
		FormFactor:         envOrDefault("SSP_TEST_PLATFORM_FORM_FACTOR", "MEDIUM"),
		WorkerCount:        envOrDefault("SSP_TEST_PLATFORM_WORKER_COUNT", "2"),
		ControllerCount:    envOrDefault("SSP_TEST_PLATFORM_CONTROLLER_COUNT", "3"),
		DatacenterID:       envOrDefault("SSP_TEST_PLATFORM_DATACENTER_ID", "datacenter-3"),
		ClusterID:          envOrDefault("SSP_TEST_PLATFORM_CLUSTER_ID", "domain-c9"),
		ContentDatastoreID: envOrDefault("SSP_TEST_PLATFORM_CONTENT_DATASTORE_ID", "datastore-45"),
		StoragePolicyID:    envOrDefault("SSP_TEST_PLATFORM_STORAGE_POLICY_ID", "aa6d5a82-1c88-45da-85d3-3d74b91a5bad"),
		Domain:             envOrDefault("SSP_TEST_PLATFORM_DOMAIN", "nsbucqesystem.test"),
		DNSServer:          envOrDefault("SSP_TEST_PLATFORM_DNS_SERVER", "172.16.111.1"),
		NTPServer:          envOrDefault("SSP_TEST_PLATFORM_NTP_SERVER", "ntp.broadcom.net"),
		NetworkID:          envOrDefault("SSP_TEST_PLATFORM_NETWORK_ID", "dvs-47"),
		PortgroupID:        envOrDefault("SSP_TEST_PLATFORM_PORTGROUP_ID", "dvportgroup-49"),
		DefaultGateway:     envOrDefault("SSP_TEST_PLATFORM_DEFAULT_GATEWAY", "172.16.111.1"),
		Subnet:             envOrDefault("SSP_TEST_PLATFORM_SUBNET", "172.16.111.1/24"),
		NodeIPPool:         os.Getenv("SSP_TEST_PLATFORM_NODE_IP_POOL"),
		ServiceIPPool:      os.Getenv("SSP_TEST_PLATFORM_SERVICE_IP_POOL"),
		IngressFqdn:        os.Getenv("SSP_TEST_PLATFORM_INGRESS_FQDN"),
		KafkaFqdn:          os.Getenv("SSP_TEST_PLATFORM_KAFKA_FQDN"),
		SspBundleID:        envOrDefault("SSP_TEST_PLATFORM_BUNDLE_ID", "6b4c85e8-930c-416f-a6da-0e25a96cf131"),
		AdminPassword:      os.Getenv("SSP_TEST_PLATFORM_ADMIN_PASSWORD"),
		AuditPassword:      os.Getenv("SSP_TEST_PLATFORM_AUDIT_PASSWORD"),
	}

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			// These have no safe defaults - either they're secrets, or reusing a
			// hardcoded value risks colliding with another platform/run in the
			// same lab (see the caveats in the doc comment above).
			required := map[string]string{
				"SSP_TEST_PLATFORM_DISPLAY_NAME":    cfg.DisplayName,
				"SSP_TEST_PLATFORM_NODE_IP_POOL":    cfg.NodeIPPool,
				"SSP_TEST_PLATFORM_SERVICE_IP_POOL": cfg.ServiceIPPool,
				"SSP_TEST_PLATFORM_INGRESS_FQDN":    cfg.IngressFqdn,
				"SSP_TEST_PLATFORM_KAFKA_FQDN":      cfg.KafkaFqdn,
				"SSP_TEST_PLATFORM_ADMIN_PASSWORD":  cfg.AdminPassword,
				"SSP_TEST_PLATFORM_AUDIT_PASSWORD":  cfg.AuditPassword,
			}
			for name, v := range required {
				if v == "" {
					t.Skipf("%s must be set to run TestAccPlatformResource (full CRUD against a real SSPI appliance; deploys an actual instance, can take hours)", name)
				}
			}
			testAccRequireAllowDestroyExisting(t)
			testAccDeleteExistingPlatforms(t)
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPlatformDestroy,
		Steps: []resource.TestStep{
			// Create + Read: full precheck + deployment LCM workflow.
			{
				Config: testAccPlatformResourceConfig(cfg, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "provider_id", cfg.ProviderID),
					resource.TestCheckResourceAttr(resourceName, "display_name", cfg.DisplayName),
					resource.TestCheckResourceAttr(resourceName, "form_factor", cfg.FormFactor),
					resource.TestCheckResourceAttr(resourceName, "worker_count", cfg.WorkerCount),
					resource.TestCheckResourceAttr(resourceName, "controller_count", cfg.ControllerCount),
					resource.TestCheckResourceAttr(resourceName, "ingress_fqdn", cfg.IngressFqdn),
					resource.TestCheckResourceAttrSet(resourceName, "status"),
				),
			},
			// Import: re-read the same platform by ID and confirm the resulting
			// state matches, modulo write-only/never-returned attributes.
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"admin_password", "audit_password", "force_delete",
				},
			},
			// Update: flip preserve_addons, exercising the UPDATE_PRECHECK/UPDATE
			// LCM workflow (via the resource's _revision-aware PUT) without
			// touching form_factor-constrained fields like worker/controller
			// count or the RequiresReplace'd display_name.
			{
				Config: testAccPlatformResourceConfig(cfg, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "preserve_addons", "true"),
				),
			},
			// Re-applying the same config should be a no-op (idempotency check).
			{
				Config:   testAccPlatformResourceConfig(cfg, true),
				PlanOnly: true,
			},
		},
		// Destroy runs automatically after the last step; verified by CheckDestroy.
		// force_delete is intentionally left unset (false) here so Destroy
		// exercises the graceful teardown path, not the force_delete=true path.
	})
}

type platformAccConfig struct {
	ProviderID         string
	DisplayName        string
	FormFactor         string
	WorkerCount        string
	ControllerCount    string
	DatacenterID       string
	ClusterID          string
	ContentDatastoreID string
	StoragePolicyID    string
	Domain             string
	DNSServer          string
	NTPServer          string
	NetworkID          string
	PortgroupID        string
	DefaultGateway     string
	Subnet             string
	NodeIPPool         string
	ServiceIPPool      string
	IngressFqdn        string
	KafkaFqdn          string
	SspBundleID        string
	AdminPassword      string
	AuditPassword      string
}

func testAccPlatformResourceConfig(cfg platformAccConfig, preserveAddons bool) string {
	return fmt.Sprintf(`
resource "sspi_platform" "test" {
  provider_id  = %q
  display_name = %q
  form_factor  = %q
  ssp_type     = "ATP"

  worker_count     = %s
  controller_count = %s

  datacenter_id        = %q
  cluster_id           = %q
  content_datastore_id = %q
  storage_policy_id    = %q

  domain      = %q
  dns_servers = [%q]
  ntp_server  = %q

  network_id               = %q
  portgroup_id              = %q
  platform_default_gateway = %q
  platform_subnet           = %q
  node_ip_pool              = [%q]
  service_ip_pool           = [%q]

  ingress_fqdn = %q
  kafka_fqdn   = %q

  ssp_bundle_id = %q

  admin_password  = %q
  audit_password  = %q
  preserve_addons = %t
}
`,
		cfg.ProviderID, cfg.DisplayName, cfg.FormFactor,
		cfg.WorkerCount, cfg.ControllerCount,
		cfg.DatacenterID, cfg.ClusterID, cfg.ContentDatastoreID, cfg.StoragePolicyID,
		cfg.Domain, cfg.DNSServer, cfg.NTPServer,
		cfg.NetworkID, cfg.PortgroupID, cfg.DefaultGateway, cfg.Subnet,
		cfg.NodeIPPool, cfg.ServiceIPPool,
		cfg.IngressFqdn, cfg.KafkaFqdn,
		cfg.SspBundleID,
		cfg.AdminPassword, cfg.AuditPassword, preserveAddons,
	)
}

// testAccCheckPlatformDestroy confirms every sspi_platform left in state was
// actually torn down on the SSPI appliance (GET returns 404), not just
// dropped from Terraform state.
func testAccCheckPlatformDestroy(s *terraform.State) error {
	client, err := testAccAPIClient()
	if err != nil {
		return err
	}
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "sspi_platform" {
			continue
		}
		resp, err := client.GetPlatformConfigWithResponse(context.Background(), rs.Primary.ID)
		if err != nil {
			return fmt.Errorf("error checking sspi_platform %s was destroyed: %w", rs.Primary.ID, err)
		}
		if resp.StatusCode() != http.StatusNotFound {
			return fmt.Errorf("sspi_platform %s still exists on the SSPI appliance (HTTP %d)", rs.Primary.ID, resp.StatusCode())
		}
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
