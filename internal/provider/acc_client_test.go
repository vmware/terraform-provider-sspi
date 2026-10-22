// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
	"github.com/vmware/terraform-provider-sspi/internal/client/depot_client"
	"github.com/vmware/terraform-provider-sspi/internal/client/iam_client"
)

// testAccAPIClient builds a raw SSPI API client from the same SSPI_HOST/
// SSPI_ENDPOINT, SSPI_USERNAME, SSPI_PASSWORD, and SSPI_INSECURE environment
// variables the provider itself reads (see provider.go's Configure), for use
// in CheckDestroy funcs that need to confirm a resource was actually removed
// from the appliance rather than just dropped from Terraform state. Also
// mirrors provider.go's ALL_PROXY/SOCKS dialing so it can reach a lab
// appliance behind an SSH SOCKS proxy the same way the provider itself does.
func testAccAPIClient() (*api_client.ClientWithResponses, error) {
	endpoint, httpClient, authEditor, err := testAccHTTPSetup()
	if err != nil {
		return nil, err
	}
	return api_client.NewClientWithResponses(
		endpoint,
		api_client.WithHTTPClient(httpClient),
		api_client.WithRequestEditorFn(authEditor),
	)
}

// testAccIAMClient is testAccAPIClient's counterpart for the IAM API
// (/sspi/iam), used by the LDAP identity source and role binding CheckDestroy funcs.
func testAccIAMClient() (*iam_client.ClientWithResponses, error) {
	endpoint, httpClient, authEditor, err := testAccHTTPSetup()
	if err != nil {
		return nil, err
	}
	return iam_client.NewClientWithResponses(
		endpoint,
		iam_client.WithHTTPClient(httpClient),
		iam_client.WithRequestEditorFn(authEditor),
	)
}

// testAccDepotClient is testAccAPIClient's counterpart for the Depot API
// (/sspi/bundles), used by the bundle resources' CheckDestroy funcs.
func testAccDepotClient() (*depot_client.ClientWithResponses, error) {
	endpoint, httpClient, authEditor, err := testAccHTTPSetup()
	if err != nil {
		return nil, err
	}
	return depot_client.NewClientWithResponses(
		endpoint,
		depot_client.WithHTTPClient(httpClient),
		depot_client.WithRequestEditorFn(authEditor),
	)
}

func testAccHTTPSetup() (string, *http.Client, func(context.Context, *http.Request) error, error) {
	endpoint := os.Getenv("SSPI_HOST")
	if endpoint == "" {
		endpoint = os.Getenv("SSPI_ENDPOINT")
	}
	if endpoint == "" {
		return "", nil, nil, fmt.Errorf("SSPI_HOST or SSPI_ENDPOINT must be set")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}
	endpoint = strings.TrimRight(endpoint, "/")

	username := os.Getenv("SSPI_USERNAME")
	password := os.Getenv("SSPI_PASSWORD")

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

	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	authEditor := func(ctx context.Context, req *http.Request) error {
		req.SetBasicAuth(username, password)
		return nil
	}
	return endpoint, httpClient, authEditor, nil
}

// SSP_TEST_ALLOW_DESTROY_EXISTING gates the destructive pre-test cleanup in
// testAccDeleteExistingPlatforms/testAccDeleteExistingVsphereProviders: every
// real SSPI appliance we've tried this against comes pre-seeded with a
// vCenter provider and a deployed platform (see TestAccPlatformResource's
// and TestAccVsphereProviderResource's doc comments for why that blocks
// those tests otherwise), so both tests delete whatever they find before
// creating their own. That is destructive to state neither test created,
// so it requires this explicit opt-in rather than running unconditionally.
const allowDestroyExistingEnvVar = "SSP_TEST_ALLOW_DESTROY_EXISTING"

func testAccRequireAllowDestroyExisting(t *testing.T) {
	t.Helper()
	if os.Getenv(allowDestroyExistingEnvVar) != "true" {
		t.Skipf("%s must be set to \"true\" to run this test: it deletes any pre-existing sspi_platform/sspi_vsphere_provider "+
			"on the target SSPI appliance (SSPI_HOST) before creating its own", allowDestroyExistingEnvVar)
	}
}

