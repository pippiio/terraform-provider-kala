# The import ID is the case NUMBER — the string Kala shows, not the integer id.
# Writes address a case by its number; only reads use the id.
terraform import kala_case.roof KA-4

# worker_number cannot be recovered: Kala does not report who created a case.
# Set it explicitly afterwards. It does not force replacement, precisely so
# that an imported case is not destroyed and recreated — which would archive
# the real case and leave a duplicate.
