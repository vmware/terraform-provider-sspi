# Testing the Terraform Provider for VMware SSPI

This guide covers running unit and acceptance tests for the **Terraform Provider for VMware Security Services
Platform Installer (SSPI)** (`terraform-provider-sspi`), setting up SOCKS5 proxy access for isolated testbeds,
and troubleshooting common testing and diagnostic issues.

For the companion SSP runtime provider's test suite, see
[`terraform-provider-ssp`'s testing guide](https://github.com/vmware/terraform-provider-ssp/blob/main/TESTING.md).

---

## Test Suites Overview

The provider test suite consists of two main types of tests:

1. **Unit Tests**: Test resource/datasource schema conversions, state mapping, and client request/response formatting against in-memory HTTP mock servers (`httptest.Server`). They execute quickly and do not require live infrastructure.
2. **Acceptance Tests**: Run real end-to-end Terraform CRUD lifecycle operations against a live SSPI appliance (`172.16.111.4`).

---

## Running Unit Tests

Unit tests execute locally and perform in-memory HTTP mocking without hitting external network endpoints.

Run all unit tests via `make`:

```shell
make test-unit
```

Or using standard `go test`:

```shell
go test -v ./internal/... -tags=unittest -count=1
```

*Note: The provider HTTP client transport automatically bypasses proxies for `127.0.0.1` and `localhost` to ensure unit test HTTP mock servers run without interference from `ALL_PROXY` settings.*

---

## Running Acceptance Tests

Acceptance tests require network access to a live **SSPI Appliance**.

### 1. Environment Variable Credentials

```shell
export SSPI_HOST="https://172.16.111.4"
export SSPI_USERNAME="admin"
export SSPI_PASSWORD="your-sspi-password"
export SSPI_INSECURE="true"
```

### 2. Testbed Resource ID Overrides

Acceptance tests for existing platform resources (data sources) attempt to look up live objects. If your testbed contains pre-provisioned bundles, platforms, or vCenter providers, override the default IDs using environment variables:

```shell
# Bundle ID existing on SSPI Depot (/sspi/bundles)
export SSP_TEST_BUNDLE_ID="6b4c85e8-930c-416f-a6da-0e25a96cf131"

# Deployed Platform ID existing on SSPI (/sspi/platforms)
export SSP_TEST_PLATFORM_ID="69a6df45-4fad-4acb-a6bc-040ce5668c90"

# Registered vCenter Provider details existing on SSPI (/sspi/providers)
export SSP_TEST_VSPHERE_PROVIDER_ID="5912b7ff-88bd-4599-9153-eb1218941ebc"
export SSP_TEST_VSPHERE_PROVIDER_SERVER="vxlan-vm-111-128.nimbus-tb.nimbus.internal"
export SSP_TEST_VSPHERE_PROVIDER_USER="ssp-op-712f9b5a@vsphere.local"
```

### 3. Executing Acceptance Tests

To enable acceptance testing, set `TF_ACC=1`:

```shell
make testacc
```

#### Running Specific Acceptance Tests

To run a specific test suite or test case, pass the `TESTARGS` variable:

```shell
make testacc TESTARGS="-run=TestAccPlatformResource"
```

To run a single test case directly with `go test`:

```shell
TF_ACC=1 go test -v ./internal/provider -run=TestAccVsphereProviderResource -timeout 5m
```

---

## SOCKS5 Proxy & Testbed Connectivity Setup

When running acceptance tests against isolated lab environments (e.g., Nimbus testbeds accessible only via a jump host), route HTTP traffic through an SSH SOCKS5 proxy tunnel.

### 1. Establishing the SSH SOCKS5 Tunnel

Open a SOCKS5 proxy on local port `9999` using the jump host:

```shell
ssh worker@<jump_host_ip> -D 9999 -N
```

### 2. Proxy Environment Variables (`socks5h://`)

Configure proxy environment variables before running acceptance tests. **Crucially, use `socks5h://` (with `h`) to mandate remote DNS resolution on the jump host:**

```shell
export ALL_PROXY="socks5h://127.0.0.1:9999"
export HTTPS_PROXY="socks5h://127.0.0.1:9999"
export HTTP_PROXY="socks5h://127.0.0.1:9999"
export NO_PROXY="127.0.0.1,localhost,::1"
```

---

## Troubleshooting & Testing Diagnosis Guide

| Symptoms / Error Message | Root Cause | Resolution / Fix |
| --- | --- | --- |
| `dial tcp: lookup <hostname>: no such host` | Using standard `socks5://` scheme causing local DNS lookup for internal domain names (e.g. `.nimbus.internal`). | Change proxy protocol scheme to `socks5h://` (e.g., `ALL_PROXY="socks5h://127.0.0.1:9999"`). The `h` suffix forces DNS resolution via the remote SOCKS proxy. |
| `socks connect tcp 127.0.0.1:9999->127.0.0.1:XXXXX: dial tcp ... connection refused` | `ALL_PROXY` environment variable routing local `httptest.Server` unit test calls into the SOCKS proxy. | Ensure `export NO_PROXY="127.0.0.1,localhost"` is set. The provider client automatically disables proxying for `127.0.0.1` and `localhost` targets. |
| `Unexpected response status code: 404` in `TestAccBundleDataSource`, `TestAccPlatformResource`, or `TestAccVsphereProviderResource` | Hardcoded fallback UUIDs in the test suite do not exist on the current live testbed snapshot. | Query the live SSPI appliance API (`/sspi/bundles`, `/sspi/platforms`, `/sspi/providers`) using `curl` and export `SSP_TEST_BUNDLE_ID`, `SSP_TEST_PLATFORM_ID`, and `SSP_TEST_VSPHERE_PROVIDER_ID`. |
| `ssh: connect to host ... port 22: Operation timed out` | Jump host IP or port 22 is blocked by local firewall or VPN routing. | Verify network route to jump host and ensure corporate VPN or host route is active. |

---

## Querying Live Testbed Objects via SOCKS Proxy

To inspect existing resources on a live testbed to obtain valid IDs for acceptance tests:

```shell
# Query SSPI Bundles
curl -s -x socks5h://127.0.0.1:9999 -k -u admin:<password> https://<sspi_ip>/sspi/bundles

# Query SSPI Platforms
curl -s -x socks5h://127.0.0.1:9999 -k -u admin:<password> https://<sspi_ip>/sspi/platforms

# Query SSPI Registered vCenter Providers
curl -s -x socks5h://127.0.0.1:9999 -k -u admin:<password> https://<sspi_ip>/sspi/providers
```
