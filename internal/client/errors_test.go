package client

import (
	"errors"
	"testing"
)

// The API documents exactly one status code (401). Everything else is
// classified by status class, never by parsing an unknown error body.

func TestClassifyStatus_MapsToSentinels(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"401 unauthorized", 401, ErrUnauthorized},
		{"404 not found", 404, ErrNotFound},
		{"400 bad request", 400, ErrClientRequest},
		{"403 forbidden", 403, ErrClientRequest},
		{"429 too many requests", 429, ErrClientRequest},
		{"500 internal", 500, ErrServer},
		{"502 bad gateway", 502, ErrServer},
		{"503 unavailable", 503, ErrServer},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyStatus(tc.status, "")
			if !errors.Is(got, tc.want) {
				t.Errorf("classifyStatus(%d) = %v, want errors.Is(_, %v)", tc.status, got, tc.want)
			}
		})
	}
}

func TestClassifyStatus_SuccessIsNotAnError(t *testing.T) {
	for _, status := range []int{200, 201, 204, 302} {
		if got := classifyStatus(status, ""); got != nil {
			t.Errorf("classifyStatus(%d) = %v, want nil", status, got)
		}
	}
}

func TestClassifyStatus_CarriesStatusCode(t *testing.T) {
	err := classifyStatus(503, "upstream down")

	var se *statusError
	if !errors.As(err, &se) {
		t.Fatalf("classifyStatus did not produce a *statusError: %v", err)
	}
	if se.StatusCode() != 503 {
		t.Errorf("StatusCode() = %d, want 503", se.StatusCode())
	}
	if se.Error() == "" {
		t.Error("Error() must not be empty")
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		// Retrying a 4xx cannot help — the request itself is wrong — and only
		// amplifies load against an API whose rate limits are undocumented.
		{"401 not retryable", classifyStatus(401, ""), false},
		{"404 not retryable", classifyStatus(404, ""), false},
		{"400 not retryable", classifyStatus(400, ""), false},
		{"429 not retryable", classifyStatus(429, ""), false},
		{"500 retryable", classifyStatus(500, ""), true},
		{"503 retryable", classifyStatus(503, ""), true},
		{"transport retryable", ErrTransport, true},
		{"decode not retryable", ErrDecode, false},
		{"nil not retryable", nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryable(tc.err); got != tc.want {
				t.Errorf("isRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestStatusError_DetailIsIncludedWhenPresent(t *testing.T) {
	withDetail := classifyStatus(500, "boom")
	if got := withDetail.Error(); got == ErrServer.Error() {
		t.Errorf("detail should be appended, got %q", got)
	}

	without := classifyStatus(500, "")
	if got := without.Error(); got != ErrServer.Error() {
		t.Errorf("no detail should render the bare sentinel, got %q", got)
	}
}