// testAccDeleteExistingPlatforms deletes every sspi_platform already present
// on the target appliance and waits for each to be fully torn down (this can
// take as long as a normal Delete - up to platformPollTimeout in the real
// resource, so it's given the same generous budget here).
func testAccDeleteExistingPlatforms(t *testing.T) {
	t.Helper()
	client, err := testAccAPIClient()
	if err != nil {
		t.Fatalf("building API client to clean up pre-existing platforms: %s", err)
	}
	ctx := context.Background()

	listResp, err := client.GetAllPlatformsWithResponse(ctx, &api_client.GetAllPlatformsParams{})
	if err != nil {
		t.Fatalf("listing existing platforms: %s", err)
	}
	if listResp.JSON200 == nil || listResp.JSON200.Configs == nil {
		return
	}

	for _, p := range *listResp.JSON200.Configs {
		if p.Id == nil {
			continue
		}
		id := *p.Id
		t.Logf("%s=true: deleting pre-existing sspi_platform %s (display_name=%q) to free ESXi/IPAM capacity for this test",
			allowDestroyExistingEnvVar, id, p.Service.InstanceName)

		delResp, err := client.DeletePlatformWithResponse(ctx, id, &api_client.DeletePlatformParams{})
		if err != nil {
			t.Fatalf("deleting pre-existing platform %s: %s", id, err)
		}
		switch delResp.StatusCode() {
		case http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound:
		default:
			t.Fatalf("deleting pre-existing platform %s: unexpected status %d: %s", id, delResp.StatusCode(), string(delResp.Body))
		}

		deadline := time.Now().Add(platformPollTimeoutForTests)
		for {
			getResp, err := client.GetPlatformConfigWithResponse(ctx, id)
			if err != nil {
				t.Fatalf("polling deletion of pre-existing platform %s: %s", id, err)
			}
			if getResp.StatusCode() == http.StatusNotFound {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out after %s waiting for pre-existing platform %s to be deleted", platformPollTimeoutForTests, id)
			}
			time.Sleep(15 * time.Second)
		}
	}
}

// testAccDeleteExistingVsphereProviders deletes every sspi_vsphere_provider
// already registered on the target appliance and waits for each to be fully
// gone. The appliance enforces one provider per vCenter server, so a
// pre-existing registration would otherwise make TestAccVsphereProviderResource's
// Create fail with a 409 before ever exercising the resource. Callers should
// delete platforms first (testAccDeleteExistingPlatforms) since a platform
// can depend on a provider registration.
func testAccDeleteExistingVsphereProviders(t *testing.T) {
	t.Helper()
	client, err := testAccAPIClient()
	if err != nil {
		t.Fatalf("building API client to clean up pre-existing vSphere providers: %s", err)
	}
	ctx := context.Background()

	listResp, err := client.GetAllVsphereProvidersWithResponse(ctx, &api_client.GetAllVsphereProvidersParams{})
	if err != nil {
		t.Fatalf("listing existing vSphere providers: %s", err)
	}
	if listResp.JSON200 == nil || listResp.JSON200.Configs == nil {
		return
	}

	for _, p := range *listResp.JSON200.Configs {
		if p.Id == nil {
			continue
		}
		id := *p.Id
		t.Logf("%s=true: deleting pre-existing sspi_vsphere_provider %s (server=%q) to free it up for this test",
			allowDestroyExistingEnvVar, id, p.Server)

		delResp, err := client.DeleteVsphereProviderWithResponse(ctx, id)
		if err != nil {
			t.Fatalf("deleting pre-existing vSphere provider %s: %s", id, err)
		}
		switch delResp.StatusCode() {
		case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		default:
			t.Fatalf("deleting pre-existing vSphere provider %s: unexpected status %d: %s", id, delResp.StatusCode(), string(delResp.Body))
		}

		deadline := time.Now().Add(10 * time.Minute)
		for {
			getResp, err := client.GetVsphereProviderWithResponse(ctx, id)
			if err != nil {
				t.Fatalf("polling deletion of pre-existing vSphere provider %s: %s", id, err)
			}
			if getResp.StatusCode() == http.StatusNotFound {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for pre-existing vSphere provider %s to be deleted", id)
			}
			time.Sleep(5 * time.Second)
		}
	}
}

// platformPollTimeoutForTests mirrors the resource_platform package's own
// platformPollTimeout (90 minutes in production). It's redeclared here
// rather than imported since that identifier lives in an internal package
// this test binary doesn't import.
const platformPollTimeoutForTests = 90 * time.Minute
