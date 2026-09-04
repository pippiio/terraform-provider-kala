# A new hire. employee_number is chosen by you — Kala does not allocate it.
resource "kala_employee" "carpenter" {
  employee_number = 101
  name            = "Sofie Kjær"
  email           = "sofie.kjaer@example.com"

  title              = "Tømrer"
  department         = "Montage"
  initials           = "SKJ"
  phone              = "+45 20 00 00 01"
  date_of_employment = "2026-04-01"

  # Reports to the site manager below.
  boss_employee_number = kala_employee.site_manager.employee_number
}

# A leader. is_leader unlocks Kala's leader views; is_planner and is_finance are
# separate rights and are granted independently.
resource "kala_employee" "site_manager" {
  employee_number = 100
  name            = "Anders Holm"
  email           = "anders.holm@example.com"

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
  employee_number = 102
  name            = "Mette Lund"
  email           = "mette.lund@example.com"
  active          = false
}

# Re-onboarding. Employee 103 belonged to someone who left years ago, and Kala
# cannot delete employees, so the number is still in use. Create ADOPTS that
# existing record and reactivates it rather than failing or creating a second
# person — which is what makes re-hiring work at all.
#
# No welcome email is sent on adoption or reactivation.
resource "kala_employee" "returning" {
  employee_number = 103
  name            = "Mette Lund"
  email           = "mette.lund@example.com"
  active          = true
}

# A migration of people who already exist in Kala. send_welcome_email = false
# matters here: mail reaches a real person and cannot be recalled, and these
# employees have been working for years.
locals {
  office_staff = {
    bech = {
      number  = 110
      name    = "Jonas Bech"
      email   = "jonas.bech@example.com"
      title   = "Bogholder"
      finance = true
    }
    sorensen = {
      number  = 111
      name    = "Line Sørensen"
      email   = "line.sorensen@example.com"
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
