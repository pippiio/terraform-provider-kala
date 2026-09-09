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
export KALA_API_KEY=...    # webapiv2 — employee reads
export KALA_USERNAME=...   # internal app API — employee lifecycle,
export KALA_PASSWORD=...   #   plus all customer, case, and task data sources
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
documented `webapiv2` covers **employee reads**, while the app's internal API
owns employee **lifecycle** — creation, activation, and every profile field —
and is the **only** source of customers, cases, and tasks. `username`/`password`
are therefore required by every data source below except `kala_employees`.

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
| Role in this provider | Reads only, and only employees — backs `kala_employees` | Every write, the whole employee lifecycle, `kala_employee`'s own `Read`, and **all** customer, case, and task reads |

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
| `kala_customers` | data source | List customers |
| `kala_customer` | data source | One customer by `id`, `number`, or `cvr` |
| `kala_cases` | data source | List cases, active or archived |
| `kala_case` | data source | One case by number, with the full detail record |
| `kala_tasks` | data source | Tasks (checklist items) on one case |
| `kala_task` | data source | One task by `id` or name |
| `kala_customer` | resource | Create and update a customer |
| `kala_case` | resource | Create, update, and archive a case |
| `kala_task` | resource | Create and update a checklist item |
| `kala_task_assignment` | resource | Assign an employee to a set of tasks on a case |

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

**Data sources report whether they read everything.** Every list data source
exposes a `complete` attribute and warns when its pagination cap was reached
before the end of the data. A list with `complete = false` is a **subset**, and
treating it as the whole account is a bug — check it before acting on the result.

The same reasoning governs the single-record lookups. `kala_customer` and
`kala_task` select client-side, because Kala has no by-id endpoint for either. If
the record is absent from a read that did **not** cover the account, they say the
lookup could not be completed rather than reporting the record as missing —
absence from a partial read proves nothing.

**`kala_cases` selects a set; it does not narrow one.** Kala's archived and
non-archived cases are disjoint, and no single call returns both. `active = true`
(the default) returns only active cases and `active = false` returns only
archived ones, so reading everything takes two blocks and a `concat`.

**`kala_tasks` requires a `case_id`.** Kala addresses checklist items by case and
has no account-wide task endpoint. Reading tasks across cases is a `for_each`
composition over `kala_cases`, deliberately left to you rather than hidden in the
provider as a request-per-case fan-out.

### Personal, commercial, and transactional data in state

Everything a data source exposes is written to Terraform state, and this provider
reaches data well beyond names and numbers. State files must be treated as
confidential and stored accordingly.

Sensitive fields are therefore **opt-in and null by default**:

| Opt-in | Exposes |
|--------|---------|
| `include_contact_details` | Customer email, phone, address; task assignee, author, and completer |
| `include_financials` | Case cost, sales, result, invoiced/uninvoiced, realised; task hours and fixed price |

Two things follow. Employee, customer, and task records are **personal data under
GDPR** — names, emails, phone numbers, and who completed which task. Case and
task financials are **commercially sensitive**, and a state file in a shared
backend or a CI artifact distributes them to everyone with access to it.

Kala's "tasks" are checklist items, which carry completion timestamps and
registered hours. Reading them is supported; *managing* them as Terraform
resources is not, and remains out of scope.

## Limitations

These are properties of Kala's API, not of the implementation. They are listed
because each one is a way to get a wrong answer if you assume otherwise.

### Absence is not always provable

`kala_customer` and `kala_task` have **no upstream by-id endpoint**. Both read a
list and select from it. When the record is missing from a read that hit its
pagination cap, they report that the lookup *could not be completed* rather than
that the record does not exist — absence from a partial read proves nothing.
Raise `page_size` if you see that diagnostic; do not read it as "not found".

Every list data source exposes `complete` for the same reason. A list with
`complete = false` is a subset.

### `customer_company` matches text; it is not a join

The case list carries the customer's company name but **no customer id**, so
`kala_cases.customer_company` compares strings. It will not follow a renamed
company, and two customers sharing a company name are indistinguishable through
it. A real join would need a per-case detail fetch. Use `kala_customer` when you
need an id.

