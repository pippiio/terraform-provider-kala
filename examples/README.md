# Examples

Two kinds of thing live here, and they are not interchangeable.

**Registry fragments** — `provider/`, `data-sources/*/`, `resources/*/` — are
consumed by `tfplugindocs` and embedded verbatim into the pages under `docs/`.
Their paths and filenames are fixed by that tool's convention, they deliberately
omit the `terraform {}` block, and editing one changes published documentation.
Regenerate `docs/` after touching any of them.

**Walkthroughs** — every other directory — are self-contained, runnable
configurations. They carry their own `required_providers` block and go further
than a documentation snippet can: composition across data sources, the cost of a
fan-out, and the specific ways Kala's API will hand you a confident wrong answer.

| Directory | What it shows | Credentials |
|---|---|---|
| `employees/` | Reading the employee roster; provider configuration | `KALA_API_KEY` |
| `employee/` | The `kala_employee` lifecycle — hiring, leadership rights, bulk migration, offboarding, re-hiring | all three |
| `customers/` | Listing customers and looking one up by `cvr`, `number`, or `id` | username + password |
| `cases/` | Active and archived cases as disjoint sets; one case in full | username + password |
| `tasks/` | Checklist items per case, and fanning out across cases | username + password |
| `customer-cases-tasks/` | End to end: one customer → their cases → every task on them | username + password |

Start with `customer-cases-tasks/` if you want the whole traversal in one place,
or `employees/` if you only need reads and only hold an API key.

## Credentials

Kala is two APIs, and which one a data source uses decides what it needs:

```bash
export KALA_API_KEY=...    # webapiv2 — kala_employees only
export KALA_USERNAME=...   # internal app API — the kala_employee resource,
export KALA_PASSWORD=...   #   plus every customer, case, and task read
```

If your Kala login belongs to more than one company, set `KALA_COMPANY` too. The
provider will not guess which tenant to act on.

## Running one

The provider is not published to the public registry, so `terraform init` needs
a filesystem mirror and a `provider_installation` block in `~/.terraformrc`. See
[Installation](../README.md#installation) — the binary on disk is not enough on
its own.

```bash
make install-mirror        # from the repository root
cd examples/customers
terraform init
terraform plan
```

`plan` is enough for every read-only walkthrough; only `employee/` writes
anything. Note that Terraform re-reads data sources on both `plan` and `apply`,
so the request counts described in each example are paid every time.

## Before you apply `employee/`

**Kala cannot delete an employee.** `employee_number` is chosen by you rather
than allocated by Kala, so applying against a number already in use *adopts*
that person and writes to their record. `terraform destroy` deactivates rather
than deletes. Pick numbers deliberately, and read the comments in
`employee/main.tf` first — particularly `send_welcome_email`, which sends real
mail to a real person and cannot be recalled.

## Validating changes

Every directory here is checked against the provider's real schemas:

```bash
terraform fmt -check -recursive examples/
terraform validate            # per directory, after init
```

Registry fragments have no `terraform {}` block, so validating one means copying
it into a scratch directory alongside a `required_providers` block first.
