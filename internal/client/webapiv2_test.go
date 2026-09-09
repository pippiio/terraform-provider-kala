package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Every test here runs against an httptest server: unit tests make no real
// network calls. Retry delays are set to microseconds so the suite stays
// fast without disabling the backoff path.

func testConfig(endpoint string) Config {
	return Config{
		Endpoint:     endpoint,
		APIKey:       "test-key",
		MaxRetries:   3,
		Timeout:      5 * time.Second,
		retryBaseDur: time.Microsecond,
	}
}

func TestPing_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/Ping" {
			t.Errorf("path = %q, want /Ping", got)
		}
		if got := r.URL.Query().Get("api_key"); got != "test-key" {
			t.Errorf("api_key = %q, want test-key", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"pong":"pong"}`))
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPing_401IsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	err := c.Ping(context.Background())

	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

// A rejected request cannot succeed on retry, and retrying amplifies load
// against an API with undocumented rate limits.
func TestRetry_4xxIsNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	_ = c.Ping(context.Background())

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("4xx was retried: %d calls, want exactly 1", got)
	}
}

func TestRetry_401IsNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	_ = c.Ping(context.Background())

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("401 was retried: %d calls, want exactly 1", got)
	}
}

func TestRetry_5xxRetriesToMaxAttempts(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.MaxRetries = 3
	c := newWebAPIv2(cfg)

	err := c.Ping(context.Background())
	if !errors.Is(err, ErrServer) {
		t.Errorf("want ErrServer after exhausting retries, got %v", err)
	}
	// 1 initial attempt + 3 retries.
	if got := atomic.LoadInt32(&calls); got != 4 {
		t.Errorf("calls = %d, want 4 (1 initial + 3 retries)", got)
	}
}

func TestRetry_5xxThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"pong":"pong"}`))
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("want success on the 3rd attempt, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

// Cancellation must abort in flight, not run to completion.
func TestContext_CancellationAbortsRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.MaxRetries = 100
	cfg.retryBaseDur = 20 * time.Millisecond
	c := newWebAPIv2(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	err := c.Ping(ctx)
	if err == nil {
		t.Fatal("want an error after cancellation, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want errors.Is(err, context.Canceled), got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got > 10 {
		t.Errorf("cancellation did not stop retries: %d calls", got)
	}
}

func TestContext_AlreadyCancelledMakesNoRequest(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := newWebAPIv2(testConfig(srv.URL))
	if err := c.Ping(ctx); err == nil {
		t.Fatal("want an error for an already-cancelled context")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("made %d requests with a cancelled context, want 0", got)
	}
}

func TestTransportError_IsRetriedAndClassified(t *testing.T) {
	// Closed server: connections are refused, producing a transport error.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	cfg := testConfig(url)
	cfg.MaxRetries = 2
	c := newWebAPIv2(cfg)

	err := c.Ping(context.Background())
	if !errors.Is(err, ErrTransport) {
		t.Errorf("want ErrTransport, got %v", err)
	}
}

// The credential must never survive into an error message.
func TestErrors_NeverContainTheAPIKey(t *testing.T) {
	const key = "extremely-secret-key"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.APIKey = key
	cfg.MaxRetries = 0
	c := newWebAPIv2(cfg)

	err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("API key leaked into error: %q", err.Error())
	}
}

func TestGetEmployee_SuccessAndNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("employeeNumber") == "404" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"number":4711,"name":"Frodo","settings":[{"key":"k","value":"v"}]}`))
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))

	got, err := c.GetEmployee(context.Background(), 4711)
	if err != nil {
		t.Fatalf("GetEmployee: %v", err)
	}
	if got.Number != 4711 || got.Name != "Frodo" {
		t.Errorf("got %+v, want number 4711 name Frodo", got)
	}

	if _, err := c.GetEmployee(context.Background(), 404); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// A non-2xx body must never be decoded — its shape is undocumented.
func TestNonSuccessBodyIsNotDecoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`this is not json at all`))
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.MaxRetries = 0
	c := newWebAPIv2(cfg)

	_, err := c.GetEmployee(context.Background(), 1)
	if errors.Is(err, ErrDecode) {
		t.Errorf("non-2xx body should be classified by status, not decoded: %v", err)
	}
	if !errors.Is(err, ErrClientRequest) {
		t.Errorf("want ErrClientRequest, got %v", err)
	}
}
