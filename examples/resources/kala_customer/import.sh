# The import ID is Kala's numeric customer id — not the "KA-1" number, which
# is a different value.
terraform import kala_customer.bag_end 4

# Import is the ONLY way to adopt an existing customer: create allocates a new
# one rather than matching, and the result cannot be deleted.
#
# Reconcile the configuration with what Kala holds BEFORE the next apply.
# Updates replace the whole record, so an attribute the configuration omits is
# blanked upstream.
