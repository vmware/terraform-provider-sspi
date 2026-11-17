// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"context"
	"crypto/tls"
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
	userPasswordAllowEnvVar  = "SSP_TEST_ALLOW_USER_PASSWORD_RESET"
	userPasswordTargetEnvVar = "SSP_TEST_USER_PASSWORD_TARGET"
	userPasswordFirstEnvVar  = "SSP_TEST_USER_PASSWORD_FIRST"
	userPasswordSecondEnvVar = "SSP_TEST_USER_PASSWORD_SECOND"

	defaultUserPasswordTarget = "audit"
	defaultUserPasswordFirst  = "AccTest-P@ss1-2026!"
	defaultUserPasswordSecond = "AccTest-P@ss2-2026!"
)

func userPasswordEnvOrDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// TestAccUserPasswordResource_AdminReset exercises sspi_user_password's
// administrator reset path (username set, POST /sspi/iam/reset-password)
// against a REAL SSPI appliance.
//
// Impact: this permanently changes the password of a real local user. The
// provider cannot revert a password on destroy, and the appliance has no
// user-creation API, so the target must be an existing non-critical local
// user (default "audit", the Auditor account). The test:
//   - refuses to run unless SSP_TEST_ALLOW_USER_PASSWORD_RESET=true,
//   - refuses to target the account the provider itself authenticates as
//     (SSPI_USERNAME), since resetting it would break the provider's own
//     credentials mid-test,
//   - verifies each step by really logging in as the target user with the
//     new password (GET /sspi/iam/current-user-info), and confirms after
//     destroy that the last password is still active (destroy is a no-op).
//
// The target user is left with SSP_TEST_USER_PASSWORD_SECOND afterwards.
//
// Optional env vars: SSP_TEST_USER_PASSWORD_TARGET (default "audit"),
// SSP_TEST_USER_PASSWORD_FIRST / _SECOND (two distinct passwords that meet
// the appliance's password policy).
func TestAccUserPasswordResource_AdminReset(t *testing.T) {
	resourceName := "sspi_user_password.test"

	target := userPasswordEnvOrDefault(userPasswordTargetEnvVar, defaultUserPasswordTarget)
	first := userPasswordEnvOrDefault(userPasswordFirstEnvVar, defaultUserPasswordFirst)
	second := userPasswordEnvOrDefault(userPasswordSecondEnvVar, defaultUserPasswordSecond)

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if os.Getenv(userPasswordAllowEnvVar) != "true" {
				t.Skipf("%s must be set to \"true\" to run this test: it permanently changes the password of local user %q on the target SSPI appliance",
					userPasswordAllowEnvVar, target)
			}
			if strings.EqualFold(target, os.Getenv("SSPI_USERNAME")) {
				t.Fatalf("%s=%q is the account the provider authenticates as (SSPI_USERNAME); resetting it would break the provider's own credentials. Choose a different user",
					userPasswordTargetEnvVar, target)
			}
			if first == second {
				t.Fatalf("%s and %s must differ so the Update step is a real change", userPasswordFirstEnvVar, userPasswordSecondEnvVar)
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckUserPasswordRetainedAfterDestroy(target, second),
		Steps: []resource.TestStep{
			// Create: reset to the first password and prove it by logging in.
			{
				Config: testAccUserPasswordResetConfig(target, first),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", "pwd-reset-"+target),
					resource.TestCheckResourceAttr(resourceName, "username", target),
					resource.TestCheckResourceAttr(resourceName, "new_password", first),
					resource.TestCheckNoResourceAttr(resourceName, "current_password"),
					testAccCheckUserCanLogin(target, first),
				),
			},
			// Idempotency: same config produces an empty plan (Read preserves state).
			{
				Config:   testAccUserPasswordResetConfig(target, first),
				PlanOnly: true,
			},
			// Update: rotating new_password triggers another reset.
			{
				Config: testAccUserPasswordResetConfig(target, second),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", "pwd-reset-"+target),
					resource.TestCheckResourceAttr(resourceName, "new_password", second),
					testAccCheckUserCanLogin(target, second),
					testAccCheckUserCannotLogin(target, first),
				),
			},
			// Error path: resetting a user that does not exist is surfaced as an error.
			{
				Config:      testAccUserPasswordResetConfig("no-such-user-acc-test", first),
				ExpectError: regexp.MustCompile(`Error resetting password`),
			},
		},
		// Destroy runs automatically after the last step. CheckDestroy asserts
		// the reset was NOT reverted, matching the resource's documented no-op Delete.
	})
}

// TestAccUserPasswordResource_SelfChangeMissingCurrentPassword covers the
// self-service validation: without username, current_password is mandatory.
// The provider rejects this before sending any request, so it changes nothing
// on the appliance and needs no opt-in beyond a reachable, configured one.
func TestAccUserPasswordResource_SelfChangeMissingCurrentPassword(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "sspi_user_password" "test" {
  new_password = "Irrelevant-P@ss2026!"
}
`,
				ExpectError: regexp.MustCompile(`Missing current_password`),
			},
		},
	})
}

func testAccUserPasswordResetConfig(username, newPassword string) string {
	return fmt.Sprintf(`
resource "sspi_user_password" "test" {
  username     = %q
  new_password = %q
}
`, username, newPassword)
}

// testAccLoginStatus authenticates to the appliance as the given user via
// GET /sspi/iam/current-user-info and returns the HTTP status. It honours
// the same SSPI_HOST/SSPI_INSECURE/ALL_PROXY settings as the provider.
func testAccLoginStatus(username, password string) (int, error) {
	endpoint := os.Getenv("SSPI_HOST")
	if endpoint == "" {
		endpoint = os.Getenv("SSPI_ENDPOINT")
	}
	if endpoint == "" {
		return 0, fmt.Errorf("SSPI_HOST or SSPI_ENDPOINT must be set")
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

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint+"/sspi/iam/current-user-info", nil)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(username, password)
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func testAccCheckUserCanLogin(username, password string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		status, err := testAccLoginStatus(username, password)
		if err != nil {
			return fmt.Errorf("logging in as %q: %w", username, err)
		}
		if status != http.StatusOK {
			return fmt.Errorf("expected login as %q with the new password to succeed (HTTP 200), got HTTP %d", username, status)
		}
		return nil
	}
}

func testAccCheckUserCannotLogin(username, password string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		status, err := testAccLoginStatus(username, password)
		if err != nil {
			return fmt.Errorf("logging in as %q: %w", username, err)
		}
		if status != http.StatusUnauthorized {
			return fmt.Errorf("expected login as %q with the superseded password to fail (HTTP 401), got HTTP %d", username, status)
		}
		return nil
	}
}

// testAccCheckUserPasswordRetainedAfterDestroy asserts destroy did not revert
// the password: Delete is a documented no-op, so the last password set must
// still work once the resource is gone from state.
func testAccCheckUserPasswordRetainedAfterDestroy(username, password string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		for _, rs := range s.RootModule().Resources {
			if rs.Type == "sspi_user_password" {
				return fmt.Errorf("sspi_user_password %s still in state after destroy", rs.Primary.ID)
			}
		}
		return testAccCheckUserCanLogin(username, password)(s)
	}
}
