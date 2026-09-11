data "kala_customers" "all" {
  # search    = "baggins"   # filtered upstream
  # page_size = 200

  # Contact details are personal data and everything a data source exposes is
  # written to Terraform state, so they are withheld unless requested.
  # include_contact_details = true
}

# Check this before treating the list as the whole account: when the pagination
# cap is reached, `customers` is a subset and the provider also emits a warning.
output "complete" {
  value = data.kala_customers.all.complete
}
