package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// internalMock stands in for the app API: the SignIn -> SelectCompany handshake
// plus Workers, SetValidated, and SignUp.
type internalMock struct {
	srv *httptest.Server

	signIns    int32
	selects    int32
	workers    map[int64]*wireWorker
	lastSetVal *wireSetValidatedRequest
	lastSignUp *wireSignUpRequest

	signInStatus   int
	setValStatus   int
	signUpStatus   int
	setValNoEffect bool // simulate a 200 that does not actually change state
}

func newInternalMock(t *testing.T) *internalMock {
	t.Helper()
	m := &internalMock{workers: map[int64]*wireWorker{}}

	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(body)
		}

		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			atomic.AddInt32(&m.signIns, 1)
			if m.signInStatus != 0 {
				w.WriteHeader(m.signInStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				GlobalUserID:     38357,
				SecureLoginToken: "secure-login-token",
				Companies:        []wireCompany{{ID: 17221, Name: "TechChapter"}},
			})

		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			atomic.AddInt32(&m.selects, 1)
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{
				GlobalCompanyName: "TechChapter",
				Token:             "session-token",
			})

		case strings.HasSuffix(r.URL.Path, "/api/Workers/"):
			if got := r.Header.Get("kauthtoken"); got != "session-token" {
				t.Errorf("Workers called with kauthtoken %q", got)
			}
			out := make([]wireWorker, 0, len(m.workers))
			for _, wk := range m.workers {
				out = append(out, *wk)
			}
			_ = json.NewEncoder(w).Encode(out)

		case strings.HasSuffix(r.URL.Path, "/api/SetValidated/"):
			if m.setValStatus != 0 {
				w.WriteHeader(m.setValStatus)
				return
			}
			if r.Header.Get("kacompany") == "" {
				t.Error("SetValidated requires the kacompany header")
			}
			var req wireSetValidatedRequest
			_ = json.Unmarshal(body, &req)
			m.lastSetVal = &req
			if !m.setValNoEffect {
				if wk, ok := m.workers[req.WorkerNr]; ok {
					wk.IsValidated = req.IsValidated
				}
			}
			w.WriteHeader(http.StatusOK)

		case strings.HasSuffix(r.URL.Path, "/Api/SignUp/"):
			if m.signUpStatus != 0 {
				w.WriteHeader(m.signUpStatus)
				return
			}
			var req wireSignUpRequest
			_ = json.Unmarshal(body, &req)
			m.lastSignUp = &req
			nr := req.MedarbejderNr
			m.workers[nr] = &wireWorker{WorkerNr: &nr, Name: req.Name, IsValidated: true}
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	t.Cleanup(m.srv.Close)
	return m
}

func (m *internalMock) addWorker(nr int64, name string, validated bool) {
	n := nr
	m.workers[nr] = &wireWorker{WorkerNr: &n, Name: name, IsValidated: validated}
}

func (m *internalMock) client() InternalClient {
	return NewInternal(InternalConfig{
		Endpoint:     m.srv.URL,
		Username:     "user@example.com",
		Password:     "hunter2",
		MaxRetries:   1,
		Timeout:      5 * time.Second,
		retryBaseDur: time.Microsecond,
	})
}

// --- authentication -------------------------------------------------------

func TestInternal_SignInSelectCompanyHandshake(t *testing.T) {
	m := newInternalMock(t)
	m.addWorker(1, "Alice", true)

	if _, err := m.client().ListWorkers(context.Background()); err != nil {
		t.Fatalf("ListWorkers: %v", err)
	}

	if got := atomic.LoadInt32(&m.signIns); got != 1 {
		t.Errorf("SignIn calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&m.selects); got != 1 {
		t.Errorf("SelectCompany calls = %d, want 1", got)
	}
}

// The handshake is expensive; a client must not repeat it per request.
func TestInternal_SessionIsReusedAcrossCalls(t *testing.T) {
	m := newInternalMock(t)
	m.addWorker(1, "Alice", true)
	c := m.client()

	for i := 0; i < 3; i++ {
		if _, err := c.ListWorkers(context.Background()); err != nil {
			t.Fatalf("ListWorkers: %v", err)
		}
	}

	if got := atomic.LoadInt32(&m.signIns); got != 1 {
		t.Errorf("SignIn calls = %d, want 1 — the session must be cached", got)
	}
}

func TestInternal_MissingCredentialsIsAClearError(t *testing.T) {
	c := NewInternal(InternalConfig{Endpoint: "https://example.test"})

	_, err := c.ListWorkers(context.Background())
	if err == nil {
		t.Fatal("want an error when username/password are absent")
	}
	for _, want := range []string{"KALA_USERNAME", "KALA_PASSWORD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s, got %q", want, err.Error())
		}
	}
}

