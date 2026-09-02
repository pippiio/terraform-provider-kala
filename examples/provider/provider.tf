# Customers, cases, and tasks are served ONLY by Kala's internal app API, which
# authenticates with a username and password rather than the api_key. Employees
# and settings use the api_key. Both come from the environment:
#
#   export KALA_API_KEY=...    # webapiv2 — employees and settings
#   export KALA_USERNAME=...   # internal API — customers, cases, tasks
#   export KALA_PASSWORD=...
provider "kala" {}
