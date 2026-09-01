terraform {
  required_providers {
    kala = { source = "registry.terraform.io/techchapter/kala" }
  }
}

# Credentials come from KALA_API_KEY. See examples/employees for details.
provider "kala" {}

# Discover which employees exist and which setting keys are already in use.
data "kala_employees" "all" {}

resource "kala_employee_setting" "work_type" {
  employee_number = 4711
  key             = "default_work_type"
  value           = "montage"
  friendly_name   = "Default Work Type"
  # type          = "text"   # optional, defaults to "text"
}

# Introducing a key that exists nowhere on the account requires an explicit
# opt-in. Kala cannot delete settings, so a typo would live on this person's
# record permanently — the guard is there to make that a deliberate choice.
resource "kala_employee_setting" "new_flag" {
  employee_number = 4711
  key             = "beta_ui_enabled"
  value           = "true"
  friendly_name   = "Beta UI Enabled"
  allow_new_key   = true
}

output "existing_setting_keys" {
  description = "Keys already in use across the account — valid without allow_new_key."
  value       = distinct(flatten([
    for e in data.kala_employees.all.employees : [for s in e.settings : s.key]
  ]))
}
