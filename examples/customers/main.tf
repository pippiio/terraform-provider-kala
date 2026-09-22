terraform {
  required_providers {
    kala = {
      source = "registry.terraform.io/pippiio/kala"
    }
  }
}

# Customers, cases, and tasks are served ONLY by Kala's internal app API, which
# authenticates with a username and password rather than the api_key. Both
# credentials come from the environment:
#
#   export KALA_API_KEY=...    # webapiv2 — employee reads
#   export KALA_USERNAME=...   # internal API — customers, cases, tasks
#   export KALA_PASSWORD=...
provider "kala" {}

# ---------------------------------------------------------------------------
# Listing
# ---------------------------------------------------------------------------

data "kala_customers" "all" {
  # search = "baggins"   # filtered upstream
  # page_size = 200

  # Contact details are personal data and everything a data source exposes is
  # written to Terraform state. They are withheld unless you ask:
  # include_contact_details = true
}

# ALWAYS check this before treating the list as the whole account. When the
# pagination cap is reached, `customers` is a subset and the provider also
# emits a warning.
output "customer_list_is_complete" {
  value = data.kala_customers.all.complete
}

output "customer_count" {
  value = "${length(data.kala_customers.all.customers)} of ${data.kala_customers.all.total}"
}

# Identifiers and company names only — no contact data in outputs by default.
output "customers" {
  value = [
    for c in data.kala_customers.all.customers :
    { id = c.id, number = c.number, company = c.company }
  ]
}

# ---------------------------------------------------------------------------
# Looking one up
# ---------------------------------------------------------------------------

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

output "resolved_customer" {
  value = {
    id      = data.kala_customer.by_cvr.id
    number  = data.kala_customer.by_cvr.number
    company = data.kala_customer.by_cvr.company
  }
}
