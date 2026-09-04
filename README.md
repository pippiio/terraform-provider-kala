# Terraform Provider for Kala

Manage employees in [Kala](https://kala.app) — a Danish work-management platform
for construction and service trades — as Terraform configuration.

```hcl
resource "kala_employee" "frodo" {
  employee_number = 42
  name            = "Frodo Baggins"
  email           = "frodo@example.com"

  title      = "Ringbearer"
  department = "Expeditions"
  is_leader  = true
}
```

## Installation

This provider is **not published to the Terraform Registry**. It is distributed
from private GitHub releases, which Terraform cannot fetch directly:

```bash
./scripts/install-provider.sh v0.1.0
```

Then tell Terraform to use the mirror — **this part is required**, because the
binary on disk is not enough on its own:

```hcl
# ~/.terraformrc
provider_installation {
  filesystem_mirror {
    path    = "/Users/you/.terraform.d/plugins"
    include = ["registry.terraform.io/techchapter/*"]
  }
  direct {
    exclude = ["registry.terraform.io/techchapter/*"]
  }
}
```

```hcl
terraform {
  required_providers {
    kala = {
      source  = "registry.terraform.io/techchapter/kala"
      version = "0.1.0"
    }
  }
}
```

Without the `provider_installation` block, `terraform init` still asks the public
registry and fails — `~/.terraform.d/plugins` alone does not override that.

See **[docs/private-distribution.md](docs/private-distribution.md)** for why a
`source` pointing at GitHub cannot work, and the alternatives (network mirror,
private registry) for when a filesystem mirror stops scaling.

## Configuration

Credentials resolve from the environment, so they need never enter a `.tf` file:

```bash
export KALA_API_KEY=...    # webapiv2 — reads
export KALA_USERNAME=...   # internal app API — employee lifecycle
export KALA_PASSWORD=...
```

```hcl
provider "kala" {
  # endpoint = "https://app.kala.dk/webapiv2"   # the default
  # skip_credential_validation = true           # for credential-less CI
}
```

Kala's surface is split across two APIs and neither is sufficient alone: the
documented `webapiv2` covers **reads**, while the app's internal API owns employee
**lifecycle** — creation, activation, and every profile field. Only resources that
touch lifecycle need `username`/`password`.

## Resources and data sources

| Name | Kind | Purpose |
|------|------|---------|
| `kala_employee` | resource | Create, adopt, configure, and deactivate an employee |
| `kala_employees` | data source | List active employees |

### Behaviour worth knowing before you apply

**Destroy deactivates; it does not delete.** Kala has no delete endpoint for
employees. `terraform destroy` sets the employee inactive, verifies it, and warns
that the record and its history remain.

**An existing `employee_number` is adopted, not rejected.** Numbers are chosen by
you rather than allocated by Kala, and employees cannot be deleted, so a number
being "already in use" is normal for anyone who has ever offboarded someone. The
resource takes ownership and reactivates them if inactive, leaving every
attribute your configuration does not mention untouched.

**Welcome emails only fire on genuine creation.** Never on adoption or
reactivation. Set `send_welcome_email = false` to suppress them entirely.

**Employee settings are read-only.** `kala_employees` surfaces the key/value
settings Kala returns, but the provider does not write them. Kala's list endpoint
under-reports settings compared to its single-employee endpoint, so treat the
list as what Kala reported rather than as the complete set.

**Some employees' email addresses cannot be changed.** Kala's `SetEmailNew`
returns success and changes nothing for certain employees. Observed 2026-09-04
against a live tenant: one employee accepted every address tried, another
refused every address tried — the same request differing only in the employee
number, in both active and inactive states. The provider verifies the write by
reading it back, so it reports an error rather than a false success. Retrying
with a different address will not help; those records appear to be editable only
in Kala's own interface.

**Renaming works, through an endpoint the documentation does not mention.**
`ChangeWorkerName` was supplied from a browser session on 2026-09-04 and
verified against the live tenant. `name` is therefore fully managed, with drift
detection, and an adopted employee whose name differs from the configuration is
renamed to match rather than warned about. `boss_employee_number` arrived the
same way, via `ChangeBoss`.

Both endpoints are undocumented, so both are covered by acceptance tests that
assert the change upstream rather than in Terraform state.

## Development

```bash
make build        # compile
make test         # unit tests — hermetic, no credentials, no network
make cover        # unit tests with coverage
make lint         # golangci-lint
make fmt          # gofmt -s -w
make testacc      # acceptance tests — real tenant, real writes
```

Unit tests use `httptest` mocks whose response shapes are copied from real
observed payloads. A handful of integration tests run against a live tenant and
skip unless explicitly enabled:

```bash
KALA_PROBE=1 go test ./internal/client/ -run TestIntegration_InternalRead -v
```

`TestIntegration_CreateEmployee` creates a **permanent** employee — Kala has no
delete — and is gated behind its own variable for that reason.

### Acceptance tests

`make testacc` runs real Terraform against a real Kala tenant. It is gated on
`TF_ACC` and skips entirely without it, so `make test` and CI stay hermetic.
These never run in CI: the tenant holds real personal data, and no credential
for it belongs in repository secrets.

```bash
export KALA_API_KEY=...
export KALA_USERNAME=...
export KALA_PASSWORD=...

export KALA_ACC_EMPLOYEE_NUMBER=9001      # a number reserved for testing
export KALA_ACC_EMPLOYEE_NUMBER_ALT=9002  # optional; enables the adoption test
export KALA_ACC_EMAIL=terraform-acc@example.com

make testacc
```

**Pick the numbers deliberately, and reserve them.** `KALA_ACC_EMPLOYEE_NUMBER`
has no default on purpose: the first run creates a real employee under whatever
number you give it, and **Kala cannot delete an employee**. A number that
already belongs to someone would be adopted and written to instead.

Once reserved, the suite is safely repeatable. Create is an upsert and destroy
deactivates, so every run after the first adopts and reactivates the same record
rather than making another. A hundred runs leave one employee, not a hundred.

Every acceptance configuration sets `send_welcome_email = false`. Mail reaches a
real person and cannot be recalled, so the suite never sends any — including on
the very first run, when the employee is genuinely created.

The tests leave the employee **deactivated**, because that is what
`terraform destroy` does and what `CheckDestroy` verifies. That is the expected
end state, not a failure.

To work against a locally built binary, use a dev override:

```hcl
# ~/.terraformrc
provider_installation {
  dev_overrides {
    "registry.terraform.io/techchapter/kala" = "/Users/you/go/bin"
  }
  direct {}
}
```

`terraform init` is skipped for overridden providers — run `terraform plan`
directly.

## Releasing

```bash
git tag v0.1.0 && git push origin v0.1.0
```

The release workflow re-runs fmt, lint, and tests against the tagged commit — a
tag can point at any commit, including one that never passed CI — then builds
every platform and creates a **draft** GitHub Release for a human to publish.

Signing is optional; configure `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` to enable
it. See [docs/private-distribution.md](docs/private-distribution.md#cutting-a-release).
