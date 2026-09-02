# Set exactly one of id, number, or cvr.
#
# `cvr` and `number` are narrowed upstream before matching, so they do not read
# the whole account. `id` cannot be — Kala's query parameter searches text — so
# an id lookup reads the list and selects from it.

data "kala_customer" "by_cvr" {
  cvr = "12345678"
}

data "kala_customer" "by_number" {
  number = "K-001"
}

data "kala_customer" "by_id" {
  id = 1
}
