# One customer, all of their cases, all of the tasks on those cases.
#
# This is the walkthrough that spans all three data sources, and it exists
# because the traversal is NOT a simple join. Kala offers no customer -> cases
# path and no account-wide task endpoint, so each hop is worked around
# differently and each workaround has a way of giving you a wrong answer.
#
# Read the "What this costs" and "Where this can lie to you" sections at the
# bottom before pointing it at a large tenant.

terraform {
  required_providers {
    kala = {
      source = "registry.terraform.io/pippiio/kala"
    }
  }
}

# Every data source below except kala_employees is served by Kala's internal app
# API, so username and password are both required:
#
#   export KALA_USERNAME=...
#   export KALA_PASSWORD=...
provider "kala" {}

# ---------------------------------------------------------------------------
# 1. The customer
# ---------------------------------------------------------------------------
#
# Set exactly one of cvr, number, or id. cvr and number are narrowed upstream
# before matching; id is not, because Kala's query parameter searches text, so
# an id lookup reads the customer list and selects from it.

data "kala_customer" "target" {
  cvr = "12345678"
}

# ---------------------------------------------------------------------------
# 2. Their cases
# ---------------------------------------------------------------------------
#
# THE JOIN IS BY COMPANY NAME, NOT BY ID.
#
# Case records in the *list* carry customer_company as text but no customer_id
# — only the kala_case detail endpoint exposes customer_id. So there is nothing
# to join on, and customer_company matches the company string exactly, ignoring
# case. Two customers sharing a company name are indistinguishable here.
#
# Active and archived cases are DISJOINT sets and no single call returns both,
# so covering the customer's whole history takes two blocks.

data "kala_cases" "active" {
  customer_company = data.kala_customer.target.company
  # active = true   # the default
}

data "kala_cases" "archived" {
  customer_company = data.kala_customer.target.company
  active           = false
}

locals {
  all_cases = concat(
    data.kala_cases.active.cases,
    data.kala_cases.archived.cases,
  )

  # Keyed by the integer id, because that is what tasks reference. The string
  # `number` (e.g. "KA-1") is what kala_case takes — they are not the same
  # value and are not interchangeable.
  cases_by_id = { for c in local.all_cases : tostring(c.id) => c }
}

# ---------------------------------------------------------------------------
# 3. One case in full
# ---------------------------------------------------------------------------
#
# The list returns roughly 25 fields; the detail endpoint returns 64. Fetch the
# detail only for the cases you actually need it for — this is one request each.
#
# customer_id appears here and nowhere else, which is how you confirm that the
# name-based match above found the right customer.

data "kala_case" "first" {
  case_number = local.all_cases[0].number

  # include_financials      = true   # commercially sensitive; off by default
  # include_contact_details = true   # personal data; off by default
}

# ---------------------------------------------------------------------------
# 4. Tasks, fanned out per case
# ---------------------------------------------------------------------------
#
# A Kala "task" is a checklist item on a case. case_id is required because Kala
# addresses checklist items by case and has no account-wide task endpoint.
#
# The fan-out is deliberately yours to write rather than hidden inside the
# provider, so that one-request-per-case is visible at the call site.

data "kala_tasks" "per_case" {
  for_each = local.cases_by_id

  case_id = each.value.id

  # Applied upstream, so these genuinely reduce what is read:
  # search          = "roof"
  # name_contains   = "gutter"
  # only_unfinished = true

  # Applied CLIENT-SIDE — Kala accepts no assignee parameter. This narrows the
  # result without reducing the read, which is why `complete` still describes
  # the underlying read rather than the filtered list.
  # assignee_worker_nr = 1
}

# ---------------------------------------------------------------------------
# Results
# ---------------------------------------------------------------------------

output "customer" {
  value = {
    id      = data.kala_customer.target.id
    number  = data.kala_customer.target.number
    company = data.kala_customer.target.company
    cases   = data.kala_customer.target.case_count
  }
}

output "cases" {
  value = [
    for c in local.all_cases : {
      id       = c.id
      number   = c.number
      name     = c.name
      archived = c.archived
    }
  ]
}

# customer_id is only available from the detail endpoint. If this does not equal
# data.kala_customer.target.id, the company-name match found someone else.
output "first_case_customer_matches" {
  value = data.kala_case.first.customer_id == data.kala_customer.target.id
}

output "open_tasks_per_case" {
  value = {
    for id, d in data.kala_tasks.per_case :
    local.cases_by_id[id].number => [
      for t in d.tasks : t.name if !t.is_finished
    ]
  }
}

output "task_progress_per_case" {
  value = {
    for id, d in data.kala_tasks.per_case :
    local.cases_by_id[id].number => "${d.case_finished}/${d.case_total}"
  }
}

