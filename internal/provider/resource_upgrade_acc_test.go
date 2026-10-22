// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"golang.org/x/net/proxy"
)

const (
	upgradeAllowEnvVar     = "SSP_TEST_ALLOW_UPGRADE"
	upgradeBundleURLEnvVar = "SSP_TEST_UPGRADE_BUNDLE_URL"
)

// TestAccUpgradeWorkflow exercises the end-to-end SSPI appliance upgrade
// (sspi_installer_bundle_remote -> sspi_upgrade_manager -> sspi_upgrade with
// run_prechecks = true) against a REAL SSPI appliance.
//
// Impact: this upgrades the appliance under test to the version in the
// bundle, and the upgrade cannot be undone. It also reboots the appliance
// and takes about 1 to 2 hours (about 55 minutes on a healthy testbed), so
// run it with a long go test timeout (e.g. -timeout 4h) and, ideally, from a
// host with direct access to the appliance inside tmux rather than from a
// laptop over a SOCKS tunnel. Use a disposable testbed only. The test:
//   - refuses to run unless SSP_TEST_ALLOW_UPGRADE=true,
//   - requires SSP_TEST_UPGRADE_BUNDLE_URL, an HTTP(S) URL of an upgrade
//     bundle (.sub) the appliance itself can download without authentication,
//   - confirms against the appliance (GET /sspi/upgrade/status and
//     /sspi/upgrade/manager/status) that both upgrades really reached
//     SUCCESS, rather than trusting Terraform state alone,
//   - confirms the follow-up plan is empty.
//
// Destroy is a no-op for sspi_upgrade_manager and sspi_upgrade (the API has
// no delete), so CheckDestroy only asserts they left Terraform state; the
// appliance stays on the upgraded version.
func TestAccUpgradeWorkflow(t *testing.T) {
	const (
		bundleName  = "sspi_installer_bundle_remote.upgrade_bundle"
		managerName = "sspi_upgrade_manager.this"
		upgradeName = "sspi_upgrade.this"
	)

	url := os.Getenv(upgradeBundleURLEnvVar)

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if os.Getenv(upgradeAllowEnvVar) != "true" {
				t.Skipf("%s must be set to \"true\" to run this test: it irreversibly upgrades (and reboots) the target SSPI appliance (SSPI_HOST)",
					upgradeAllowEnvVar)
			}
			if url == "" {
				t.Skipf("%s must be set to the URL of an SSPI upgrade bundle (.sub) the appliance can download", upgradeBundleURLEnvVar)
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckUpgradeResourcesRemoved,
		Steps: []resource.TestStep{
			// Create: import the bundle, upgrade the Upgrade Manager, then run
			// pre-checks and the upgrade, waiting for each to settle.
			{
				Config: testAccUpgradeWorkflowConfig(url),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(bundleName, "status", "READY"),
					resource.TestCheckResourceAttrSet(bundleName, "id"),
					resource.TestCheckResourceAttrSet(bundleName, "version"),

					resource.TestCheckResourceAttr(managerName, "id", "upgrade-manager"),
					resource.TestCheckResourceAttr(managerName, "status", "SUCCESS"),
					resource.TestCheckResourceAttr(managerName, "progress", "100"),
					// target_version was not configured, so it must be resolved
					// from the single staged package, i.e. the bundle's version.
					resource.TestCheckResourceAttrPair(managerName, "target_version", bundleName, "version"),

					resource.TestCheckResourceAttr(upgradeName, "id", "upgrade"),
					resource.TestCheckResourceAttr(upgradeName, "run_prechecks", "true"),
					resource.TestMatchResourceAttr(upgradeName, "status", regexp.MustCompile(`^SUCCESS(_WITH_WARNINGS)?$`)),
					resource.TestCheckResourceAttr(upgradeName, "progress", "100"),
					resource.TestCheckResourceAttrSet(upgradeName, "current_version"),
					resource.TestCheckResourceAttrSet(upgradeName, "target_version"),

					testAccCheckApplianceUpgradeSucceeded,
				),
			},
			// Idempotency: re-planning the same config must be a no-op. This
			// is also what guards against the resource being left tainted or
			// re-running pre-checks against an already upgraded appliance.
			{
				Config:   testAccUpgradeWorkflowConfig(url),
				PlanOnly: true,
			},
		},
	})
}

