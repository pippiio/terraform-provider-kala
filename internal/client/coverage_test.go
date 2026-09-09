package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Fills the remaining coverage gaps: the public constructor, the sanitizedError
// unwrap chain, boundary cases in redaction, and the transport paths that the
// happy-path tests do not reach.

func TestNew_ReturnsAConfiguredClient(t *testing.T) {
	c := New(Config{APIKey: "k"})
	if c == nil {
		t.Fatal("New returned nil")
	}

	impl, ok := c.(*webAPIv2)
	if !ok {
		t.Fatalf("New returned %T, want *webAPIv2", c)
	}
	if impl.cfg.Endpoint != DefaultEndpoint {
		t.Errorf("Endpoint = %q, want the default %q", impl.cfg.Endpoint, DefaultEndpoint)
	}
	if impl.cfg.MaxRetries != 0 {
		t.Errorf("MaxRetries = %d, want 0 preserved from config", impl.cfg.MaxRetries)
	}
}

func TestSanitizedError_UnwrapReturnsCause(t *testing.T) {
	cause := errors.New("dial failed for api_key=leak-me")

	wrapped := sanitizeError(cause)
	if wrapped == nil {
		t.Fatal("sanitizeError returned nil for a non-nil error")
	}
	if strings.Contains(wrapped.Error(), "leak-me") {
		t.Fatalf("secret survived sanitization: %q", wrapped.Error())
	}

	// Unwrap must still reach the original so errors.Is/As keep working.
	if got := errors.Unwrap(wrapped); !errors.Is(got, cause) {
		t.Errorf("Unwrap() = %v, want the original cause", got)
	}
	if !errors.Is(wrapped, cause) {
		t.Error("errors.Is must traverse the sanitized wrapper")
	}
}

func TestRedactSensitiveValues_Boundaries(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantAbsent string
		wantSubstr string
	}{
		{
			name:       "value at end of string",
			in:         "url?api_key=abc123",
			wantAbsent: "abc123",
			wantSubstr: "REDACTED",
		},
		{
			name:       "already redacted is left alone",
			in:         "url?api_key=REDACTED",
			wantAbsent: "REDACTEDREDACTED",
			wantSubstr: "api_key=REDACTED",
		},
		{
			name: "similar param name is not matched",
			// "xapi_key" must not be treated as "api_key".
			in:         "url?xapi_key=keepme&page=1",
			wantAbsent: "REDACTED",
			wantSubstr: "keepme",
		},
		{
			name:       "empty value",
			in:         "url?api_key=&page=2",
			wantAbsent: "secret",
			wantSubstr: "page=2",
		},
		{
			name:       "multiple sensitive params",
			in:         "url?api_key=one&password=two&page=3",
			wantAbsent: "two",
			wantSubstr: "page=3",
		},
		{
			name:       "no sensitive params",
			in:         "url?page=1&page_size=10",
			wantAbsent: "REDACTED",
			wantSubstr: "page_size=10",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSensitiveValues(tc.in)
			if tc.wantAbsent != "" && strings.Contains(got, tc.wantAbsent) {
				t.Errorf("got %q, must not contain %q", got, tc.wantAbsent)
			}
			if tc.wantSubstr != "" && !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("got %q, want it to contain %q", got, tc.wantSubstr)
			}
		})
	}
}

func TestWithDefaults_NegativeRetriesClampToZero(t *testing.T) {
	c := Config{MaxRetries: -5}.withDefaults()
	if c.MaxRetries != 0 {
		t.Errorf("MaxRetries = %d, want 0", c.MaxRetries)
	}
}

func TestWithDefaults_PreservesSuppliedValues(t *testing.T) {
	custom := &http.Client{Timeout: time.Second}
	c := Config{
		Endpoint:   "https://example.test/api",
		Timeout:    99 * time.Second,
		MaxRetries: 7,
		HTTPClient: custom,
	}.withDefaults()

	if c.Endpoint != "https://example.test/api" {
		t.Errorf("Endpoint overwritten: %q", c.Endpoint)
	}
	if c.Timeout != 99*time.Second {
		t.Errorf("Timeout overwritten: %v", c.Timeout)
	}
	if c.MaxRetries != 7 {
		t.Errorf("MaxRetries overwritten: %d", c.MaxRetries)
	}
	if c.HTTPClient != custom {
		t.Error("HTTPClient overwritten")
	}
}

func TestAttempt_MalformedEndpointIsATransportError(t *testing.T) {
	cfg := testConfig("://invalid-endpoint")
	cfg.MaxRetries = 0
	c := newWebAPIv2(cfg)

	err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("want an error for a malformed endpoint")
	}
	if !errors.Is(err, ErrTransport) {
		t.Errorf("want ErrTransport, got %v", err)
	}
}

func TestListEmployees_InvalidRecordFailsThePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Second record has no identity.
		_, _ = w.Write([]byte(`[{"number":1,"name":"A"},{"name":"B"}]`))
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	_, err := c.ListEmployees(context.Background(), ListOptions{PageSize: 10})

	if !errors.Is(err, ErrDecode) {
		t.Errorf("want ErrDecode, got %v", err)
	}
}

func TestGetEmployee_UndecodableSuccessBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"number": not-json`))
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	_, err := c.GetEmployee(context.Background(), 1)

	if !errors.Is(err, ErrDecode) {
		t.Errorf("want ErrDecode for a 2xx with an undecodable body, got %v", err)
	}
}

func TestSanitizeURL_RedactsAllSensitiveNames(t *testing.T) {
	for _, name := range sensitiveParams {
		in := "https://example.test/x?" + name + "=topsecret"
		if got := sanitizeURL(in); strings.Contains(got, "topsecret") {
			t.Errorf("sensitive param %q was not redacted: %q", name, got)
		}
	}
}
