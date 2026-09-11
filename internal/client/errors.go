package client

import "errors"

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

// classifyStatus maps an HTTP status code onto a sentinel error.
// detail must already be sanitized by the caller.
func classifyStatus(status int, detail string) error {
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
