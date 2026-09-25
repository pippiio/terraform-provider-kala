# The import ID is the employee number.
terraform import kala_employee.smith 101

# Every attribute on Kala's record is recovered, including name and email.
# send_welcome_email is not part of that record — it describes what Terraform
# should do at creation — so it defaults to false and an imported employee is
# never mailed.
