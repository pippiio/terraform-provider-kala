# `active` selects a SET rather than narrowing one. Kala's archived and
# non-archived cases are DISJOINT, and no single call returns both, so reading
# everything takes two blocks.

data "kala_cases" "active" {
  # active = true   # the default
}

data "kala_cases" "archived" {
  active = false
}

output "all_cases" {
  value = concat(data.kala_cases.active.cases, data.kala_cases.archived.cases)
}
