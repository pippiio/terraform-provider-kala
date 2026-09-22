# Releasing

How a version of this provider gets built, signed, and published, and how to
verify one you have downloaded.

The provider is not on the public registry yet; README covers how to install it
from a filesystem mirror in the meantime. This document covers the release
mechanics themselves, which are the same either way, and what publishing to the
registry will require once the first signed release exists.

---

## Cutting a release

Tags drive releases, and this repository constrains them:

- Tags must match `v[0-9]*` (the *Enforce tag naming* ruleset).
- Tag creation is restricted to specific actors (*Create tag actors*).
- Tags cannot be moved or deleted once pushed (*Immutable tags*) — a bad release
  needs a new version, never a retag. There is no bypass actor on that rule, so
  this applies to everyone including administrators: never push a tag to
  rehearse something.

```bash
git tag v0.1.0
git push origin v0.1.0
```

`.github/workflows/release.yml` then re-runs fmt, lint, and tests against the
tagged commit — a tag can point at any commit, including one that never passed
CI — before building every platform with GoReleaser and creating a **draft**
GitHub Release.

The draft is deliberate: it is the last point at which a release can be
abandoned without burning a version number. Inspect it, then publish.

## Signing

**A signature is required to publish to the registry.** Releases are signed with
a GPG key held as the `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` repository secrets;
the workflow imports it and GoReleaser produces a detached binary signature over
the checksum file.

The key must be RSA or DSA — the registry does not accept ECC — and its public
half must be registered against the `pippiio` namespace under *User Settings →
Signing Keys* in the registry, by an organisation admin.

Signing is not enforced by the workflow. With no `GPG_PRIVATE_KEY` secret it
builds with `--skip=sign` and the release still succeeds, noting the omission
only in the job summary. **That failure is silent from the outside**, so confirm
the signature exists before publishing a draft:

```bash
gh release view v0.1.0 --json assets --jq '.assets[].name'
```

Expect one zip per platform, plus `_SHA256SUMS`, `_SHA256SUMS.sig`, and
`_manifest.json`. A release missing the `.sig` is rejected by the registry, and
because tags are immutable the fix is a new version, not a re-tag.

## Publishing to the registry

Ingestion happens by webhook on a **published** GitHub Release — a draft is
invisible to the registry. The first release additionally needs a one-time
connection: registry.terraform.io → *Publish* → *Provider* → select the
repository. That installs the webhook, so subsequent releases are picked up
automatically.

The public key must be registered before the first ingestion, or it fails
signature validation.

Never modify or replace the assets of an already-published version. Terraform
caches checksums, and anyone who installed it will see a mismatch.

---

## Verifying a downloaded artifact

```bash
# Integrity
shasum -a 256 -c terraform-provider-kala_0.1.0_SHA256SUMS \
  --ignore-missing

# Provenance
gpg --verify terraform-provider-kala_0.1.0_SHA256SUMS.sig \
             terraform-provider-kala_0.1.0_SHA256SUMS
```

`scripts/install-provider.sh` performs the checksum step automatically and
refuses to install on a mismatch.

---

## Dev overrides (local development only)

For working on the provider itself, skip packaging entirely:

```hcl
# ~/.terraformrc
provider_installation {
  dev_overrides {
    "registry.terraform.io/pippiio/kala" = "/Users/you/go/bin"
  }
  direct {}
}
```

`go build -o ~/go/bin/terraform-provider-kala .` and run `terraform plan`
directly — **`terraform init` is skipped for overridden providers**, and
Terraform prints a warning on every command to that effect.

Never use this outside local development: it ignores version constraints
entirely and silently uses whatever binary is on disk.

---

## Troubleshooting

**`provider ... does not have a package available for your platform`**
The `<os>_<arch>` does not match the machine. Check with `go env GOOS GOARCH`
(Terraform uses the same names). Apple Silicon is `darwin_arm64`, not
`darwin_amd64`.

**`no available releases match the given constraints`**
The version constraint does not match any published version. Note that the
registry publishes the bare version (`0.1.0`), not the tag (`v0.1.0`), and that
a release still in draft has not been ingested at all.

**A release published but never appeared in the registry**
Check that the release carries `_SHA256SUMS.sig` and `_manifest.json`, and that
the signing key is registered under the `pippiio` namespace. Ingestion failures
surface in the registry UI, not in the GitHub Actions run — the workflow will
have reported success.

**`Provider development overrides are in effect`**
A `dev_overrides` block is active, so version constraints are ignored. Remove it
for anything but local development.
