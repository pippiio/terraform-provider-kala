data "kala_employees" "all" {}

# Names and numbers only. The full employee objects include phone numbers and
# settings — personal data that does not belong in CI logs by default.
output "employee_names" {
  value = [
    for e in data.kala_employees.all.employees :
    { number = e.number, name = e.name }
  ]
}