# ---------------------------------------------------------------------------
# Where this can lie to you
# ---------------------------------------------------------------------------
#
# CHECK THIS BEFORE TREATING ANY OF THE ABOVE AS COMPLETE.
#
# Every list data source reports whether its read reached the end of the data.
# When `complete` is false the list is a SUBSET — the pagination cap was hit —
# and the provider also emits a warning. Acting on a partial list as though it
# were whole is a bug, not a rounding error: a case missing from `cases` is
# indistinguishable from a case that does not exist.
#
# Raise page_size on kala_cases if you see false here.

output "coverage_is_complete" {
  value = {
    active_cases   = data.kala_cases.active.complete
    archived_cases = data.kala_cases.archived.complete
    tasks = {
      for id, d in data.kala_tasks.per_case :
      local.cases_by_id[id].number => d.complete
    }
  }
}

# ---------------------------------------------------------------------------
# What this costs
# ---------------------------------------------------------------------------
#
#   1 read   kala_customer  (plus a full customer-list read if you select by id)
#   2 reads  kala_cases     (active and archived, each paginated)
#   1 read   kala_case      (per case you fetch the detail for)
#   N reads  kala_tasks     (one per case, each paginated)
#
# N is the customer's entire case history, active and archived. On a customer
# with hundreds of cases this is hundreds of requests on every plan and every
# apply — Terraform re-reads data sources both times. Narrow with
# only_unfinished, or restrict local.cases_by_id to the cases you care about,
# before running this against a large account.

# ---------------------------------------------------------------------------
# 5. The same traversal, MANAGED
# ---------------------------------------------------------------------------
#
# Everything above reads. Below creates — and creating is the part that cannot
# be undone, so read this section before applying it.
#
#   kala_customer          removes from state on destroy; the customer REMAINS
#   kala_case              ARCHIVES on destroy; the case remains, reversibly
#   kala_task              removes from state on destroy; the item REMAINS
#   kala_task_assignment   detaches the employee; the job link remains
#
# None of these creates is an upsert. Kala allocates every id and number
# itself, so applying this twice creates two of everything. Bring existing
# records under management with `terraform import`, never by re-declaring them.

resource "kala_customer" "acme" {
  company = "Acme Roofing ApS"
  cvr     = "87654321"
  email   = "post@example.com"
  phone   = "+45 20 00 00 10"
  address = "Industrivej 4"
  zip     = "2600"

  description = "Managed by Terraform"
}

# A case is either customer-facing or internal. customer_number is required
# when internal_project is false and must be omitted when it is true — checked
# at PLAN time, because upstream they are two differently shaped requests.
resource "kala_case" "reroof" {
  name            = "Re-roof, Industrivej 4"
  customer_number = kala_customer.acme.number
  worker_number   = 1

  address = "Industrivej 4"
  zip     = "2600"

  # The contact for THIS JOB. Not the customer's own phone number — changing
  # it does not touch kala_customer.acme.
  contact_phone = "+45 20 00 00 11"
}

# Tasks are the checklist. This resource manages what the work IS; whether it
# is done is read-only, so a worker ticking one off produces no diff.
resource "kala_task" "steps" {
  for_each = {
    strip    = { name = "Strip the old covering", photo = true }
    membrane = { name = "Lay membrane", photo = true }
    inspect  = { name = "Final inspection", photo = false }
  }

  case_number = kala_case.reroof.number
  name        = each.value.name

  image_required = each.value.photo
  note_required  = true

  # SECOND precision: Kala does not round-trip finer, so a sub-second value
  # would produce a permanent diff.
  deadline = "2026-10-31T15:00:00Z"
}

# Assignment is a job link between an employee and a CASE, scoped to a set of
# items. The link is SHARED, so declare exactly ONE of these per (case,
# employee) pair — two would overwrite each other on every apply.
resource "kala_task_assignment" "roofer" {
  case_number   = kala_case.reroof.number
  worker_number = 101

  # The COMPLETE set for this pair. An id removed here is detached next apply.
  task_ids = [
    kala_task.steps["strip"].id,
    kala_task.steps["membrane"].id,
  ]
}

resource "kala_task_assignment" "inspector" {
  case_number   = kala_case.reroof.number
  worker_number = 102

  task_ids = [kala_task.steps["inspect"].id]
}

# The managed records read back through the same data sources as everything
# above — which is the point: what Terraform creates and what it reads are the
# same records, addressed the same way.
output "managed_case" {
  value = {
    number   = kala_case.reroof.number
    customer = kala_customer.acme.number
    tasks    = [for k in kala_task.steps : k.name]
  }
}
