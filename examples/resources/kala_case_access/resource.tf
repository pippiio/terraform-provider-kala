# Grants one employee access to one case — an entry on the case's access list.
# On a RESTRICTED case, only employees on that list can see it and register
# time on it.
#
# Destroy revokes the access. If someone revokes it in the Kala UI instead, the
# next plan proposes creating this resource again, restoring it.

resource "kala_case_access" "gimli_roof" {
  case_number     = kala_case.roof.number
  employee_number = 101
}

# Access is not assignment. kala_task_assignment refuses a restricted case the
# employee cannot access, so make it wait for the grant.
resource "kala_task_assignment" "gimli" {
  case_number   = kala_case.roof.number
  worker_number = 101
  task_ids      = [kala_task.mount_gutter.id]

  depends_on = [kala_case_access.gimli_roof]
}
