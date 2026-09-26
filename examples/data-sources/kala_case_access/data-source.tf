# Which employees are granted access to a case — and therefore may register
# time on it.
#
# READ THIS BEFORE USING THE RESULT. The set is derived from task assignment,
# because Kala exposes nothing else. A case with no tasks ALWAYS reports an
# empty set, however many people have access. It is a reporting aid for
# reviewing configuration, not an authorization check.

data "kala_case_access" "roof" {
  case_number = kala_case.roof.number # the STRING number, not the integer id
}

# Read restricted and employee_numbers together. restricted = false means the
# case is unrestricted and the set says nothing. restricted = true with an
# empty set is ambiguous: nobody granted, or nobody granted holds a task.
output "roof_access" {
  value = {
    restricted       = data.kala_case_access.roof.restricted
    employee_numbers = data.kala_case_access.roof.employee_numbers
  }
}

# The set is named to join. It holds who is GRANTED access, not who is able
# to register: a deactivated employee may remain in it and will not resolve,
# so read-only lookups like this one are the safe way to iterate it.
data "kala_employees" "all" {}

locals {
  granted_names = [
    for e in data.kala_employees.all.employees : e.name
    if contains(data.kala_case_access.roof.employee_numbers, e.number)
  ]
}
