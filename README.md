# Terraform Provider for VMware SSPI (Security Services Platform Installer)

This is the official Terraform provider for the **VMware Security Services Platform Installer (SSPI)**
(`terraform-provider-sspi`) — the Day-0/Day-1 bootstrap and lifecycle-management appliance for
[VMware Security Services Platform (SSP)](https://github.com/vmware/terraform-provider-ssp).

This provider (`provider "sspi"`) automates the SSPI appliance:

1. **vCenter Provider Registration**: registering vCenter target providers (`sspi_vsphere_provider`) for SSP/Avi Operations cluster deployment.
2. **Software Package Management**: uploading `.tar.gz` software packages to the SSPI Depot (`sspi_installer_bundle_local`).
3. **Cluster Deployment & Lifecycle**: pre-deployment validation, provisioning SSP (`ATP`) and Avi Operations clusters, scaling worker nodes/form factors, updating IP pools, and teardown (`sspi_platform`).
4. **Identity & Access**: LDAP identity source integration (`sspi_ldap_identity_source`) and local user password management (`sspi_user_password`).
5. **Backup/Restore**: SSPI appliance backup target configuration and recurring backup schedules (`sspi_installer_backup_config`, `sspi_installer_recurring_backup_config`).

For Day-2 operational workflows against a **deployed** SSP cluster (site onboarding, security feature
activation, runtime backup/restore, in-place upgrades), see the companion
[`terraform-provider-ssp`](https://github.com/vmware/terraform-provider-ssp) provider — the two are
designed to be used together within the same root module. See the
[FSDD](https://github.com/vmware/terraform-provider-ssp/blob/main/FSDD.md) for the full
architecture and the cross-provider dependency model.

---

## Requirements

| Dependency | Version |
| --- | --- |
| [Terraform](https://developer.hashicorp.com/terraform/downloads) | ≥ 1.5 |
| [Go](https://go.dev/dl/) | ≥ 1.21 (only for building from source) |
| SSPI Appliance | Accessible over HTTPS (`port 443`), admin credentials available |

---

## Installation

Add the provider to your `required_providers` block:

```hcl
terraform {
  required_providers {
    sspi = {
      source  = "registry.terraform.io/vmware/sspi"
      version = "1.0.0"
    }
  }
  required_version = ">= 1.5"
}
```

Then run:

```shell
terraform init
```

---

## Authentication & Provider Configuration

```shell
export SSPI_HOST="https://sspi.corp.local"
export SSPI_USERNAME="admin"
export SSPI_PASSWORD="your-sspi-password"
```

```hcl
provider "sspi" {
  host     = "https://sspi.corp.local"   # env: SSPI_HOST
  username = "admin"                     # env: SSPI_USERNAME
  password = var.sspi_password           # env: SSPI_PASSWORD
  insecure = true                        # set true to skip TLS verification (labs)
}
```

**Required role:** the configured account must hold the `enterprise_admin` role. Nearly every
write operation (create/update/delete on any `sspi_*` resource) requires `enterprise_admin`, alone
or paired with `auditor`; an `auditor`-only or `security_op`-only account can read via the
`data.sspi_*` data sources but will fail with an HTTP 403 on the first resource write. On
`Configure()`, the provider calls `GET /sspi/iam/current-user-info` and emits a warning diagnostic
if the account's roles don't include `enterprise_admin`, so a role mismatch surfaces at
`terraform plan` time instead of as an unexplained 403 mid-`apply`.

---

## Example HCL

```hcl
terraform {
  required_providers {
    sspi = {
      source  = "registry.terraform.io/vmware/sspi"
      version = "1.0.0"
    }
  }
}

provider "sspi" {
  host     = "https://sspi.corp.local"
  username = "admin"
  password = var.sspi_password
  insecure = true
}

resource "sspi_vsphere_provider" "vc01" {
  server      = "vc01.corp.local"
  user        = "administrator@vsphere.local"
  password    = var.vc_password
  certificate = var.vc_certificate
}

resource "sspi_installer_bundle_local" "ssp_pkg" {
  file_path = "/tmp/ssp-platform-5.2.0.tar.gz"
}

resource "sspi_platform" "ssp_cluster" {
  provider_id          = sspi_vsphere_provider.vc01.id
  display_name         = "Production-SSP-Cluster"
  ssp_type             = "ATP"
  form_factor          = "MEDIUM" # real enum: COMPACT | MEDIUM | LARGE | EXTRA_LARGE
  worker_count         = 3
  domain               = "corp.local"
  datacenter_id        = "datacenter-1"
  cluster_id           = "domain-c1"
  content_datastore_id = "datastore-1"
  network_id           = "dvportgroup-1"
  storage_policy_id    = var.vsan_storage_policy_id
  platform_default_gateway = "10.0.0.1"
  platform_subnet          = "10.0.0.0/24"
  dns_servers          = ["10.0.0.10"]
  ntp_server           = "ntp.corp.local"
  node_ip_pool         = ["172.16.111.50-172.16.111.60"]
  service_ip_pool      = ["172.16.111.70-172.16.111.80"]
  ingress_fqdn         = "ssp-cluster.corp.local"
  kafka_fqdn           = "ssp-cluster-kafka.corp.local"
  ssp_bundle_id        = sspi_installer_bundle_local.ssp_pkg.id
  admin_password       = var.ssp_admin_password
  audit_password       = var.ssp_audit_password
}
```

---

## Resources & Data Sources

| Resource | Description |
| --- | --- |
| `sspi_vsphere_provider` | Registers and manages vCenter target provider endpoints (`/sspi/providers`) |
| `sspi_installer_bundle_local` | Uploads `.tar.gz` software packages into SSPI Depot (`/sspi/bundles`) |
| `sspi_platform` | Provision, scale, reconfigure, and teardown SSP or Avi Operations clusters (`/sspi/platforms`) |
| `sspi_installer_backup_config` | Configures SSPI appliance SFTP backup target (`/sspi/backup/config`) |
| `sspi_installer_recurring_backup_config` | Configures SSPI appliance recurring backup schedule (`/sspi/backup/recurring/config`) |
| `sspi_ldap_identity_source` | Configures SSPI appliance LDAP directory integration (`/sspi/iam/ldap-identity-sources`) |
| `sspi_user_password` | Manages local user account password resets and changes (`/sspi/iam/*`) |

| Data Source | Description |
| --- | --- |
| `data.sspi_vsphere_provider` | Fetches details of a registered vCenter provider |
| `data.sspi_platform` | Reads SSPI platform configuration and status |
| `data.sspi_bundle` | Reads SSPI Depot package bundle metadata |

---

## Building from Source

```shell
make build
```

To run unit tests:

```shell
make test
```

To install the plugin locally into `~/.terraform.d/plugins`:

```shell
make install
```