It is also deliberately not pushed into the upstream `search` parameter, which
matches case *names* as well as customer fields — narrowing with it could drop
cases that genuinely match.

### Active and archived cases are disjoint sets

`kala_cases.active` selects **which set** to return, not how to narrow one. Kala
offers no call returning both, so reading every case takes two data source blocks
and a `concat`. A configuration that reads only the default set is silently
blind to archived cases.

### Tasks are per-case by construction

`kala_tasks.case_id` is required: Kala addresses checklist items by case and has
no account-wide task endpoint. Reading across cases is a `for_each` over
`kala_cases` — deliberately yours to write, so the cost of one request per case
is visible rather than hidden inside the provider.

Assignee filtering is applied **client-side**, because Kala accepts no assignee
parameter. It narrows the result without reducing what was read, which is why
`complete` still describes the read. Only the responsible worker
(`respWorkerNr`) is matched; the `workersAssigned` collection has never been
observed populated, so its shape is unknown and it is not exposed.

### Identifiers do not interchange

| Field | Type | Note |
|-------|------|------|
| `kala_case.number` | string | e.g. `KA-1`. How a case is **addressed** |
| `kala_case.id` | number | How a case is **referenced** by `kala_tasks.case_id` |
| `kala_customer.number` | string | A **string** here; webapiv2 spells the same field as an integer, and the two are not known to hold the same value |

`economy_case_number` mirrors `number` on every case observed, including
Kala-native internal projects that have no e-conomic counterpart. Do not treat it
as evidence of an e-conomic link.

### Fields that are absent, not empty

The case list returns 27 fields; the detail endpoint returns 69. Anything past
identity and customer name must come from `kala_case`.

`is_finished` is exposed only from the detail endpoint, where it means
completion. The list endpoint has a field of the same name that tracks
*archived-ness* and disagrees with it, so it is not exposed at all.

`status_name` on tasks is **not translated** — the values come from the
company-wide `kanban_options` setting and appear in whatever language it uses.

Several list fields (`status`, `color`, `start_date`, `end_date`) were null
throughout the account this provider was developed against, so their types were
never confirmed and they are not exposed.

### Operational

- All six customer, case, and task data sources require `KALA_USERNAME` and
  `KALA_PASSWORD`. Only `kala_employees` works with `api_key` alone.
- They read Kala's **internal, undocumented, unversioned** app API, because
  `webapiv2` does not expose these entities usefully. It may change without
  notice.
- Multi-company behaviour is untested: the `kacompany` header is sent, but
  development had access to a single-company account only.
- Kala documents no rate limits. Requests are bounded, retried with backoff on
  5xx, and never retried on 4xx, but a large `for_each` over cases will still
  generate one request per case.

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

# For the customer, case, task, and assignment tests: EXISTING records the
# suite reads, changes, and puts back. It does not create them.
export KALA_ACC_CUSTOMER_ID=4             # numeric customer id
export KALA_ACC_CASE_NUMBER=KA-4          # a customer-facing, non-archived case
export KALA_ACC_TASK_ID=9                 # a checklist item ON that case

make testacc
```

### The write resources are tested the opposite way round

The employee suite reuses a fixed number safely because create is an **upsert**:
run N+1 adopts whatever run N left, so a hundred runs converge on one record.

**None of `kala_customer`, `kala_case`, or `kala_task` works that way.** Kala
allocates every id and number itself, so create is not idempotent and cannot be
— running a create test twice makes two records, and Kala can delete neither.

So that part of the suite does not create by default:

- **Read, update, import and destroy run every time**, against the existing
  records named above. Each test **restores what it changed** in a final step,
  so the tenant is left as it was found.
- **Creation runs only under `KALA_ACC_CREATE=1`**, because each run leaves a
  permanent record:

  ```bash
  KALA_ACC_CREATE=1 make testacc   # CREATES RECORDS THAT CANNOT BE DELETED
  ```

`KALA_ACC_CASE_NUMBER` must name a **customer-facing** case (an internal project
has no customer to assert against) that is **not archived** (the archive test
archives it and puts it back). `KALA_ACC_TASK_ID` must be an item on that case.

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
