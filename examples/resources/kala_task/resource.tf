# A task is a checklist item on a case. It cannot exist without one, and it
# cannot be moved between cases — changing case_number replaces the resource,
# which creates a new item and strands the old one.

resource "kala_task" "mount_gutter" {
  case_number = kala_case.roof.number
  name        = "Mount gutter, north side"

  description = "Use the 120mm brackets from the depot."

  # SECOND precision. Kala does not round-trip finer: the create response
  # echoes what it was given, but the list read returns it a few milliseconds
  # later, so a sub-second value would produce a permanent diff.
  deadline = "2026-09-30T15:00:00Z"

  note_required  = true
  image_required = true

  invoice_mode = "REG_HOURS&STANDARD"
  price_fixed  = 500
}

# This resource manages what the work IS, not whether it is done.
#
# is_finished is read-only. A worker ticking the task off in Kala produces no
# diff — if Terraform managed completion it would revert their work on the next
# apply, which is a fight it would keep winning.
output "progress" {
  value = {
    task     = kala_task.mount_gutter.name
    finished = kala_task.mount_gutter.is_finished
  }
}

# A checklist of tasks on one case.
resource "kala_task" "checklist" {
  for_each = {
    strip    = "Strip the old gutter"
    brackets = "Fit new brackets"
    test     = "Water test and photograph"
  }

  case_number = kala_case.roof.number
  name        = each.value
}
