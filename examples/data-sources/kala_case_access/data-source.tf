# Who is granted access to a case, and who is assigned to its tasks.
#
# Kala keeps these as two different lists. Being assigned to a task does not
# grant access: on a restricted case, an employee assigned but not granted is
# assigned to tasks they cannot see.

data "kala_case_access" "roof" {
  case_number = kala_case.roof.number # the STRING number, not the integer id
}

output "roof_access" {
  value = {
    restricted = data.kala_case_access.roof.restricted
    granted    = data.kala_case_access.roof.granted_employee_numbers
    assigned   = data.kala_case_access.roof.assigned_employee_numbers
  }
}

# The rule, enforced: on a restricted case every assigned employee must also be
# granted access. assigned_without_access is always empty on an unrestricted
# case, so this check only ever fires where the rule applies.
check "roof_assignees_have_access" {
  assert {
    condition     = length(data.kala_case_access.roof.assigned_without_access) == 0
    error_message = "Assigned without access on case ${data.kala_case_access.roof.case_number}: employee number(s) ${join(", ", data.kala_case_access.roof.assigned_without_access)}. They cannot see the tasks they are assigned to."
  }
}
