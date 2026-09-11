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

# Filter by customer company. Applied client-side over the case list, which
# carries the company as text but no customer id — so this is string matching,
# not a join. Pair it with kala_customer when you need the id.
data "kala_cases" "for_customer" {
  customer_company = "Bag End Ltd"
}

# An explicit empty string selects cases with no customer, such as internal
# projects. Leaving it unset applies no filter at all.
data "kala_cases" "internal_projects" {
  customer_company = ""
}