// SEC1.3: the password must never reach an error message.
func TestInternal_PasswordNeverAppearsInErrors(t *testing.T) {
	const password = "super-secret-password"

	m := newInternalMock(t)
	m.signInStatus = http.StatusUnauthorized

	c := NewInternal(InternalConfig{
		Endpoint: m.srv.URL, Username: "u", Password: password,
		retryBaseDur: time.Microsecond,
	})

	_, err := c.ListWorkers(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("password leaked into the error: %q", err.Error())
	}
}

// --- activation (ADR-002) -------------------------------------------------

func TestInternal_SetWorkerValidatedDeactivates(t *testing.T) {
	m := newInternalMock(t)
	m.addWorker(42, "Bob", true)

	if err := m.client().SetWorkerValidated(context.Background(), 42, false); err != nil {
		t.Fatalf("SetWorkerValidated: %v", err)
	}

	if m.lastSetVal == nil {
		t.Fatal("SetValidated was never called")
	}
	if m.lastSetVal.WorkerNr != 42 || m.lastSetVal.IsValidated {
		t.Errorf("sent %+v, want {WorkerNr:42 IsValidated:false}", *m.lastSetVal)
	}
	if m.workers[42].IsValidated {
		t.Error("worker should be deactivated")
	}
}

// ARCH1.8: HTTP 200 is not proof. A write that does not take effect must fail.
func TestInternal_SetWorkerValidatedFailsWhenUnverified(t *testing.T) {
	m := newInternalMock(t)
	m.addWorker(42, "Bob", true)
	m.setValNoEffect = true // 200 OK, but nothing changes

	err := m.client().SetWorkerValidated(context.Background(), 42, false)
	if err == nil {
		t.Fatal("an unverified deactivation must fail — a silent no-op would leave a departed employee active")
	}
	if !strings.Contains(err.Error(), "reads back as") {
		t.Errorf("error should report the read-back mismatch, got %q", err.Error())
	}
}

func TestInternal_SetWorkerValidatedPropagatesHTTPError(t *testing.T) {
	m := newInternalMock(t)
	m.addWorker(42, "Bob", true)
	m.setValStatus = http.StatusForbidden

	if err := m.client().SetWorkerValidated(context.Background(), 42, false); err == nil {
		t.Fatal("want the HTTP failure to surface")
	}
}

func TestInternal_GetWorkerNotFound(t *testing.T) {
	m := newInternalMock(t)
	m.addWorker(1, "Alice", true)

	_, err := m.client().GetWorker(context.Background(), 999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// --- creation (/Api/SignUp/) ----------------------------------------------

func TestInternal_CreateWorkerSendsMedarbejderNr(t *testing.T) {
	m := newInternalMock(t)

	got, err := m.client().CreateWorker(context.Background(), NewWorker{
		Number: 2, Email: "test@example.com", Name: "Test Mogens",
	})
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}

	if m.lastSignUp == nil {
		t.Fatal("SignUp was never called")
	}
	if m.lastSignUp.MedarbejderNr != 2 {
		t.Errorf("medarbejderNr = %d, want 2", m.lastSignUp.MedarbejderNr)
	}
	if m.lastSignUp.Email != "test@example.com" || m.lastSignUp.Name != "Test Mogens" {
		t.Errorf("sent %+v", *m.lastSignUp)
	}
	if got.WorkerNr != 2 {
		t.Errorf("returned workerNr = %d, want 2", got.WorkerNr)
	}
}

func TestInternal_CreateWorkerRequiresAllFields(t *testing.T) {
	m := newInternalMock(t)
	c := m.client()

	cases := []struct {
		name string
		in   NewWorker
	}{
		{"no number", NewWorker{Email: "a@b.c", Name: "N"}},
		{"no name", NewWorker{Number: 1, Email: "a@b.c"}},
		{"no email", NewWorker{Number: 1, Name: "N"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.CreateWorker(context.Background(), tc.in); err == nil {
				t.Error("want a validation error before any request is made")
			}
		})
	}
}

// The read-back is also the experiment that would reveal a medarbejderNr /
// workerNr mismatch, so its failure message must say so.
func TestInternal_CreateWorkerUnverifiedMentionsIdentifierMismatch(t *testing.T) {
	m := newInternalMock(t)
	m.signUpStatus = http.StatusOK // accepted, but the mock records nothing

	// Override: accept SignUp but never add the worker.
	m.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 1}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "session-token"})
		case strings.HasSuffix(r.URL.Path, "/api/Workers/"):
			_, _ = w.Write([]byte(`[]`)) // the new employee never appears
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	_, err := m.client().CreateWorker(context.Background(), NewWorker{
		Number: 2, Email: "a@b.c", Name: "N",
	})
	if err == nil {
		t.Fatal("want an error when the created employee cannot be found")
	}
	if !strings.Contains(err.Error(), "medarbejderNr") {
		t.Errorf("error should flag the possible identifier mismatch, got %q", err.Error())
	}
}

