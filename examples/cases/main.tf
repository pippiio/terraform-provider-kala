terraform {
  required_providers {
    kala = {
      source = "registry.terraform.io/techchapter/kala"
    }
  }
}

provider "kala" {}

# ---------------------------------------------------------------------------
# `active` selects a SET — it is not a narrowing filter
# ---------------------------------------------------------------------------
#
# Kala's archived and non-archived cases are DISJOINT sets, and there is no
# single call that returns both. Reading everything therefore takes two blocks.

data "kala_cases" "active" {
  # active = true   # the default
}

data "kala_cases" "archived" {
  active = false
}

output "case_counts" {
  value = {
    active   = length(data.kala_cases.active.cases)
    archived = length(data.kala_cases.archived.cases)
  }
}

output "case_lists_are_complete" {
  value = {
    active   = data.kala_cases.active.complete
    archived = data.kala_cases.archived.complete
  }
}

# Merging the two sets is the caller's job, deliberately.
output "all_cases" {
  value = [
    for c in concat(data.kala_cases.active.cases, data.kala_cases.archived.cases) :
    { id = c.id, number = c.number, name = c.name, archived = c.archived }
  ]
}

# ---------------------------------------------------------------------------
# One case, in full
# ---------------------------------------------------------------------------
#
# The list returns roughly 25 fields; the detail endpoint returns 64. Anything
# past identity and customer name has to come from here.

data "kala_case" "one" {
  case_number = "KA-1" # the STRING number, not the integer id

  # Off by default. Financials are commercially sensitive AND transactional.
  # include_financials      = true
  # include_contact_details = true
}

output "case_detail" {
  value = {
    id          = data.kala_case.one.id
    customer_id = data.kala_case.one.customer_id
    is_finished = data.kala_case.one.is_finished
    tasks       = "${data.kala_case.one.checklist_items_completed}/${data.kala_case.one.checklist_items_total}"
  }
}
