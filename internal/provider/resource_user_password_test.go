// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// userPasswordMockAPI simulates the subset of the SSPI appliance IAM API
// (POST /sspi/iam/reset-password, POST /sspi/iam/change-password) needed to
// drive ssp_user_password through Create without a live SSPI appliance.
type userPasswordMockAPI struct {
	mu sync.Mutex
}

func newUserPasswordMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	m := &userPasswordMockAPI{}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sspi/iam/reset-password", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /sspi/iam/change-password", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUnitUserPasswordAdminResetConfig(host string) string {
	return testUnitSSPIProviderConfig(host) + `
resource "sspi_user_password" "test" {
  username     = "admin"
  new_password = "N3wP@ssw0rd!"
}
`
}

func testUnitUserPasswordSelfChangeConfig(host string) string {
	return testUnitSSPIProviderConfig(host) + `
resource "sspi_user_password" "test" {
  current_password = "Old-P@ssw0rd!"
  new_password      = "N3wP@ssw0rd!"
}
`
}

// TestUnitUserPasswordResource_AdminReset exercises ssp_user_password's
// administrator password reset path (username set) against a mocked SSPI
// appliance API.
func TestUnitUserPasswordResource_AdminReset(t *testing.T) {
	srv := newUserPasswordMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitUserPasswordAdminResetConfig(srv.URL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_user_password.test", "id", "pwd-reset-admin"),
					resource.TestCheckResourceAttr("sspi_user_password.test", "username", "admin"),
				),
			},
		},
	})
}

// TestUnitUserPasswordResource_SelfChange exercises ssp_user_password's
// self-service password change path (username unset) against a mocked SSPI
// appliance API.
func TestUnitUserPasswordResource_SelfChange(t *testing.T) {
	srv := newUserPasswordMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitUserPasswordSelfChangeConfig(srv.URL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_user_password.test", "id", "pwd-change"),
				),
			},
		},
	})
}
