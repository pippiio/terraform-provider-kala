package provider

import (
	"os"
	"testing"
)

// kalaEnv is every variable the provider reads as a fallback for its own
// configuration (see credentials.go and provider.go).
var kalaEnv = []string{
	"KALA_API_KEY", "KALA_USERNAME", "KALA_PASSWORD", "KALA_ENDPOINT", "KALA_COMPANY",
}

// TestMain makes the unit tests hermetic against the developer's environment.
//
// Tests here configure the provider with some attributes null, and the provider
// then falls back to KALA_* environment variables. With a developer's real
// variables exported -- a .envrc, which .gitignore anticipates -- six tests
// went looking for a Kala tenant behind an httptest mock and failed. CI never
// saw it, because CI runs with the variables cleared; the suite only ever
// passed in exactly the environment CI provides.
//
// Acceptance tests live in this package and NEED the real variables, so they
// are left alone when TF_ACC is set. Otherwise every KALA_* is unset before any
// test runs, and a test that wants one sets it itself with t.Setenv.
func TestMain(m *testing.M) {
	if os.Getenv("TF_ACC") == "" {
		for _, name := range kalaEnv {
			_ = os.Unsetenv(name) // an unset variable cannot fail to unset in a way that matters here
		}
	}
	os.Exit(m.Run())
}
