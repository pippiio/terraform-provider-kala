# The import ID is the case number and the employee number, separated by a
# colon — the same form as kala_task_assignment.
terraform import kala_case_access.gimli_roof KA-4:101

# An employee who already has access is adopted without a write, so importing is
# never required to start managing an existing grant — but it avoids a plan that
# shows a create for access that already exists.
