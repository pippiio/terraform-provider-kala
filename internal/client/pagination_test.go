package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// Pagination must terminate under every condition the API can produce.
// An unbounded loop against a paginated endpoint is guardrail GO1.6's target.

// pageServer serves `total` synthetic employees, honouring page/page_size.
func pageServer(t *testing.T, total int, calls *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
		if size <= 0 {
			size = 500
		}

		start := page * size
		end := start + size
		if start > total {
			start = total
		}
		if end > total {
			end = total
		}

		out := make([]map[string]any, 0, end-start)
		for i := start; i < end; i++ {
			out = append(out, map[string]any{"number": i + 1, "name": fmt.Sprintf("Employee %d", i+1)})
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(out)
	}))
}

func TestListEmployees_SinglePartialPageTerminates(t *testing.T) {
	var calls int32
	srv := pageServer(t, 7, &calls)
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	got, err := c.ListEmployees(context.Background(), ListOptions{PageSize: 10})
	if err != nil {
		t.Fatalf("ListEmployees: %v", err)
	}
	if len(got) != 7 {
		t.Errorf("len = %d, want 7", len(got))
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("a short page must end the loop: %d requests, want 1", n)
	}
}

func TestListEmployees_FollowsMultiplePages(t *testing.T) {
	var calls int32
	srv := pageServer(t, 25, &calls)
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	got, err := c.ListEmployees(context.Background(), ListOptions{PageSize: 10})
	if err != nil {
		t.Fatalf("ListEmployees: %v", err)
	}
	if len(got) != 25 {
		t.Errorf("len = %d, want 25", len(got))
	}
	// pages of 10,10,5 — the third is short and ends the loop.
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Errorf("requests = %d, want 3", n)
	}
	if got[0].Number != 1 || got[24].Number != 25 {
		t.Errorf("ordering/aggregation wrong: first=%d last=%d", got[0].Number, got[24].Number)
	}
}

// An exact multiple means the last full page is followed by an empty one.
func TestListEmployees_ExactMultipleTerminatesOnEmptyPage(t *testing.T) {
	var calls int32
	srv := pageServer(t, 20, &calls)
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	got, err := c.ListEmployees(context.Background(), ListOptions{PageSize: 10})
	if err != nil {
		t.Fatalf("ListEmployees: %v", err)
	}
	if len(got) != 20 {
		t.Errorf("len = %d, want 20", len(got))
	}
	// 10, 10, then an empty page that stops it.
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Errorf("requests = %d, want 3 (two full pages + one empty)", n)
	}
}

func TestListEmployees_EmptyResultSet(t *testing.T) {
	srv := pageServer(t, 0, nil)
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	got, err := c.ListEmployees(context.Background(), ListOptions{PageSize: 10})
	if err != nil {
		t.Fatalf("ListEmployees: %v", err)
	}
	if got == nil {
		t.Error("want an empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// A server that always returns a full page would loop forever without the cap.
func TestListEmployees_PageCapPreventsInfiniteLoop(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
		out := make([]map[string]any, 0, size)
		for i := 0; i < size; i++ {
			out = append(out, map[string]any{"number": i + 1, "name": "X"})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	_, err := c.ListEmployees(context.Background(), ListOptions{PageSize: 5, MaxPages: 4})

	if err == nil {
		t.Fatal("want an error when the page cap is hit, got nil")
	}
	if !strings.Contains(err.Error(), "page cap") {
		t.Errorf("error should explain the cap, got %q", err.Error())
	}
	if n := atomic.LoadInt32(&calls); n != 4 {
		t.Errorf("requests = %d, want exactly MaxPages (4)", n)
	}
}

func TestListEmployees_DefaultsAreApplied(t *testing.T) {
	var gotOrder, gotPageSize string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrder = r.URL.Query().Get("order")
		gotPageSize = r.URL.Query().Get("page_size")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	if _, err := c.ListEmployees(context.Background(), ListOptions{}); err != nil {
		t.Fatalf("ListEmployees: %v", err)
	}

	if gotOrder != "asc" {
		t.Errorf("order = %q, want asc (the API default)", gotOrder)
	}
	// Deliberately below the API's documented default of 5000 (risk R4).
	if gotPageSize != strconv.Itoa(defaultPageSize) {
		t.Errorf("page_size = %q, want %d", gotPageSize, defaultPageSize)
	}
	if defaultPageSize >= 5000 {
		t.Errorf("defaultPageSize = %d; must stay well below the API default of 5000", defaultPageSize)
	}
}

func TestListEmployees_PropagatesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.MaxRetries = 0
	c := newWebAPIv2(cfg)

	if _, err := c.ListEmployees(context.Background(), ListOptions{}); err == nil {
		t.Fatal("want the underlying error to propagate")
	}
}

func TestAuthParam_IndexUsesDifferentSpelling(t *testing.T) {
	// The API contradicts itself; the client must not assume one spelling (FR7).
	if got := authParam("Index"); got != "apikey" {
		t.Errorf("authParam(Index) = %q, want apikey", got)
	}
	for _, ep := range []string{"Ping", "ActiveEmployee", "ActiveEmployeesList"} {
		if got := authParam(ep); got != "api_key" {
			t.Errorf("authParam(%s) = %q, want api_key", ep, got)
		}
	}
}

func TestBackoff_GrowsExponentiallyAndIsCapped(t *testing.T) {
	base := backoffFor(0, defaultRetryBase)
	next := backoffFor(1, defaultRetryBase)
	if next <= base {
		t.Errorf("backoff must grow: attempt0=%v attempt1=%v", base, next)
	}
	if got := backoffFor(50, defaultRetryBase); got != maxRetryBackoff {
		t.Errorf("backoff must be capped at %v, got %v", maxRetryBackoff, got)
	}
}

func TestConfig_DefaultsFillZeroValues(t *testing.T) {
	c := Config{}.withDefaults()

	if c.Endpoint != DefaultEndpoint {
		t.Errorf("Endpoint = %q, want %q", c.Endpoint, DefaultEndpoint)
	}
	if c.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", c.Timeout, defaultTimeout)
	}
	if c.HTTPClient == nil {
		t.Error("HTTPClient must be non-nil after defaults")
	}
	if c.retryBaseDur != defaultRetryBase {
		t.Errorf("retryBaseDur = %v, want %v", c.retryBaseDur, defaultRetryBase)
	}
}