// TestAccUpgradeManagerResource_NoPackageStaged covers the validation in
// sspi_upgrade_manager: with target_version unset and nothing staged on the
// appliance, the resource fails before issuing any upgrade call. It changes
// nothing on the appliance, so it only needs a reachable one, and it skips
// itself if a package happens to be staged (or the manager was already
// upgraded and the resource would simply adopt that state).
func TestAccUpgradeManagerResource_NoPackageStaged(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			staged, upgraded, err := testAccApplianceUpgradeState()
			if err != nil {
				t.Fatalf("reading appliance upgrade state: %s", err)
			}
			if staged != 0 || upgraded {
				t.Skipf("appliance has %d staged upgrade package(s) / manager already upgraded=%t; this test needs an appliance with none staged and the manager not yet upgraded",
					staged, upgraded)
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "sspi_upgrade_manager" "this" {}
`,
				ExpectError: regexp.MustCompile(`no upgrade package is staged`),
			},
		},
	})
}

func testAccUpgradeWorkflowConfig(url string) string {
	return fmt.Sprintf(`
resource "sspi_installer_bundle_remote" "upgrade_bundle" {
  url = %q
}

resource "sspi_upgrade_manager" "this" {
  depends_on = [sspi_installer_bundle_remote.upgrade_bundle]
}

resource "sspi_upgrade" "this" {
  run_prechecks = true
  depends_on    = [sspi_upgrade_manager.this]
}
`, url)
}

// testAccApplianceUpgradeState returns the number of staged upgrade packages
// and whether the Upgrade Manager has already been upgraded successfully.
func testAccApplianceUpgradeState() (staged int, managerUpgraded bool, err error) {
	ctx := context.Background()

	pkgs, err := testAccGetJSON(ctx, "/sspi/upgrade/packages")
	if err != nil {
		return 0, false, err
	}
	if results, ok := pkgs["results"].([]any); ok {
		staged = len(results)
	}

	mgr, err := testAccGetJSON(ctx, "/sspi/upgrade/manager/status")
	if err != nil {
		return 0, false, err
	}
	s, _ := mgr["overall_status"].(string)
	return staged, s == "SUCCESS" || s == "SUCCESS_WITH_WARNINGS", nil
}

// testAccCheckApplianceUpgradeSucceeded asks the appliance directly whether
// both the Upgrade Manager upgrade and the appliance upgrade finished, so a
// bug that records SUCCESS in state without the appliance agreeing is caught.
func testAccCheckApplianceUpgradeSucceeded(_ *terraform.State) error {
	ctx := context.Background()

	for _, path := range []string{"/sspi/upgrade/manager/status", "/sspi/upgrade/status"} {
		body, err := testAccGetJSON(ctx, path)
		if err != nil {
			return err
		}
		status, _ := body["overall_status"].(string)
		if status != "SUCCESS" && status != "SUCCESS_WITH_WARNINGS" {
			return fmt.Errorf("GET %s: expected overall_status SUCCESS or SUCCESS_WITH_WARNINGS, got %q", path, status)
		}
	}
	return nil
}

func testAccCheckUpgradeResourcesRemoved(s *terraform.State) error {
	for _, rs := range s.RootModule().Resources {
		switch rs.Type {
		case "sspi_upgrade", "sspi_upgrade_manager":
			return fmt.Errorf("%s %s still in state after destroy", rs.Type, rs.Primary.ID)
		}
	}
	return nil
}

// testAccGetJSON issues an authenticated GET against the appliance
// (SSPI_HOST, SSPI_USERNAME, SSPI_PASSWORD, SSPI_INSECURE and ALL_PROXY, as
// the provider reads them) and decodes the JSON body. The upgrade endpoints
// are not part of the raw api_client used by the other helpers, hence plain
// net/http.
func testAccGetJSON(ctx context.Context, path string) (map[string]any, error) {
	endpoint := os.Getenv("SSPI_HOST")
	if endpoint == "" {
		endpoint = os.Getenv("SSPI_ENDPOINT")
	}
	if endpoint == "" {
		return nil, fmt.Errorf("SSPI_HOST or SSPI_ENDPOINT must be set")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}
	endpoint = strings.TrimRight(endpoint, "/")

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: os.Getenv("SSPI_INSECURE") == "true",
			MinVersion:         tls.VersionTLS12,
		},
	}
	if !strings.HasPrefix(endpoint, "http://127.0.0.1") && !strings.HasPrefix(endpoint, "http://localhost") {
		if cd, ok := proxy.FromEnvironment().(proxy.ContextDialer); ok {
			transport.DialContext = cd.DialContext
		}
	}
	httpClient := &http.Client{Timeout: 30 * time.Second, Transport: transport}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+path, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(os.Getenv("SSPI_USERNAME"), os.Getenv("SSPI_PASSWORD"))
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: unexpected status %d", path, resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("GET %s: decoding response: %w", path, err)
	}
	return out, nil
}
