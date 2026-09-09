# Building the Terraform Provider for VMware SSPI

Instructions for building and developing the **Terraform Provider for VMware Security Services Platform Installer (SSPI)** (`terraform-provider-sspi`).

---

## Requirements

Before building the provider, ensure you have installed:

- [Go](https://go.dev/dl/) ≥ 1.21 (1.25+ recommended)
- [Terraform](https://developer.hashicorp.com/terraform/downloads) ≥ 1.5 (for local testing with HCL configurations)
- [Git](https://git-scm.com/)

---

## Building the Provider Binary

1. Clone the repository:

   ```shell
   git clone https://github.com/vmware/terraform-provider-sspi.git
   cd terraform-provider-sspi
   ```

2. Build the provider using `make`:

   ```shell
   make build
   ```

   This outputs the compiled binary to the `dist/` directory: `dist/terraform-provider-sspi`.

   Alternatively, build directly using Go:

   ```shell
   go build -o terraform-provider-sspi .
   ```

---

## Development Overrides (`~/.terraformrc`)

To test local provider builds with Terraform CLI without publishing to a registry, configure **Development Overrides** in your `~/.terraformrc` file (or `%APPDATA%\terraform.rc` on Windows):

```hcl
provider_installation {
  dev_overrides {
    "registry.terraform.io/vmware/sspi" = "/Users/<your-username>/go/src/github.com/terraform-providers/terraform-provider-sspi/dist"
  }

  direct {}
}
```

Replace the directory path with the absolute path to your local output directory containing the built `terraform-provider-sspi` binary.

---

## Installing the Plugin Locally

To install the plugin binary into your local user plugin directory (`~/.terraform.d/plugins`):

```shell
make install
```
