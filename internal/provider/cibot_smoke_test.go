// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import "testing"

// TestUnitCIBotSmoke is a minimal, network-free check that the cibot
// pipeline (Jenkins and /test-all) can build this package and run it to
// completion. It exercises the same provider factory every TestAcc* test
// uses, without contacting a live SSPI appliance.
func TestUnitCIBotSmoke(t *testing.T) {
	factory, ok := testAccProtoV6ProviderFactories["sspi"]
	if !ok {
		t.Fatal(`expected "sspi" provider factory to be registered`)
	}

	server, err := factory()
	if err != nil {
		t.Fatalf("provider factory returned error: %v", err)
	}
	if server == nil {
		t.Fatal("provider factory returned a nil server")
	}
}
