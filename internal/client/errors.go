package client

import (
	"errors"
	"strings"
)

// Sentinel errors for conditions callers must distinguish.
//
// The Kala documentation specifies exactly one status code — 401 for an invalid
// key — and nothing else. Classification is therefore by status *class*, never
// by parsing a response body whose shape is unknown.
var (
	// ErrUnauthorized indicates the credentials were rejected (HTTP 401).
	ErrUnauthorized = errors.New("kala: unauthorized — the API key was rejected")

	// ErrNotFound indicates the requested record does not exist (HTTP 404).
	ErrNotFound = errors.New("kala: not found")

	// ErrClientRequest indicates a non-retryable 4xx other than 401/404.
	ErrClientRequest = errors.New("kala: client request error")

	// ErrServer indicates a retryable 5xx response.
	ErrServer = errors.New("kala: server error")

	// ErrTransport indicates a network-level failure with no HTTP response.
	ErrTransport = errors.New("kala: transport error")

	// ErrDecode indicates a 2xx response whose body could not be decoded into
	// the expected shape. Because the API documents no field types, this is a
	// realistic signal that upstream changed.
	ErrDecode = errors.New("kala: could not decode response")

	// ErrConflict indicates a write Kala refused because the record changed
	// underneath it -- the compare-and-swap check on the case field-setters,
	// which carry the value they expect to replace.
	//
	// It arrives as a 5xx, but it is DETERMINISTIC: retrying cannot change the
	// answer, so it is deliberately excluded from isRetryable.
	ErrConflict = errors.New("kala: the record changed since it was read")
)

// statusError carries an HTTP status alongside a sentinel for errors.Is.
type statusError struct {
	sentinel error
	status   int
	detail   string
}

func (e *statusError) Error() string {
	if e.detail == "" {
		return e.sentinel.Error()
	}
	return e.sentinel.Error() + ": " + e.detail
}

func (e *statusError) Unwrap() error { return e.sentinel }

// StatusCode returns the HTTP status that produced this error, or 0 if the
// failure happened before a response was received.
func (e *statusError) StatusCode() int { return e.status }

// htmlTitle extracts the <title> of an ASP.NET error page.
//
// Kala's error pages put the only useful explanation there and bury it in
// markup. Observed three times: the MVC parameter-binding errors that revealed
// endpoint signatures, and the case setters' compare-and-swap refusal. Without
// this the operator is shown "<!DOCTYPE html>" and learns nothing.
//
// Deliberately a string scan rather than an HTML parse: the input is a bounded,
// frequently truncated sample, and failing to parse must not mean failing to
// explain.
func htmlTitle(detail string) string {
	lower := strings.ToLower(detail)
	open := strings.Index(lower, "<title>")
	if open < 0 {
		return ""
	}
	rest := detail[open+len("<title>"):]
	end := strings.Index(strings.ToLower(rest), "</title>")
	if end < 0 {
		return ""
	}
	return strings.Join(strings.Fields(rest[:end]), " ")
}

// isCompareAndSwapRefusal reports whether a 5xx is Kala refusing a write
// because the value it was asked to replace no longer matches.
//
// Every case field-setter carries the value it expects to overwrite
// (previousCaseName, previousAddress, previousZip, oldPhoneNumber), and Kala
// checks it -- verified 2026-09-08. The refusal is hand-written prose, so it is
// matched on rather than inferred from the status code, which is an unhelpful
// 500.
func isCompareAndSwapRefusal(title string) bool {
	l := strings.ToLower(title)
	return strings.Contains(l, "doesn't match") || strings.Contains(l, "does not match")
}

// classifyStatus maps an HTTP status code onto a sentinel error.
// detail must already be sanitized by the caller.
func classifyStatus(status int, detail string) error {
	if title := htmlTitle(detail); title != "" {
		// The page is markup wrapped around one sentence. Keep the sentence.
		detail = title
		if status >= 500 && isCompareAndSwapRefusal(title) {
			// Deterministic: the record changed, and repeating the request
			// cannot change that. ErrConflict is excluded from isRetryable.
			return &statusError{sentinel: ErrConflict, status: status, detail: detail}
		}
	}
	switch {
	case status == 401:
		return &statusError{sentinel: ErrUnauthorized, status: status, detail: detail}
	case status == 404:
		return &statusError{sentinel: ErrNotFound, status: status, detail: detail}
	case status >= 400 && status < 500:
		return &statusError{sentinel: ErrClientRequest, status: status, detail: detail}
	case status >= 500:
		return &statusError{sentinel: ErrServer, status: status, detail: detail}
	default:
		return nil
	}
}

// isRetryable reports whether an error warrants another attempt.
//
// Only server-side and transport failures are retried. A 4xx is never retried:
// the request itself is wrong, so repeating it cannot help and only amplifies
// load against an API whose rate limits are undocumented.
func isRetryable(err error) bool {
	return errors.Is(err, ErrServer) || errors.Is(err, ErrTransport)
}
