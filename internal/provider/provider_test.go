// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

// Package provider_test holds this provider's acceptance and unit tests.
//
// Acceptance test scope (honest statement, since the *_acc_test.go file
// names alone don't make this obvious): every TestAcc* test in this package
// today (TestAccPlatformDataSource, TestAccVsphereProviderDataSource,
// TestAccBundleDataSource) is a read-only lookup of a resource that already
// exists in whichever SSPI lab environment CI points at. None of them
// exercise a full Create→Update→Destroy lifecycle for any sspi_* resource.
//
// This is deliberate, not an oversight: sspi_platform (the resource with the
// most to gain from acceptance testing) provisions a real Kubernetes cluster
// on real vSphere infrastructure and can take 30-90 minutes per apply/destroy
// cycle (see the FSDD's async-operations risk note); sspi_vsphere_provider
// and sspi_installer_bundle_local likewise require, respectively, a real
// vCenter endpoint and a multi-GB package file. Full resource-CRUD
// acceptance testing for these is not implemented — it would require a
// dedicated, disposable SSPI lab environment and a CI budget this provider
// does not currently have, not a testing gap that's simply unaddressed.
// Resource-level correctness is instead covered by the mocked-HTTP unit
// tests (TestUnit*) in this package, which do exercise full CRUD against a
// fake server.
package provider_test

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/vmware/terraform-provider-sspi/internal/provider"
)

// testAccProtoV6ProviderFactories passes the provider factory into test assertions.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"sspi": providerserver.NewProtocol6WithError(provider.New("test")()),
}

func testAccPreCheck(t *testing.T) {
	if v := os.Getenv("SSPI_HOST"); v == "" {
		if v2 := os.Getenv("SSPI_ENDPOINT"); v2 == "" {
			t.Skip("SSPI_HOST or SSPI_ENDPOINT must be set for SSPI acceptance tests")
		}
	}
	if v := os.Getenv("SSPI_USERNAME"); v == "" {
		t.Skip("SSPI_USERNAME must be set for SSPI acceptance tests")
	}
	if v := os.Getenv("SSPI_PASSWORD"); v == "" {
		t.Skip("SSPI_PASSWORD must be set for SSPI acceptance tests")
	}
}
