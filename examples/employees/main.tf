terraform {
  required_providers {
    kala = {
      source = "registry.terraform.io/techchapter/kala"
    }
  }
}

# The api_key is deliberately omitted here. The provider reads KALA_API_KEY from
# the environment, which keeps the credential out of version control:
#
#   export KALA_API_KEY=...
#
# Setting it in this block would work, but pushes the secret into a .tf or
# .tfvars file — both of which tend to get committed.
provider "kala" {
  # endpoint = "https://app.kala.dk/webapiv2"  # the default
  # company  = 42                              # or KALA_COMPANY

  # Terraform runs provider configuration for `validate` and `plan` as well as
  # `apply`, so the credential check needs network reachability every time.
  # Set this in credential-less CI jobs that only validate:
  # skip_credential_validation = true
}

data "kala_employees" "all" {}

output "employee_count" {
  value = length(data.kala_employees.all.employees)
}

# Names and numbers only. The full employee objects include phone numbers and
# settings — personal data that does not belong in CI logs by default.
output "employee_names" {
  value = [
    for e in data.kala_employees.all.employees :
    { number = e.number, name = e.name }
  ]
}

output "leaders" {
  value = [
    for e in data.kala_employees.all.employees :
    e.name if e.is_leader
  ]
}