// --- WorkerInfo -----------------------------------------------------------

func workerInfoMock(t *testing.T, handler http.HandlerFunc) InternalClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":17221}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
		default:
			handler(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return NewInternal(InternalConfig{
		Endpoint: srv.URL, Username: "u", Password: "p",
		retryBaseDur: time.Microsecond,
	})
}

func TestInternal_GetWorkerInfoDecodesFullRecord(t *testing.T) {
	c := workerInfoMock(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("workerNr"); got != "3" {
			t.Errorf("workerNr = %q, want 3", got)
		}
		if r.Header.Get("kacompany") == "" {
			t.Error("WorkerInfo requires the kacompany header")
		}
		// Shapes as observed on the live API: normHours and the dates are
		// STRINGS, and several fields come back null.
		_, _ = w.Write([]byte(`{
			"workerNr":3,"workerId":3,"name":"N","email":"a@b.c","initials":"AB",
			"title":"T","department":"D","phone":"p","privatePhone":null,
			"licensePlate":"XY12345","dateOfEmployment":"2020-01-01",
			"flexStartDate":"2021-01-01","normHours":"37","leaderNote":"note",
			"isValidated":true,"isLeader":true,"isPlanner":false,"isSuperUser":true,
			"isFinance":false,"isVisibleInPlanner":true,"allowWeekView":true,
			"workerImage":null,"extraRolesDict":null,"isTool":0
		}`))
	})

	got, err := c.GetWorkerInfo(context.Background(), 3)
	if err != nil {
		t.Fatalf("GetWorkerInfo: %v", err)
	}

	if got.Email != "a@b.c" {
		t.Errorf("Email = %q — this is the field the write-only claim got wrong", got.Email)
	}
	if got.NormHours != "37" || got.DateOfEmployment != "2020-01-01" {
		t.Errorf("string-typed fields wrong: %+v", got)
	}
	if got.PrivatePhone != "" {
		t.Errorf("null privatePhone should decode to empty, got %q", got.PrivatePhone)
	}
	if !got.IsLeader || !got.IsSuperUser || got.IsPlanner {
		t.Errorf("booleans wrong: %+v", got)
	}
	// isTool is an int upstream, not a bool — it must not break decoding.
	if got.WorkerID != 3 {
		t.Errorf("WorkerID = %d, want 3", got.WorkerID)
	}
}

// A missing worker produces HTTP 500 with an HTML body, not 404. That is
// classified as retryable, which is precisely why GetWorkerInfo must not be
// used as an existence check.
func TestInternal_GetWorkerInfoMissingWorkerIs500(t *testing.T) {
	c := workerInfoMock(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>Sequence contains no elements</title>`))
	})

	_, err := c.GetWorkerInfo(context.Background(), 99999)
	if !errors.Is(err, ErrServer) {
		t.Errorf("want ErrServer (not ErrNotFound) — establish existence with GetWorker first: %v", err)
	}
}

func TestInternal_GetWorkerInfoEmptyBodyIsNotFound(t *testing.T) {
	c := workerInfoMock(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if _, err := c.GetWorkerInfo(context.Background(), 3); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestInternal_GetWorkerInfoMalformedBody(t *testing.T) {
	c := workerInfoMock(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{broken`))
	})

	if _, err := c.GetWorkerInfo(context.Background(), 3); !errors.Is(err, ErrDecode) {
		t.Errorf("want ErrDecode, got %v", err)
	}
}

