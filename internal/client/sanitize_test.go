package client

import (
	"errors"
	"strings"
	"testing"
)

// The Kala API takes credentials as *query parameters*, so any code path that
// reports a request URL — a wrapped error, a log line — leaks the key unless it
// is redacted first. These tests pin guardrail SEC1.3 and risk R2.

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
