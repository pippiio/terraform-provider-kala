# The case list returns roughly 25 fields; this endpoint returns 64. Anything
# past identity and customer name has to come from here.

data "kala_case" "one" {
  case_number = "KA-1" # the STRING number, not the integer id

  # Off by default. Financials are commercially sensitive AND transactional.
  # include_financials      = true
  # include_contact_details = true
}
