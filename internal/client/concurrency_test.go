package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Terraform runs up to 10 resource operations at once by default, and every one
// of them goes through a single provider-configured client. These tests pin
// what that concurrency must not do.
//
// What they can establish: the handshake happens once, the company header is
// never lost, and nothing races. All of that is deterministic and hermetic.
//
// What they CANNOT establish: whether Kala rate-limits. Finding that out means
// issuing many real writes, and every write in this provider creates something
// that cannot be deleted. That gap is documented rather than guessed at.

type concurrentMock struct {
	srv       *httptest.Server
	signIns   int64
	companies sync.Map
	requests  int64
}

func newConcurrentMock(t *testing.T) *concurrentMock {
	t.Helper()
	m := &concurrentMock{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			atomic.AddInt64(&m.signIns, 1)
			// A slow handshake widens the window in which a second caller
			// could start its own -- which is what the mutex must prevent.
			time.Sleep(5 * time.Millisecond)
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":4242}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
		default:
			atomic.AddInt64(&m.requests, 1)
			m.companies.Store(r.Header.Get("kacompany"), true)
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "totalCount": 0})
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *concurrentMock) client() InternalClient {
	return NewInternal(InternalConfig{
		Endpoint: m.srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond,
	})
}

// Ten parallel operations must produce ONE login, not ten.
//
// Each sign-in is a real authentication against a tenant, and a provider that
// issued one per resource would look like a credential-stuffing client to
// anything watching -- besides being ten times slower on every plan.
func TestConcurrency_TenParallelOperationsShareOneHandshake(t *testing.T) {
	m := newConcurrentMock(t)
	c := m.client()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.ListTasks(context.Background(), TaskQuery{CaseID: 1}); err != nil {
				t.Errorf("ListTasks: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&m.signIns); got != 1 {
		t.Errorf("sign-ins = %d, want exactly 1 shared by all ten callers", got)
	}
	if got := atomic.LoadInt64(&m.requests); got != 10 {
		t.Errorf("requests = %d, want 10 -- every caller must still do its own work", got)
	}
}

// The company header must reach every request, not just the one that happened
// to perform the handshake. Losing it would send a request to whichever
// company Kala defaults to -- a silent write to the wrong tenant.
func TestConcurrency_EveryRequestCarriesTheCompanyHeader(t *testing.T) {
	m := newConcurrentMock(t)
	c := m.client()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.ListTasks(context.Background(), TaskQuery{CaseID: 1})
		}()
	}
	wg.Wait()

	var seen []string
	m.companies.Range(func(k, _ any) bool {
		seen = append(seen, k.(string))
		return true
	})
	if len(seen) != 1 || seen[0] != "4242" {
		t.Errorf("kacompany values seen = %v, want exactly [4242] on every request", seen)
	}
}

// Mixed reads and writes in parallel, run under -race in CI. A shared client
// mutating session state while other goroutines read it is the defect this
// exists to catch.
func TestConcurrency_MixedReadsAndWritesDoNotRace(t *testing.T) {
	m := newConcurrentMock(t)
	c := m.client()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = c.ListTasks(context.Background(), TaskQuery{CaseID: 1})
		}()
		go func() {
			defer wg.Done()
			_ = c.SetJobLinkChecklist(context.Background(), 4, []int64{1})
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&m.signIns); got != 1 {
		t.Errorf("sign-ins = %d, want 1 across mixed reads and writes", got)
	}
}
