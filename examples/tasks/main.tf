terraform {
  required_providers {
    kala = {
      source = "registry.terraform.io/techchapter/kala"
    }
  }
}

provider "kala" {}

# A Kala "task" is a checklist item on a case.
#
# `case_id` is REQUIRED: Kala addresses checklist items by case and has no
# account-wide task endpoint. Reading every task in the account would mean one
# request per case, so the provider does not offer it — compose it yourself if
# you genuinely need it, and be aware of the cost.

data "kala_cases" "active" {}

# Tasks for one case.
data "kala_tasks" "for_case" {
  case_id = data.kala_cases.active.cases[0].id

  # Applied upstream:
  # search          = "roof"
  # name_contains   = "gutter"
  # only_unfinished = true

  # Applied client-side — Kala has no assignee parameter. This narrows the
  # RESULT without reducing what was read, so `complete` still describes the
  # underlying read rather than the filtered list.
  # assignee_worker_nr = 1

  # include_contact_details = true   # assignee, created_by, finished_by
  # include_financials      = true   # hours and price
}

output "tasks" {
  value = [
    for t in data.kala_tasks.for_case.tasks :
    { id = t.id, name = t.name, finished = t.is_finished, status = t.status_name }
  ]
}

output "case_task_progress" {
  value = "${data.kala_tasks.for_case.case_finished}/${data.kala_tasks.for_case.case_total}"
}

# ---------------------------------------------------------------------------
# Tasks across several cases: a for_each composition, not a provider fan-out
# ---------------------------------------------------------------------------

data "kala_tasks" "per_case" {
  for_each = { for c in data.kala_cases.active.cases : tostring(c.id) => c }

  case_id         = each.value.id
  only_unfinished = true
}

# ---------------------------------------------------------------------------
# All of one customer's tasks
# ---------------------------------------------------------------------------
#
# Kala has no customer -> tasks path. The case list carries the customer's
# company as text but no customer id, so the join is by company name, filtered
# client-side, and then fanned out per case.

data "kala_customer" "target" {
  cvr = "12345678"
}

data "kala_cases" "for_customer" {
  customer_company = data.kala_customer.target.company
}

data "kala_tasks" "for_customer" {
  for_each = { for c in data.kala_cases.for_customer.cases : tostring(c.id) => c }

  case_id = each.value.id
}

output "customer_task_counts" {
  value = { for id, d in data.kala_tasks.for_customer : id => length(d.tasks) }
}

output "open_tasks_per_case" {
  value = {
    for id, d in data.kala_tasks.per_case :
    id => length(d.tasks)
  }
}

# ---------------------------------------------------------------------------
# One task
# ---------------------------------------------------------------------------
#
# Set exactly one of id or name. `name` is narrowed upstream via nameContains,
# then matched exactly.

data "kala_task" "one" {
  case_id = data.kala_cases.active.cases[0].id
  name    = "Secret task"
}

output "task_detail" {
  value = {
    id       = data.kala_task.one.id
    finished = data.kala_task.one.is_finished
    deadline = data.kala_task.one.deadline
  }
}
