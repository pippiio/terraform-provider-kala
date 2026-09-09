package provider

import (
	"fmt"
	"os"
)

// envVarForAttribute maps a provider attribute to the environment variable that
// can supply it.
//
// Environment fallback is mandatory: making credentials
// Required in the schema forces them into .tf or .tfvars files, both of which
// tend to reach version control. The reference prototype made exactly this
// mistake.
var envVarForAttribute = map[string]string{
	"KALA_API_KEY":  "api_key",
	"KALA_COMPANY":  "company",
	"KALA_ENDPOINT": "endpoint",
	"KALA_USERNAME": "username",
	"KALA_PASSWORD": "password",
}

// resolveCredential returns configured if non-empty, else the value of envVar.
//
// It returns an error naming BOTH sources when neither supplies a value, so the
// user is not left guessing which one the provider actually reads.
func resolveCredential(configured, envVar string) (string, error) {
	if configured != "" {
		return configured, nil
	}

	if v := os.Getenv(envVar); v != "" {
		return v, nil
	}

	attr, ok := envVarForAttribute[envVar]
	if !ok {
		attr = "the corresponding provider attribute"
	}

	return "", fmt.Errorf(
		"no value found: set the %q attribute in the provider block, or export %s. "+
			"Prefer the environment variable so the credential stays out of version control",
		attr, envVar,
	)
}
