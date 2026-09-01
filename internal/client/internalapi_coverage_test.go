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

func TestInternalConfig_Defaults(t *testing.T) {
	c := InternalConfig{}.withDefaults()

	if c.Endpoint != DefaultInternalEndpoint {
		t.Errorf("Endpoint = %q, want %q", c.Endpoint, DefaultInternalEndpoint)
	}
	if c.Timeout != defaultTimeout || c.retryBaseDur != defaultRetryBase || c.HTTPClient == nil {
		t.Errorf("defaults not applied: %+v", c)
	}

	custom := &http.Client{Timeout: time.Second}
	kept := InternalConfig{
		Endpoint: "https://x.test", Timeout: 9 * time.Second,
		MaxRetries: -3, HTTPClient: custom,
	}.withDefaults()
	if kept.Endpoint != "https://x.test" || kept.Timeout != 9*time.Second || kept.HTTPClient != custom {
		t.Errorf("supplied values overwritten: %+v", kept)
	}
	if kept.MaxRetries != 0 {
		t.Errorf("negative MaxRetries should clamp to 0, got %d", kept.MaxRetries)
	}
}

func TestWireWorker_RequiresWorkerNr(t *testing.T) {
	if _, err := (wireWorker{}).toDomain(); !errors.Is(err, ErrDecode) {
		t.Errorf("a worker without workerNr must fail: %v", err)
	}
	zero := int64(0)
	if _, err := (wireWorker{WorkerNr: &zero}).toDomain(); !errors.Is(err, ErrDecode) {
		t.Errorf("workerNr 0 must fail: %v", err)
	}
	n := int64(7)
	got, err := wireWorker{WorkerNr: &n, Name: "A", Department: "D", Initials: "AA", IsValidated: true}.toDomain()
	if err != nil {
		t.Fatalf("toDomain: %v", err)
	}
	if got.WorkerNr != 7 || got.Department != "D" || got.Initials != "AA" || !got.IsValidated {
		t.Errorf("mapping lost data: %+v", got)
	}
}

func TestInternal_SignInWithNoCompaniesIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Auth/SignIn/") {
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond})
	_, err := c.ListWorkers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no companies") {
		t.Errorf("want a no-companies error, got %v", err)
	}
}

func TestInternal_EmptySelectCompanyTokenIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":""}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond})
	_, err := c.ListWorkers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "empty token") {
		t.Errorf("want an empty-token error, got %v", err)
	}
}

func TestInternal_MalformedSignInResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer srv.Close()

	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond})
	if _, err := c.ListWorkers(context.Background()); !errors.Is(err, ErrDecode) {
		t.Errorf("want ErrDecode, got %v", err)
	}
}

func TestInternal_EmptyWorkersBodyIsEmptyList(t *testing.T) {
	m := newInternalMock(t)
	m.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
		default:
			w.WriteHeader(http.StatusOK) // empty body
		}
	})

	got, err := m.client().ListWorkers(context.Background())
	if err != nil {
		t.Fatalf("empty workers body should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestInternal_MalformedWorkersBody(t *testing.T) {
	m := newInternalMock(t)
	m.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
		default:
			_, _ = w.Write([]byte(`[{"name":"no identity"}]`))
		}
	})

	if _, err := m.client().ListWorkers(context.Background()); !errors.Is(err, ErrDecode) {
		t.Errorf("a worker without workerNr must fail the page, got %v", err)
	}
}

func TestInternal_ContextCancellation(t *testing.T) {
	m := newInternalMock(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := m.client().ListWorkers(ctx); err == nil {
		t.Error("want an error for an already-cancelled context")
	}
}

func TestInternal_TransportErrorIsClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := NewInternal(InternalConfig{Endpoint: url, Username: "u", Password: "p", retryBaseDur: time.Microsecond})
	if _, err := c.ListWorkers(context.Background()); !errors.Is(err, ErrTransport) {
		t.Errorf("want ErrTransport, got %v", err)
	}
}

func TestInternal_CreateWorkerHTTPFailure(t *testing.T) {
	m := newInternalMock(t)
	m.signUpStatus = http.StatusBadRequest

	_, err := m.client().CreateWorker(context.Background(), NewWorker{Number: 5, Email: "a@b.c", Name: "N"})
	if err == nil {
		t.Fatal("want the HTTP failure to surface")
	}
}

func TestInternal_RetriesOn5xxThenSucceeds(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Auth/SignIn/") {
			calls++
			if calls < 2 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":1}]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/") {
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewInternal(InternalConfig{
		Endpoint: srv.URL, Username: "u", Password: "p",
		MaxRetries: 3, retryBaseDur: time.Microsecond,
	})
	if _, err := c.ListWorkers(context.Background()); err != nil {
		t.Fatalf("should recover after a 5xx: %v", err)
	}
	if calls != 2 {
		t.Errorf("SignIn attempts = %d, want 2", calls)
	}
}

func TestInternal_4xxIsNotRetried(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewInternal(InternalConfig{
		Endpoint: srv.URL, Username: "u", Password: "p",
		MaxRetries: 5, retryBaseDur: time.Microsecond,
	})
	_, _ = c.ListWorkers(context.Background())

	if calls != 1 {
		t.Errorf("4xx was retried: %d calls, want 1", calls)
	}
}

func TestInternal_MalformedSelectCompanyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Auth/SignIn/") {
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":1}]}`))
			return
		}
		_, _ = w.Write([]byte(`{broken`))
	}))
	defer srv.Close()

	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond})
	if _, err := c.ListWorkers(context.Background()); !errors.Is(err, ErrDecode) {
		t.Errorf("want ErrDecode, got %v", err)
	}
}

func TestInternal_SelectCompanyHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Auth/SignIn/") {
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":1}]}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond})
	_, err := c.ListWorkers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "select-company") {
		t.Errorf("want a select-company failure, got %v", err)
	}
}

func TestInternal_GetWorkerPropagatesListFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond})
	if _, err := c.GetWorker(context.Background(), 1); err == nil {
		t.Error("want the list failure to propagate")
	}
}

func TestInternal_SetWorkerValidatedPropagatesSessionFailure(t *testing.T) {
	c := NewInternal(InternalConfig{Endpoint: "https://example.test"})
	if err := c.SetWorkerValidated(context.Background(), 1, false); err == nil {
		t.Error("want a credentials error")
	}
}

func TestInternal_CreateWorkerPropagatesSessionFailure(t *testing.T) {
	c := NewInternal(InternalConfig{Endpoint: "https://example.test"})
	if _, err := c.CreateWorker(context.Background(), NewWorker{Number: 1, Email: "a@b.c", Name: "N"}); err == nil {
		t.Error("want a credentials error")
	}
}
