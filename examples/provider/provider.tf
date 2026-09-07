# Kala is two APIs, and which one a data source or resource uses decides which
# credentials it needs.
#
#   export KALA_API_KEY=...    # webapiv2 — kala_employees only
#   export KALA_USERNAME=...   # internal app API — the kala_employee resource,
#   export KALA_PASSWORD=...   #   plus every customer, case, and task read
#
# Reading the environment keeps the credentials out of version control. Setting
# api_key, username, or password in this block works, but writes the secret into
# a .tf or .tfvars file — both of which tend to get committed.
provider "kala" {
  # --- Which tenant is written to -------------------------------------------
  #
  # A Kala login can belong to several companies. Omit this when yours belongs
  # to exactly one; when it belongs to several the provider refuses to guess,
  # rather than writing to whichever Kala happens to list first.
  #
  # company = 4242   # or KALA_COMPANY

  # --- Transport ------------------------------------------------------------
  #
  # endpoint        = "https://app.kala.dk/webapiv2"   # the default
  # max_retries     = 3    # 5xx and transport errors only; 4xx is never retried
  # timeout_seconds = 30   # per request

  # --- Credential-less CI ---------------------------------------------------
  #
  # Terraform configures the provider for `validate` and `plan` as well as
  # `apply`, so the credential check needs network reachability every time. Set
  # this in jobs that only validate configuration and hold no credentials.
  #
  # skip_credential_validation = true
}