func TestInternal_GetWorkerInfoRequiresIdentity(t *testing.T) {
	c := workerInfoMock(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"no identity"}`))
	})

	if _, err := c.GetWorkerInfo(context.Background(), 3); !errors.Is(err, ErrDecode) {
		t.Errorf("a record without workerNr must fail: %v", err)
	}
}

func TestInternal_GetWorkerInfoNeedsCredentials(t *testing.T) {
	c := NewInternal(InternalConfig{Endpoint: "https://example.test"})
	if _, err := c.GetWorkerInfo(context.Background(), 1); err == nil {
		t.Error("want a credentials error")
	}
}

// --- SetEmailNew ----------------------------------------------------------

// emailMock serves the handshake, SetEmailNew, and a WorkerInfo whose email
// reflects (or deliberately fails to reflect) the write.
func emailMock(t *testing.T, applyWrite bool, setStatus int) (InternalClient, *[]wireSetEmailRequest) {
	t.Helper()
	var calls []wireSetEmailRequest
	current := "old@example.com"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":17221}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
		case strings.HasSuffix(r.URL.Path, "/api/SetEmailNew/"):
			if setStatus != 0 {
				w.WriteHeader(setStatus)
				return
			}
			if r.Header.Get("kacompany") == "" {
				t.Error("SetEmailNew requires the kacompany header")
			}
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			var req wireSetEmailRequest
			_ = json.Unmarshal(body, &req)
			calls = append(calls, req)
			if applyWrite {
				current = req.Email
			}
			w.WriteHeader(http.StatusOK)
		default: // WorkerInfo
			_, _ = w.Write([]byte(`{"workerNr":3,"workerId":3,"email":"` + current + `"}`))
		}
	}))
	t.Cleanup(srv.Close)

	return NewInternal(InternalConfig{
		Endpoint: srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond,
	}), &calls
}

func TestInternal_SetWorkerEmailSendsWorkerNrAndEmail(t *testing.T) {
	c, calls := emailMock(t, true, 0)

	if err := c.SetWorkerEmail(context.Background(), 3, "test2@archan.dk"); err != nil {
		t.Fatalf("SetWorkerEmail: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want one call, got %d", len(*calls))
	}
	if (*calls)[0].WorkerNr != 3 || (*calls)[0].Email != "test2@archan.dk" {
		t.Errorf("sent %+v", (*calls)[0])
	}
}

// ARCH1.8: 200 OK is the endpoint's claim, not proof. A write that does not
// take effect must fail.
func TestInternal_SetWorkerEmailFailsWhenUnverified(t *testing.T) {
	c, _ := emailMock(t, false, 0) // accepts the write, never applies it

	err := c.SetWorkerEmail(context.Background(), 3, "new@example.com")
	if err == nil {
		t.Fatal("an unverified email change must fail")
	}
	if !strings.Contains(err.Error(), "reads back as") {
		t.Errorf("error should report the read-back mismatch, got %q", err.Error())
	}
}

// Kala may normalise case; a case-only difference is not a failure.
func TestInternal_SetWorkerEmailAcceptsCaseDifference(t *testing.T) {
	var current string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
		case strings.HasSuffix(r.URL.Path, "/api/SetEmailNew/"):
			current = "TEST@ARCHAN.DK" // upstream upper-cases it
			w.WriteHeader(http.StatusOK)
		default:
			_, _ = w.Write([]byte(`{"workerNr":3,"email":"` + current + `"}`))
		}
	}))
	defer srv.Close()

	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond})
	if err := c.SetWorkerEmail(context.Background(), 3, "test@archan.dk"); err != nil {
		t.Errorf("a case-only difference should be accepted: %v", err)
	}
}

func TestInternal_SetWorkerEmailRejectsEmpty(t *testing.T) {
	c, calls := emailMock(t, true, 0)

	if err := c.SetWorkerEmail(context.Background(), 3, ""); err == nil {
		t.Fatal("want a validation error before any request")
	}
	if len(*calls) != 0 {
		t.Error("no request should be made for an empty email")
	}
}

func TestInternal_SetWorkerEmailPropagatesHTTPFailure(t *testing.T) {
	c, _ := emailMock(t, false, http.StatusForbidden)

	if err := c.SetWorkerEmail(context.Background(), 3, "x@y.z"); err == nil {
		t.Fatal("want the HTTP failure to surface")
	}
}

func TestInternal_SetWorkerEmailNeedsCredentials(t *testing.T) {
	c := NewInternal(InternalConfig{Endpoint: "https://example.test"})
	if err := c.SetWorkerEmail(context.Background(), 1, "a@b.c"); err == nil {
		t.Error("want a credentials error")
	}
}
