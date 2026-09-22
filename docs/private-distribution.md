# Installing a provider from a private repository

Terraform cannot download a provider from a private GitHub repository. It only
knows three sources: a **registry**, a **network mirror**, and a **filesystem
mirror** — and none of them speak "authenticated GitHub release". This document
covers the ways round that, and which one to pick.

> **Short answer:** use a filesystem mirror, installed by the script in
> `scripts/install-provider.sh`. Move to a network mirror when more than a
> handful of people need it, or a private registry when you want
> `terraform init` to resolve versions on its own.

---

## Why `source` alone will not work

```hcl
terraform {
  required_providers {
    kala = {
      source  = "github.com/pippiio/terraform-provider-kala"  # does NOT work
      version = "0.1.0"
    }
  }
}
```

A `source` address is `HOSTNAME/NAMESPACE/TYPE`, and Terraform resolves the
hostname by asking it to implement the **provider registry protocol** — a set of
JSON endpoints under `/.well-known/terraform.json`. `github.com` does not
implement it, so this fails no matter what permissions the caller has.

The address is a *name*, not a download location. All three options below keep
the same `source` string and change only where Terraform looks for the bytes.

Throughout, this provider's address is:

```
registry.terraform.io/pippiio/kala
```

Using `registry.terraform.io` as the hostname is deliberate even though nothing
is published there: it is the default namespace, so a future move to the public
registry needs no configuration change in consumers. Substitute your own host if
you would rather be explicit that it is private.

---

## Option 1 — Filesystem mirror (recommended to start)

Terraform looks for providers in a directory laid out by address, version, and
platform. Put the binary there and `terraform init` finds it offline.

### Layout

```
~/.terraform.d/plugins/
└── registry.terraform.io/
    └── pippiio/
        └── kala/
            └── 0.1.0/
                └── darwin_arm64/
                    └── terraform-provider-kala_v0.1.0
```

On Windows the base is `%APPDATA%\terraform.d\plugins`.

The `<os>_<arch>` directory must match the machine running Terraform, and the
binary name must be `terraform-provider-<type>_v<version>`.

### Install

```bash
# Requires the GitHub CLI, authenticated against the private repo.
./scripts/install-provider.sh v0.1.0
```

The script downloads the release archive for your platform, verifies it against
`SHA256SUMS`, and unpacks it into the layout above.

### Point Terraform at the mirror — this step is required

**Putting the binary in `~/.terraform.d/plugins` is not enough on its own.** That
directory is an *implicit* mirror, and Terraform still consults the public
registry first for any `registry.terraform.io/...` address — which fails, because
nothing is published there:

```
Error: Failed to query available provider packages
  provider registry registry.terraform.io does not have a provider named
  registry.terraform.io/pippiio/kala
```

A `provider_installation` block is what stops that fall-through. Create
`~/.terraformrc` (or point `TF_CLI_CONFIG_FILE` at a file):

```hcl
provider_installation {
  filesystem_mirror {
    path    = "/Users/you/.terraform.d/plugins"
    include = ["registry.terraform.io/pippiio/*"]
  }
  direct {
    exclude = ["registry.terraform.io/pippiio/*"]
  }
}
```

Both halves matter. `filesystem_mirror` says where to look; `direct { exclude }`
says *not* to ask the registry for these addresses. Omit the exclude and
Terraform still tries the registry and fails with the error above.

Then:

```hcl
terraform {
  required_providers {
    kala = {
      source  = "registry.terraform.io/pippiio/kala"
      version = "0.1.0"
    }
  }
}
```

```console
$ terraform init
- Installed pippiio/kala v0.1.0 (unauthenticated)
```

`unauthenticated` is expected: it means the artifact carries no registry
signature, not that anything is wrong. Terraform still records the hash in
`.terraform.lock.hcl`, so subsequent runs verify the binary has not changed.

### Trade-offs

- **Good:** no infrastructure, works offline, version constraints still apply.
- **Bad:** every machine installs separately, including every CI runner. Nothing
  discovers new versions — bumping means re-running the script.

### In CI

Install before `terraform init`:

```yaml
- name: Install the Kala provider
  env:
    GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}   # needs read access to the repo
  run: ./scripts/install-provider.sh v0.1.0
```

For a runner that cannot reach GitHub at all, ship the mirror directory as a
build artifact and set `--dir` plus a matching `filesystem_mirror` path.

---

## Option 2 — Network mirror (for a team)

