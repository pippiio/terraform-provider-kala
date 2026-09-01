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
export KALA_API_KEY=...    # webapiv2 — employee settings
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
documented `webapiv2` owns employee **settings**, while the app's internal API
owns employee **lifecycle** — creation, activation, and every profile field. Only
resources that touch lifecycle need `username`/`password`.

## Resources and data sources

| Name | Kind | Purpose |
|------|------|---------|
| `kala_employee` | resource | Create, adopt, configure, and deactivate an employee |
| `kala_employee_setting` | resource | One setting on one employee |
| `kala_employees` | data source | List active employees |

### Behaviour worth knowing before you apply

**Destroy deactivates; it does not delete.** Kala has no delete endpoint for
employees. `terraform destroy` sets the employee inactive, verifies it, and warns
that the record and its history remain. For `kala_employee_setting` — which has
no deactivate either — destroy only removes the resource from state and says so.

**An existing `employee_number` is adopted, not rejected.** Numbers are chosen by
you rather than allocated by Kala, and employees cannot be deleted, so a number
being "already in use" is normal for anyone who has ever offboarded someone. The
resource takes ownership and reactivates them if inactive, leaving every
attribute your configuration does not mention untouched.

**Welcome emails only fire on genuine creation.** Never on adoption or
reactivation. Set `send_welcome_email = false` to suppress them entirely.

**Setting keys are validated against the account.** Kala cannot delete a setting,
so a mistyped key is permanent. `kala_employee_setting` rejects a key that exists
nowhere on the account and suggests the nearest match; `allow_new_key = true`
introduces a genuinely new one.

The survey behind that check is bounded — it examines at most 200 employees in
full, and skips any whose record it cannot read. On a larger account the
rejection says so and reports how many were examined, rather than claiming the
key exists nowhere. Read that wording before reaching for `allow_new_key`: the
key may simply live on an employee that was not checked.

## Development

```bash
make build        # compile
make test         # unit tests — hermetic, no credentials, no network
make cover        # unit tests with coverage
make lint         # golangci-lint
make fmt          # gofmt -s -w
```

Unit tests use `httptest` mocks whose response shapes are copied from real
observed payloads. A handful of integration tests run against a live tenant and
skip unless explicitly enabled:

```bash
KALA_PROBE=1 go test ./internal/client/ -run TestIntegration_InternalRead -v
```

`TestIntegration_CreateEmployee` creates a **permanent** employee — Kala has no
delete — and is gated behind its own variable for that reason.

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

## Project context

Design decisions, constraints, and the API quirks this provider works around are
recorded under [`draft/`](draft/):

- [`draft/adrs/`](draft/adrs/) — why destroy deactivates, and why snapshot-and-restore was rejected
- [`draft/guardrails.md`](draft/guardrails.md) — the fourteen permitted internal-API writes, and the rules around them
- [`draft/product.md`](draft/product.md) — scope, constraints, and open questions
