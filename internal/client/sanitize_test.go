package client

import (
	"errors"
	"strings"
	"testing"
)

// The Kala API takes credentials as *query parameters*, so any code path that
// reports a request URL — a wrapped error, a log line — leaks the key unless it
// is redacted first. These tests pin that.

const secret = "super-secret-key-value"

func TestSanitizeURL_RedactsAPIKeyParam(t *testing.T) {
	got := sanitizeURL("https://app.kala.dk/webapiv2/Ping?api_key=" + secret)

	if strings.Contains(got, secret) {
		t.Fatalf("api_key leaked in sanitized URL: %q", got)
	}
	if !strings.Contains(got, "api_key=REDACTED") {
		t.Errorf("want api_key=REDACTED, got %q", got)
	}
}

func TestSanitizeURL_RedactsAlternateSpelling(t *testing.T) {
	// The Index endpoint spells it "apikey" while every other endpoint uses
	// "api_key". Redaction must not depend on which one is in play.
	got := sanitizeURL("https://app.kala.dk/webapiv2/Index?company=42&apikey=" + secret)

	if strings.Contains(got, secret) {
		t.Fatalf("apikey leaked in sanitized URL: %q", got)
	}
	if !strings.Contains(got, "company=42") {
		t.Errorf("non-sensitive params must survive redaction, got %q", got)
	}
}

func TestSanitizeURL_PreservesNonSensitiveParams(t *testing.T) {
	got := sanitizeURL("https://app.kala.dk/webapiv2/ActiveEmployeesList?api_key=" + secret + "&page=3&page_size=100")

	for _, want := range []string{"page=3", "page_size=100"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q preserved, got %q", want, got)
		}
	}
	if strings.Contains(got, secret) {
		t.Fatalf("api_key leaked: %q", got)
	}
}

func TestSanitizeURL_HandlesNoQueryString(t *testing.T) {
	in := "https://app.kala.dk/webapiv2/Ping"
	if got := sanitizeURL(in); got != in {
		t.Errorf("URL without query should pass through unchanged: got %q want %q", got, in)
	}
}

func TestSanitizeURL_HandlesUnparseableInput(t *testing.T) {
	// Must fail closed: if the URL cannot be parsed we cannot prove the key is
	// absent, so return a placeholder rather than the raw string.
	got := sanitizeURL("://not a url?api_key=" + secret)

	if strings.Contains(got, secret) {
		t.Fatalf("unparseable URL leaked the key: %q", got)
	}
}

func TestSanitizeError_RedactsKeyInWrappedError(t *testing.T) {
	// Errors are the most likely leak path: wrapping a failure with the request
	// URL is the natural thing to write, and it would expose the credential.
	wrapped := sanitizeError(errors.New("Get \"https://app.kala.dk/webapiv2/Ping?api_key=" + secret + "\": dial tcp: timeout"))

	if strings.Contains(wrapped.Error(), secret) {
		t.Fatalf("api_key leaked through error: %q", wrapped.Error())
	}
	if !strings.Contains(wrapped.Error(), "REDACTED") {
		t.Errorf("want REDACTED marker in error, got %q", wrapped.Error())
	}
}

func TestSanitizeError_NilPassesThrough(t *testing.T) {
	if got := sanitizeError(nil); got != nil {
		t.Errorf("sanitizeError(nil) = %v, want nil", got)
	}
}

func TestSanitizeError_PreservesErrorsIsChain(t *testing.T) {
	// Redaction must not destroy error identity — callers still need errors.Is.
	sentinel := errors.New("boom")
	got := sanitizeError(sentinel)

	if !errors.Is(got, sentinel) {
		t.Errorf("sanitizeError broke the errors.Is chain for %v", sentinel)
	}
}

// redactJSONValues exists because a real leak was found without it: the
// sign-in body carries the password, and an upstream error echoing the request
// put it verbatim into an error message (SEC1.3).
func TestRedactSensitiveValues_JSONBodies(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			name:   "sign-in body",
			in:     `{"username":"user@example.com","password":"hunter2","appType":"web"}`,
			absent: []string{"user@example.com", "hunter2"},
			// Non-sensitive fields survive: the detail is still useful for debugging.
			present: []string{"appType", "web", redactedPlaceholder},
		},
		{
			name:    "whitespace after the colon",
			in:      `{"password": "hunter2"}`,
			absent:  []string{"hunter2"},
			present: []string{redactedPlaceholder},
		},
		{
			name:    "bare literal value",
			in:      `{"token":12345,"other":1}`,
			absent:  []string{"12345"},
			present: []string{"other", redactedPlaceholder},
		},
		{
			name:    "kauthtoken is caught in its own right",
			in:      `{"kauthtoken":"9001:4242;abc="}`,
			absent:  []string{"9001:4242"},
			present: []string{redactedPlaceholder},
		},
		{
			name:    "truncated body still redacts",
			in:      `{"username":"user@example.com","password":"hunter`,
			absent:  []string{"user@example.com"},
			present: []string{redactedPlaceholder},
		},
		{
			name:    "already redacted is left alone",
			in:      `{"password":"` + redactedPlaceholder + `"}`,
			present: []string{redactedPlaceholder},
		},
		{
			name:    "both wire shapes in one string",
			in:      `api_key=secret1 {"password":"secret2"}`,
			absent:  []string{"secret1", "secret2"},
			present: []string{redactedPlaceholder},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSensitiveValues(tc.in)
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("%q survived redaction: %s", a, got)
				}
			}
			for _, p := range tc.present {
				if !strings.Contains(got, p) {
					t.Errorf("%q missing from output: %s", p, got)
				}
			}
		})
	}
}

// Malformed and adversarial inputs: redaction must fail closed rather than
// panic or silently pass a secret through. The input is an error-body sample,
// so truncation and invalid JSON are the normal case, not the exception.
func TestRedactJSONValues_MalformedInput(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		absent []string
	}{
		{name: "key with no colon", in: `{"password" "hunter2"}`, absent: nil},
		{name: "key at end of string", in: `{"password"`, absent: nil},
		{name: "colon then end of string", in: `{"password":`, absent: nil},
		{name: "escaped quote inside value", in: `{"password":"hun\"ter2","x":1}`, absent: []string{"hun"}},
		{name: "unterminated string value", in: `{"password":"hunter2`, absent: []string{"hunter2"}},
		{name: "empty string value", in: `{"password":""}`, absent: nil},
		{name: "key appears as a value", in: `{"note":"password"}`, absent: nil},
		{name: "repeated sensitive keys", in: `{"password":"a","token":"b","password":"c"}`, absent: []string{`"a"`, `"b"`, `"c"`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redactJSONValues(tc.in) // must not panic
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("%q survived redaction: %s", a, got)
				}
			}
		})
	}
}

func TestRedactSensitiveValues_LeavesUnrelatedJSONIntact(t *testing.T) {
	in := `{"caseName":"Roof works","statusName":"Færdig","priceFixed":550}`
	if got := redactSensitiveValues(in); got != in {
		t.Errorf("unrelated JSON was altered:\n got %s\nwant %s", got, in)
	}
}
