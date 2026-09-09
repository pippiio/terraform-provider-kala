# Kala assigns work by linking an employee to a CASE, then scoping that link to
# a set of checklist items. The link is SHARED: an employee working on two
# tasks of the same case has ONE link covering both.
#
# So declare exactly ONE of these per (case, employee) pair. Two resources for
# the same pair would overwrite each other's membership on every apply, each
# reverting the other. That sharing is why assignment is its own resource
# rather than an attribute on kala_task.

resource "kala_task_assignment" "gimli" {
  case_number   = kala_case.roof.number
  worker_number = 101

  # The COMPLETE set for this pair. An id removed from this list is detached on
  # the next apply.
  task_ids = [
    kala_task.mount_gutter.id,
    kala_task.checklist["brackets"].id,
  ]
}

# A second employee on the same case is a second resource, not a second entry.
resource "kala_task_assignment" "samwise" {
  case_number   = kala_case.roof.number
  worker_number = 102

  task_ids = [kala_task.checklist["test"].id]
}

# `terraform destroy` detaches the employee from every task the link covers.
# Kala exposes no way to remove the link itself, so it remains with nothing
# assigned to it — harmless, and reused if the resource is added back.
