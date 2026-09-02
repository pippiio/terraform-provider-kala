# Set exactly one of id or name. `name` is narrowed upstream via nameContains,
# then matched exactly; two matches is an error rather than a silent first-wins.

data "kala_task" "by_name" {
  case_id = 1
  name    = "Mount gutter"
}

data "kala_task" "by_id" {
  case_id = 1
  id      = 42
}
