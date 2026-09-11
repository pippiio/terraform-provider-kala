# Worked examples of the kinds of employee you actually end up modelling, and
# the Kala behaviour each one runs into.
#
# Every number here is fictional. Choose numbers deliberately before applying
# anything like this: employee_number is chosen by you rather than allocated by
# Kala, and Kala CANNOT DELETE AN EMPLOYEE. Applying against a number already in
# use adopts that person and writes to their record.

terraform {
  required_providers {
    kala = {
      source = "registry.terraform.io/techchapter/kala"
    }
  }
}

# Creating and configuring employees needs the internal app API, so both
# credentials are required here — unlike the read-only kala_employees example
# next door, which needs only KALA_API_KEY.
#
#   export KALA_API_KEY=...
#   export KALA_USERNAME=...
#   export KALA_PASSWORD=...
#
# If your Kala login belongs to more than one company, set company (or
# KALA_COMPANY) too. The provider will not guess which one to write to.
provider "kala" {
  # company = 4242   # "Rivendell"
}

# --- 1. A tradesperson --------------------------------------------------------
#
# The common case: a new hire with a manager and a start date.

resource "kala_employee" "smith" {
  employee_number = 101
  name            = "Gimli"
  email           = "gimli@example.com"

  title              = "Smed"
  department         = "Montage"
  initials           = "SKJ"
  phone              = "+45 20 00 00 01"
  date_of_employment = "2026-04-01"
  license_plate      = "AB 12 345"

  boss_employee_number = kala_employee.site_manager.employee_number
}

# --- 2. A leader --------------------------------------------------------------
#
# is_leader, is_planner and is_finance are three independent rights, each with
# its own endpoint. Granting one does not imply the others.
#
# is_super_user and is_visible_in_planner are READ-ONLY: Kala exposes no way to
# set them, so they can be read but never configured.

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

# --- 3. Office staff, migrated in bulk ----------------------------------------
#
# These people already exist in Kala and have for years. send_welcome_email =
# false is the important line: mail reaches a real person and cannot be
# recalled, and re-onboarding someone who has worked here for a decade is a
# confusing thing to do to them.

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

  employee_number = each.value.number
  name            = each.value.name
  email           = each.value.email

  title      = each.value.title
  department = "Kontor"
  is_finance = each.value.finance

  send_welcome_email   = false
  boss_employee_number = kala_employee.site_manager.employee_number
}

# --- 4. Offboarding -----------------------------------------------------------
#
# Kala has no delete endpoint. active = false deactivates the person; so does
# `terraform destroy`, which then warns that the record remains. Their hours,
# settings, and history are all kept, and their number stays in use.

resource "kala_employee" "departed" {
  employee_number = 120
  name            = "Boromir"
  email           = "boromir@example.com"
  active          = false
}

# --- 5. Re-hiring -------------------------------------------------------------
#
# Employee 121 belonged to someone who left. Because Kala cannot delete, the
# number is still theirs — so this ADOPTS the existing record and reactivates
# it rather than erroring or creating a second person.
#
# That is what makes re-onboarding possible at all, and it is why "the number is
# already in use" is the normal case rather than a failure. Check the computed
# `adopted` attribute if you need to know which happened.

resource "kala_employee" "returning" {
  employee_number = 121
  name            = "Samwise Gamgee"
  email           = "samwise@example.com"
  active          = true
}

# --- What Kala will not let you change ----------------------------------------
#
# Read-only, because no endpoint exists to set them:
#   private_phone, flex_start_date, norm_hours, is_super_user,
#   is_visible_in_planner
#
# norm_hours is JSON embedded in a string — parse it rather than comparing it:
#   jsondecode(kala_employee.smith.norm_hours).normHours
#
# An employee whose own Kala login belongs to several companies cannot have
# their email changed at all. The provider surfaces Kala's refusal rather than
# reporting a write that did not happen.

output "adopted_rather_than_created" {
  value = {
    for k, e in kala_employee.office : k => e.adopted
  }
}

output "smith_manager" {
  value = kala_employee.smith.boss_name
}

output "smith_norm_hours" {
  value = try(jsondecode(kala_employee.smith.norm_hours).normHours, null)
}
