# Releasing terraform-provider-sspi

This document describes how versions of this provider are built, signed, released on GitHub, and
published to the [Terraform Registry](https://registry.terraform.io/providers/vmware/sspi).

## Versioning

Releases follow [Semantic Versioning](https://semver.org/) and are identified by a Git tag of the
form:

- `vX.Y.Z` for stable releases (e.g. `v1.2.3`)
- `vX.Y.Z-<label>` for pre-releases (e.g. `v1.2.3-beta`, `v1.2.3-rc.1`)

Only tags on these two patterns trigger a release — see `.github/workflows/release.yml`.

## What happens when you push a tag

1. `.github/workflows/release.yml` fires on `push: tags: ['v*.*.*', 'v*.*.*-*']`.
2. It checks out the full history (`fetch-depth: 0`, required by GoReleaser for changelog
   generation), sets up Go, and runs the golangci-lint composite action as a release gate.
3. It imports the signing key from the `GPG_PRIVATE_KEY` / `GPG_PASSPHRASE` repository secrets via
   [`crazy-max/ghaction-import-gpg`](https://github.com/crazy-max/ghaction-import-gpg).
4. It runs [GoReleaser](https://goreleaser.com/) (`.goreleaser.yml`), which:
   - Builds binaries for `linux/windows/darwin/freebsd` × `amd64/386/arm/arm64` (see `builds:`).
   - Packages each as a `.zip` archive named `terraform-provider-sspi_<version>_<os>_<arch>.zip`.
   - Produces a `terraform-provider-sspi_<version>_SHA256SUMS` checksum file.
   - Attaches a copy of `terraform-registry-manifest.json` to the release, renamed to
     `terraform-provider-sspi_<version>_manifest.json` — this is what tells the registry which
     Terraform plugin protocol version(s) this provider supports.
   - **GPG-signs** the checksum file, producing `..._SHA256SUMS.sig`. This signature is what the
     Terraform Registry verifies against the public key on file for the `vmware` publisher account
     before it will serve a release to `terraform init`.

## Important: the GitHub release is created as a draft

`.goreleaser.yml` sets `release: draft: true`. This is deliberate — it gives a human a chance to
review the generated changelog and assets before they go live. **This means every release needs a
manual step**: after the workflow finishes, go to the repo's
[Releases page](https://github.com/vmware/terraform-provider-sspi/releases), open the new draft,
review it, and click **Publish release**.

The Terraform Registry only ingests a version once its GitHub release is published (non-draft).
Skipping this step is the most common reason a tag "doesn't show up" on the registry.

## One-time setup: registering signing secrets

Each repo needs its own `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` secrets (Settings → Secrets and
variables → Actions). These are **not** inherited automatically from other repos unless configured
as GitHub organization-level secrets shared with this repo. Recommended: reuse the same GPG key
already registered for the `vmware` publisher account on the Terraform Registry (the one used for
`terraform-provider-nsxt` and other vmware providers), rather than minting a new key per provider —
one key per publisher account is simpler to manage.

## One-time setup: publishing to the Terraform Registry

This only needs to happen once per provider (not per release):

1. Sign in to [registry.terraform.io](https://registry.terraform.io) as (or with access to) the
   `vmware` publisher organization.
2. Install/grant the Terraform Registry's GitHub App access to the
   `vmware/terraform-provider-sspi` repository (a `vmware` org owner action).
3. Go to **Publish → Provider**, select this repository.
4. Confirm the GPG public key matches the private key configured in step "signing secrets" above.
   If it's a new key, upload the ASCII-armored public key to the account first.
5. Click **Publish**. The registry will then automatically ingest every future published release
   whose tag matches `vX.Y.Z` — no need to repeat this process for subsequent versions.

## Maintainer release checklist

1. Ensure `main` is green (all required PR checks passing).
2. Update `CHANGELOG.md` with the notable changes for this version.
3. Tag: `git tag vX.Y.Z && git push origin vX.Y.Z`.
4. Watch the `Release` workflow run to completion.
5. Open the repo's Releases page and **publish the draft** GoReleaser created.
6. Verify the version appears at
   `https://registry.terraform.io/v1/providers/vmware/sspi/versions` (may take a few minutes).
7. Run `terraform init -upgrade` against a test configuration pinned to the new version to confirm
   it installs cleanly.