A network mirror is a static HTTP server implementing the [provider network
mirror protocol][mirror-proto] — just JSON index files plus the archives. It
needs no registry logic and can be S3, GCS, or any bucket behind auth.

Generate the whole tree with Terraform itself:

```bash
terraform providers mirror -platform=linux_amd64 -platform=darwin_arm64 ./mirror
```

Upload `./mirror`, then point consumers at it:

```hcl
provider_installation {
  network_mirror {
    url     = "https://tf-mirror.internal.example.com/"
    include = ["registry.terraform.io/pippiio/*"]
  }
  direct {
    exclude = ["registry.terraform.io/pippiio/*"]
  }
}
```

### Trade-offs

- **Good:** one place to update, all consumers pick it up, still no registry
  implementation to maintain. Handles version discovery.
- **Bad:** you host something. Terraform's mirror client sends no credentials of
  its own, so access control has to be network-level (VPN, IP allowlist, signed
  URLs) or a proxy that injects auth.

---

## Option 3 — Private registry (for an organisation)

Implement the [provider registry protocol][registry-proto], or use something
that already does — Artifactory, Cloudsmith, Scalr, and Terraform Cloud's
private registry all support it.

```hcl
terraform {
  required_providers {
    kala = {
      source  = "tf.example.com/pippiio/kala"
      version = "~> 0.1"
    }
  }
}
```

### Trade-offs

- **Good:** the native experience. Version constraints, `terraform init -upgrade`,
  and credentials via `terraform login` all work as designed.
- **Bad:** a service to run or pay for. Only worth it once several teams consume
  several private providers.

---

## Option 4 — Dev overrides (local development only)

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

## Cutting a release

Tags drive releases, and this repository constrains them:

- Tags must match `v[0-9]*` (the *Enforce tag naming* ruleset).
- Tag creation is restricted to specific actors (*Create tag actors*).
- Tags cannot be moved or deleted once pushed (*Immutable tags*) — a bad release
  needs a new version, never a retag.

```bash
git tag v0.1.0
git push origin v0.1.0
```

`.github/workflows/release.yml` then re-runs fmt, lint, and tests against the
tagged commit — a tag can point at any commit, including one that never passed
CI — before building every platform with GoReleaser and creating a **draft**
GitHub Release. Publish it when ready.

### Signing (optional but recommended)

Without a signing key the release still works; consumers can verify integrity
from `SHA256SUMS` but cannot verify who produced it. To enable signing:

1. Generate a key: `gpg --full-generate-key`
2. Export it: `gpg --armor --export-secret-keys <KEY_ID>`
3. Add repository secrets `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE`

The workflow detects the secret and signs `SHA256SUMS` automatically; with no
secret it skips signing and notes that in the job summary. A signature is also a
prerequisite for ever publishing to the public registry.

---

## Verifying a downloaded artifact

```bash
# Integrity
shasum -a 256 -c terraform-provider-kala_0.1.0_SHA256SUMS \
  --ignore-missing

# Provenance, if the release was signed
gpg --verify terraform-provider-kala_0.1.0_SHA256SUMS.sig \
             terraform-provider-kala_0.1.0_SHA256SUMS
```

`scripts/install-provider.sh` performs the checksum step automatically and
refuses to install on a mismatch.

---

## Troubleshooting

**`Failed to query available provider packages` — "registry does not have a provider named ..."**
Terraform is asking the public registry. Either there is no
`provider_installation` block at all (the implicit `~/.terraform.d/plugins`
mirror is *not* sufficient for `registry.terraform.io/...` addresses), or the
block is missing `direct { exclude }`.

**`no available releases match the given constraints`**
The `provider_installation` block is being read — Terraform is looking in the
mirror — but that version is not there. Check the directory name matches the
`version` in `required_providers` exactly, and note the path uses the bare
version (`0.1.0`), not the tag (`v0.1.0`).

**`provider ... does not have a package available for your platform`**
The `<os>_<arch>` directory does not match the machine. Check with
`go env GOOS GOARCH` (Terraform uses the same names). Apple Silicon is
`darwin_arm64`, not `darwin_amd64`.

**Works locally, fails in CI**
The runner has no filesystem mirror. Install the provider as a pipeline step
before `terraform init`.

**`Provider development overrides are in effect`**
A `dev_overrides` block is active, so version constraints are ignored. Remove it
for anything but local development.

[mirror-proto]: https://developer.hashicorp.com/terraform/internals/provider-network-mirror-protocol
[registry-proto]: https://developer.hashicorp.com/terraform/internals/provider-registry-protocol
