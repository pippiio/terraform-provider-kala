package client

import (
	"errors"
	"strings"
	"testing"
)

// Kala's ASP.NET error pages carry the only useful explanation in <title>.
// Observed three times: MVC parameter-binding errors (which revealed endpoint
// signatures), and the case setters' compare-and-swap refusal.
//
// Without extraction the operator sees "<!DOCTYPE html>" and learns nothing.
func TestClassifyStatus_ExtractsTitleFromHTMLErrorPages(t *testing.T) {
	page := `<!DOCTYPE html>
<html>
    <head>
        <title>Previous case name doesn't match</title>
        <style> body { font-family: sans-serif; } </style>
    </head>
    <body><h1>Server Error</h1></body>
</html>`

	err := classifyStatus(500, page)
	if err == nil {
		t.Fatal("a 500 must produce an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Previous case name doesn't match") {
		t.Errorf("the page title is the only useful text and was not surfaced: %s", msg)
	}
	if strings.Contains(msg, "<!DOCTYPE") || strings.Contains(msg, "<style>") {
		t.Errorf("raw markup reached the message: %s", msg)
	}
}

// A compare-and-swap refusal is DETERMINISTIC. Kala reports it as a 5xx, which
// the retry policy would otherwise treat as transient -- costing four requests
// and seconds of backoff to reach an answer that cannot change.
func TestClassifyStatus_CompareAndSwapRefusalIsNotRetryable(t *testing.T) {
	for _, title := range []string{
		"Previous case name doesn't match",
		"Previous address doesn't match",
	} {
		page := "<html><head><title>" + title + "</title></head></html>"
		err := classifyStatus(500, page)

		if isRetryable(err) {
			t.Errorf("%q is deterministic and must not be retried", title)
		}
		if !errors.Is(err, ErrConflict) {
			t.Errorf("%q should be ErrConflict, got %v", title, err)
		}
	}
}

// Ordinary 5xx pages stay retryable -- the narrowing must not disable retries
// for genuine server failures.
func TestClassifyStatus_OrdinaryServerErrorsStayRetryable(t *testing.T) {
	for _, body := range []string{
		"<html><head><title>Runtime Error</title></head></html>",
		"upstream exploded",
		"",
	} {
		err := classifyStatus(500, body)
		if !isRetryable(err) {
			t.Errorf("a genuine 5xx must stay retryable: %q", body)
		}
		if errors.Is(err, ErrConflict) {
			t.Errorf("%q is not a conflict", body)
		}
	}
}

func TestClassifyStatus_NonHTMLBodiesAreUnchanged(t *testing.T) {
	err := classifyStatus(500, `{"status":"Error","message":"nope"}`)
	if !strings.Contains(err.Error(), `{"status":"Error"`) {
		t.Errorf("a JSON body must pass through untouched: %s", err.Error())
	}
}

// The body sample is bounded at 512 bytes, so a long ASP.NET page can be cut
// mid-title. An unterminated <title> must fall back to the raw sample rather
// than returning a truncated fragment or swallowing the error.
func TestHTMLTitle_TruncatedPageFallsBack(t *testing.T) {
	cut := `<!DOCTYPE html><html><head><title>The parameters dictionary contains a null entry for para`
	if got := htmlTitle(cut); got != "" {
		t.Errorf("an unterminated title must not be used, got %q", got)
	}

	err := classifyStatus(500, cut)
	if !isRetryable(err) {
		t.Error("a truncated page is not evidence of a conflict; it must stay retryable")
	}
	if !strings.Contains(err.Error(), "<!DOCTYPE") {
		t.Errorf("with no usable title the raw sample is all there is: %s", err.Error())
	}
}

func TestHTMLTitle_NoTitleTagIsEmpty(t *testing.T) {
	if got := htmlTitle("<html><body>nothing here</body></html>"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
