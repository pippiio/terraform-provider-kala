# The import ID is the case number and the employee number, separated by a
# colon. The pair IS the identity: a job link is one employee on one case.
terraform import kala_task_assignment.gimli KA-4:101

# task_ids is discovered on the next read, from which checklist items name that
# employee. job_link_id is not recoverable on import and is resolved on the
# next apply.
