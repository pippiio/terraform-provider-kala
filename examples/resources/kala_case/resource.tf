# A case is either customer-facing or internal — never both, never neither.
# customer_number is required when internal_project is false and must be
# omitted when it is true. That is checked at PLAN time, because upstream they
# are two differently shaped requests rather than one with a blank field.

resource "kala_case" "roof" {
  name            = "Roof replacement, Bagshot Row"
  customer_number = kala_customer.bag_end.number
  worker_number   = 1 # the employee creating it; used only at creation

  address = "Bagshot Row 1"
  zip     = "2200"

  # The contact for THIS JOB — not the customer's own phone number. Changing
  # it does not touch kala_customer.
  contact_phone = "+45 20 00 00 02"
}

# An internal project carries no customer at all.
resource "kala_case" "spring_cleaning" {
  name             = "Yard tidy-up"
  internal_project = true
  worker_number    = 1

  address = "Depot"
}

# `terraform destroy` ARCHIVES a case rather than only forgetting it — archival
# is reversible and verifiable, which is why destroy does something real here
# and does not for kala_customer. The case, its checklist items, and its
# registered hours all remain.
#
# Archiving can also be done in place, without destroying the resource:
resource "kala_case" "finished_last_year" {
  name            = "Gutter job 2025"
  customer_number = kala_customer.bag_end.number
  worker_number   = 1
  archived        = true
}
