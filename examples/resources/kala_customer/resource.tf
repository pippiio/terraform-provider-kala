# Creating a customer is IRREVERSIBLE. Kala has no delete endpoint for
# customers and no deactivation flag, so `terraform destroy` removes this
# resource from state and leaves the record in Kala forever.
#
# id and number are allocated by Kala, never chosen, so applying this twice
# creates two customers. Adopt an existing one with `terraform import`.
resource "kala_customer" "bag_end" {
  company = "Bag End Ltd" # required: a customer with no company is indistinguishable from an empty record

  first_name = "Bilbo"
  last_name  = "Baggins"
  email      = "bilbo@example.com"
  phone      = "+45 20 00 00 01"
  address    = "Bagshot Row 1"
  zip        = "2200"
  cvr        = "12345678"

  description = "Managed by Terraform"
}

# Updates REPLACE the whole record upstream. Every attribute this resource
# manages is sent on every apply, so a field removed from the configuration is
# blanked in Kala rather than left alone.
#
# That is the opposite of kala_case, where each field has its own endpoint.

output "allocated_identity" {
  value = {
    id     = kala_customer.bag_end.id
    number = kala_customer.bag_end.number # e.g. "KA-1"
  }
}
