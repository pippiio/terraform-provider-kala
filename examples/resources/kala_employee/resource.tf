# A tradesperson. employee_number is chosen by you — Kala does not allocate it.
resource "kala_employee" "smith" {
  employee_number = 101
  name            = "Gimli"
  email           = "gimli@example.com"

  title              = "Smed"
  department         = "Montage"
  initials           = "GIM"
  phone              = "+45 20 00 00 01"
  date_of_employment = "2026-04-01"

  # Reports to the site manager below.
  boss_employee_number = kala_employee.site_manager.employee_number
}

# A leader. is_leader unlocks Kala's leader views; is_planner and is_finance are
# separate rights and are granted independently.
resource "kala_employee" "site_manager" {
  employee_number = 100
  name            = "Aragorn Elessar"
  email           = "aragorn@example.com"

  title      = "Byggeleder"
  department = "Montage"
  is_leader  = true
  is_planner = true

  leader_note = "Managed by Terraform"
}

# Offboarding. Kala has no delete endpoint, so destroying this resource — or
# setting active = false — deactivates the employee and leaves their record,
# registered hours, and history intact.
resource "kala_employee" "departed" {
  employee_number = 120
  name            = "Boromir"
  email           = "boromir@example.com"
  active          = false
}

# Re-hiring. Employee 121 belonged to someone who left, and Kala cannot delete
# employees, so the number is still in use. Create ADOPTS that record and
# reactivates it rather than failing or creating a second person — which is what
# makes re-onboarding work at all.
#
# No welcome email is sent on adoption or reactivation.
resource "kala_employee" "returning" {
  employee_number = 121
  name            = "Samwise Gamgee"
  email           = "samwise@example.com"
  active          = true
}

# A migration of people who already exist in Kala. send_welcome_email = false
# matters here: mail reaches a real person and cannot be recalled, and these
# employees have been working for years.
locals {
  office_staff = {
    bilbo = {
      number  = 110
      name    = "Bilbo Baggins"
      email   = "bilbo@example.com"
      title   = "Bogholder"
      finance = true
    }
    galadriel = {
      number  = 111
      name    = "Galadriel"
      email   = "galadriel@example.com"
      title   = "Projektkoordinator"
      finance = false
    }
  }
}

resource "kala_employee" "office" {
  for_each = local.office_staff

  employee_number    = each.value.number
  name               = each.value.name
  email              = each.value.email
  title              = each.value.title
  department         = "Kontor"
  is_finance         = each.value.finance
  send_welcome_email = false
}

# adopted tells you whether a resource provisioned someone or inherited them.
output "inherited_rather_than_created" {
  value = [for k, e in kala_employee.office : k if e.adopted]
}
