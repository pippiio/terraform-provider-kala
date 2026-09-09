# The import ID is the case number and the checklist item id, separated by a
# colon. Both halves are required and neither is derivable from the other: the
# id identifies the item, and the case number is what Kala keys its writes on.
terraform import kala_task.mount_gutter KA-4:9

# Updates replace the whole record, so reconcile the configuration with what
# Kala holds before applying — an attribute the configuration omits is blanked.
