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
  # company  = 4242                             # or KALA_COMPANY
  # skip_credential_validation = true           # for credential-less CI
}
```

**`company` decides which tenant is written to.** A Kala login can belong to
several companies. Omit it when yours belongs to exactly one; when it belongs to
several the provider refuses to guess, and names the choices:

```
kala: this login is attached to 2 companies, so which one to manage is
ambiguous; set the provider's company attribute (or KALA_COMPANY) to one
of: 4242 (Rivendell), 7000 (Gondor)
```

Refusing is deliberate. The alternative is picking whichever company Kala lists
first, and nothing guarantees that order is stable between sign-ins — so a
guess could create or deactivate a person in the wrong organisation, silently.

Note this is separate from the employee restriction below: `company` selects the
tenant *your credentials* act on, whereas Kala refuses to change the email of an
*employee* whose own login spans several companies. Setting `company` does not
lift that restriction.

Kala's surface is split across two APIs and neither is sufficient alone: the
documented `webapiv2` covers **reads**, while the app's internal API owns employee
**lifecycle** — creation, activation, and every profile field. Only resources that
touch lifecycle need `username`/`password`.

### How the two APIs differ

They are not two views of one system. They disagree on authentication, on
vocabulary, on how failure is reported, and on who exists.

| | `webapiv2` | Internal app API |
|---|---|---|
| Status | Documented | Undocumented, unversioned, may change without notice |
| Credential | `api_key`, in the **query string** | `username`/`password` → session token, in **headers** |
| Auth mechanics | One parameter — but `Index` spells it `apikey` and everything else `api_key` | Two-step `SignIn` → `SelectCompany`, then `kauthtoken` + `kacompany`; expiry triggers one silent re-login and retry |
| Parameters | Query string, **even for writes** | JSON bodies |
| Vocabulary | "employee", `employeeNumber` / `number` | "worker", with the identifier key varying *per endpoint*: `workerNr`, `workerID`, or `workerId` |
| Paths | Flat endpoint names | `/api/…`, trailing slash present or absent per endpoint |
| Failure reporting | HTTP status; an unknown employee is `200` with an **empty body**, not `404` | `HTTP 200` carrying `{"status":"Error","message":"…"}` — **in Danish**. `WorkerInfo` returns `500` for a missing worker |
| Who it can see | **Active employees only** | **All workers**, active or not, via `isValidated` |
| Role in this provider | Reads only — backs `kala_employees` | Every write, the whole lifecycle, and `kala_employee`'s own `Read` |

`medarbejderNr`, `workerNr`, `workerId`, and `employeeNumber` are one value, so a
single `employee_number` addresses a person across both.

The visibility row is the one with teeth. On `webapiv2` a deactivated employee is
indistinguishable from one who never existed — the record simply stops being
returned. `kala_employee` therefore reads through the **internal** API: reading
through `webapiv2` would make a destroyed employee look deleted on the next
refresh, and Terraform would try to create someone who is already there.

The same difference means `kala_employees` will not list an employee this
provider has just deactivated. That is Kala's behaviour, not a bug in the data
source.

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

**The internal API reports failures with `HTTP 200`.** A refused write answers
`200` with `{"status": "Error", "message": "..."}` in the body. The status line
alone is therefore never evidence that a write landed. Every response is checked
for that envelope and Kala's message is surfaced verbatim — it is in Danish, and
it is the most specific explanation available.

For example, an employee whose Kala login belongs to more than one company
cannot have their email changed: *"Man kan ikke skifte email, når man er
tilknyttet flere virksomheder."* Retrying with a different address will not help.

Read-back verification remains behind that check as a backstop, for a write that
is neither reported as an error nor actually applied.

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
