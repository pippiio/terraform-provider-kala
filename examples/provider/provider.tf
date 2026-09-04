# Customers, cases, and tasks are served ONLY by Kala's internal app API, which
# authenticates with a username and password rather than the api_key. Employee
# reads use the api_key. Both come from the environment:
#
#   export KALA_API_KEY=...    # webapiv2 — employee reads
#   export KALA_USERNAME=...   # internal API — customers, cases, tasks
#   export KALA_PASSWORD=...
provider "kala" {}
