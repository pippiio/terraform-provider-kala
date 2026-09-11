# A Kala "task" is a checklist item on a case.
#
# `case_id` is REQUIRED: Kala addresses checklist items by case and has no
# account-wide task endpoint.

data "kala_tasks" "for_case" {
  case_id = 1

  # Applied upstream:
  # search          = "roof"
  # name_contains   = "gutter"
  # only_unfinished = true

  # Applied client-side — Kala has no assignee parameter. This narrows the
  # RESULT without reducing what was read, so `complete` still describes the
  # underlying read rather than the filtered list.
  # assignee_worker_nr = 1
}

# Tasks across several cases are a for_each composition, not a provider fan-out.
data "kala_cases" "active" {}

data "kala_tasks" "per_case" {
  for_each = { for c in data.kala_cases.active.cases : tostring(c.id) => c }

  case_id         = each.value.id
  only_unfinished = true
}
