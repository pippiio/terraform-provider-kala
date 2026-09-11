package client

import (
	"net/url"
	"strings"
)

// redactedPlaceholder is what a sensitive value is replaced with.
const redactedPlaceholder = "REDACTED"

// sensitiveParams are query parameters whose values must never be logged or
// surfaced in an error.
//
// The Kala API is inconsistent about its own auth parameter: the Index endpoint
// spells it "apikey" while every other endpoint uses "api_key". Both are listed
// because redaction must not depend on which endpoint produced the URL.
var sensitiveParams = []string{
	"api_key", "apikey", "password", "token", "secureLoginToken",

	// kauthtoken cannot be caught by the "token" entry: indexParamAssignment
	// requires a delimiter before the name, so "kauthtoken=" never matches
	// "token=". It is listed in its own right.
	"kauthtoken",

	// The username is the account identifier and personal data. It is
	// not a credential on its own, but it has no business in an error message
	// or a log line either.
	"username",
}

func isSensitiveParam(name string) bool {
	for _, s := range sensitiveParams {
		if strings.EqualFold(name, s) {
			return true
		}
	}
	return false
}

// sanitizeURL returns raw with the value of every sensitive query parameter
// replaced by a placeholder.
//
// This exists because the Kala API accepts credentials as query parameters
// rather than headers, which makes the otherwise-natural act of wrapping an
// error with its request URL a credential leak.
// Every error and log path in this package routes through here.
//
// It fails closed: if raw cannot be parsed we cannot prove the credential is
// absent, so a placeholder is returned instead of the original string.
func sanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Host == "" && u.Path == "" && u.RawQuery == "") {
		return "[unparseable-url-redacted]"
	}
	if u.RawQuery == "" {
		return raw
	}

	q := u.Query()
	for name := range q {
		if isSensitiveParam(name) {
			q.Set(name, redactedPlaceholder)
		}
	}
	u.RawQuery = q.Encode()

	return u.String()
}

// sanitizedError renders a redacted message while preserving the original as an
// unwrap target.
//
// A plain fmt.Errorf("...: %w", err) will NOT do here: the %w verb renders the
// wrapped error's own Error() text, which is exactly the string carrying the
// credential. This type keeps errors.Is/errors.As working via Unwrap without
// ever printing the unredacted message.
type sanitizedError struct {
	msg   string
	cause error
}

func (e *sanitizedError) Error() string { return e.msg }
func (e *sanitizedError) Unwrap() error { return e.cause }

// sanitizeError returns err with any embedded credential redacted.
//
// Go's net/http embeds the full request URL in transport errors, so an
// unsanitized error from the HTTP layer carries the API key.
func sanitizeError(err error) error {
	if err == nil {
		return nil
	}

	msg := err.Error()
	clean := redactSensitiveValues(msg)
	if clean == msg {
		return err
	}

	return &sanitizedError{msg: clean, cause: err}
}

// redactSensitiveValues replaces `name=value` occurrences of sensitive
// parameters anywhere in s, including inside a URL embedded in free text.
// redactSensitiveValues scrubs credential values from arbitrary text, covering
// both wire shapes this API produces:
//
//   - name=value      query strings and form-encoded bodies
//   - "name":"value"  JSON, which the internal app API uses throughout
//
// Both are needed. The JSON pass exists because a real leak was found without
// it: the sign-in request body carries the password, and an upstream error
// response that echoes the request back put it verbatim into an error message.
// Kala demonstrably echoes request data in errors -- its webapiv2 500 page
// includes the full query string, api_key included.
func redactSensitiveValues(s string) string {
	s = redactJSONValues(s)
	for _, name := range sensitiveParams {
		for {
			idx := indexParamAssignment(s, name)
			if idx < 0 {
				break
			}
			valStart := idx + len(name) + 1
			valEnd := valStart
			for valEnd < len(s) && !isValueTerminator(s[valEnd]) {
				valEnd++
			}
			if valEnd == valStart {
				break
			}
			s = s[:valStart] + redactedPlaceholder + s[valEnd:]
		}
	}
	return s
}

// indexParamAssignment finds `name=` in s where the value is not already
// redacted, returning the index of name or -1.
func indexParamAssignment(s, name string) int {
	needle := name + "="
	from := 0
	for {
		i := strings.Index(s[from:], needle)
		if i < 0 {
			return -1
		}
		abs := from + i
		// Require a delimiter before the name so "xapi_key=" does not match.
		if abs > 0 && !isNameBoundary(s[abs-1]) {
			from = abs + len(needle)
			continue
		}
		if strings.HasPrefix(s[abs+len(needle):], redactedPlaceholder) {
			from = abs + len(needle)
			continue
		}
		return abs
	}
}

func isNameBoundary(c byte) bool {
	return c == '?' || c == '&' || c == ' ' || c == '"' || c == '\'' || c == '='
}

func isValueTerminator(c byte) bool {
	return c == '&' || c == '"' || c == ' ' || c == '\'' || c == '\n' || c == '\\'
}

// redactJSONValues replaces the value of any sensitive JSON key, handling both
// quoted strings and bare literals, with or without whitespace after the colon.
//
// It is deliberately a string operation rather than a parse: the input is an
// error-body sample that is frequently truncated and often not valid JSON at
// all, and failing to parse must not mean failing to redact.
func redactJSONValues(s string) string {
	for _, name := range sensitiveParams {
		needle := `"` + name + `"`
		from := 0
		for {
			i := strings.Index(s[from:], needle)
			if i < 0 {
				break
			}
			abs := from + i
			j := abs + len(needle)
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if j >= len(s) || s[j] != ':' {
				from = abs + len(needle)
				continue
			}
			j++
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if j >= len(s) {
				break
			}

			if s[j] == '"' {
				valStart := j + 1
				valEnd := valStart
				for valEnd < len(s) && s[valEnd] != '"' {
					if s[valEnd] == '\\' {
						valEnd++
					}
					valEnd++
				}
				if valEnd > len(s) {
					valEnd = len(s)
				}
				if s[valStart:min(valEnd, len(s))] == redactedPlaceholder {
					from = valEnd
					continue
				}
				s = s[:valStart] + redactedPlaceholder + s[min(valEnd, len(s)):]
				from = valStart + len(redactedPlaceholder)
				continue
			}

			valStart := j
			valEnd := valStart
			for valEnd < len(s) && s[valEnd] != ',' && s[valEnd] != '}' && s[valEnd] != ']' {
				valEnd++
			}
			if valEnd == valStart {
				from = j
				continue
			}
			s = s[:valStart] + redactedPlaceholder + s[valEnd:]
			from = valStart + len(redactedPlaceholder)
		}
	}
	return s
}
