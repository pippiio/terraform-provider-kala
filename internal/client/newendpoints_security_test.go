package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// These are REGRESSION assertions, not TDD red/green. The guarantees they
// cover (kacompany on every authed request, credentials never in an error)
// already exist in authedRequest and the sanitize chokepoint. The new
// endpoints depend on both, so they are asserted rather than assumed.

const (
	testUser  = "user@example.com"
	testPass  = "hunter2-correct-horse"
	testToken = "kauth-token-9001"
)

// headerSpy records the auth headers seen on the non-handshake request.
type headerSpy struct {
	srv        *httptest.Server
	company    string
	authtoken  string
	pathSeen   string
	failStatus int
	echoBody   bool // reflect the request body back in the error response
}

func newHeaderSpy(t *testing.T) *headerSpy {
	t.Helper()
	s := &headerSpy{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			if s.echoBody && s.failStatus != 0 {
				body, _ := io.ReadAll(r.Body)
				w.WriteHeader(s.failStatus)
				_, _ = w.Write(body) // a server that echoes the request back
				return
			}
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "secure-login-token",
				Companies:        []wireCompany{{ID: 4242, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: testToken})
		default:
			s.pathSeen = r.URL.Path
			s.company = r.Header.Get("kacompany")
			s.authtoken = r.Header.Get("kauthtoken")
			if s.failStatus != 0 {
				w.WriteHeader(s.failStatus)
				if s.echoBody {
					body, _ := io.ReadAll(r.Body)
					_, _ = w.Write(body)
				}
				return
			}
			switch {
			case strings.Contains(r.URL.Path, "GetChecklistItemsPaged"):
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "totalCount": 0})
			case strings.Contains(r.URL.Path, "GetAllJobsSimplePaged"):
				_ = json.NewEncoder(w).Encode(map[string]any{"cases": []any{}, "totalCount": 0})
			case strings.Contains(r.URL.Path, "GetJobDetailsAdvanced"):
				_ = json.NewEncoder(w).Encode(map[string]any{"caseId": 1, "caseNumber": "KA-1"})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"customers": []any{}, "totalCount": 0})
			}
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *headerSpy) client() InternalClient {
	return NewInternal(InternalConfig{
		Endpoint: s.srv.URL, Username: testUser, Password: testPass,
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond,
	})
}

// FR10: kacompany accompanies kauthtoken on every authed request. Already
// implemented in authedRequest; the new endpoints depend on it.
func TestNewEndpoints_SendCompanyAndAuthHeaders(t *testing.T) {
	calls := map[string]func(InternalClient) error{
		"ListCustomers": func(c InternalClient) error {
			_, err := c.ListCustomers(t.Context(), CustomerQuery{})
			return err
		},
		"ListCases": func(c InternalClient) error {
			_, err := c.ListCases(t.Context(), CaseQuery{})
			return err
		},
		"GetCase": func(c InternalClient) error {
			_, err := c.GetCase(t.Context(), "KA-1")
			return err
		},
		"ListTasks": func(c InternalClient) error {
			_, err := c.ListTasks(t.Context(), TaskQuery{CaseID: 2})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			s := newHeaderSpy(t)
			if err := call(s.client()); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if s.company != "4242" {
				t.Errorf("kacompany = %q on %s, want 4242", s.company, s.pathSeen)
			}
			if s.authtoken != testToken {
				t.Errorf("kauthtoken = %q on %s", s.authtoken, s.pathSeen)
			}
		})
	}
}

// SEC1.3: no credential may reach an error message. The server here is
// hostile in a realistic way -- it echoes the request body back inside an
// error response, and the sign-in body contains the password.
func TestNewEndpoints_CredentialsNeverAppearInErrors(t *testing.T) {
	secrets := map[string]string{
		"password":      testPass,
		"session token": testToken,
	}
	calls := map[string]func(InternalClient) error{
		"ListCustomers": func(c InternalClient) error {
			_, err := c.ListCustomers(t.Context(), CustomerQuery{})
			return err
		},
		"ListCases": func(c InternalClient) error {
			_, err := c.ListCases(t.Context(), CaseQuery{})
			return err
		},
		"GetCase": func(c InternalClient) error {
			_, err := c.GetCase(t.Context(), "KA-1")
			return err
		},
		"ListTasks": func(c InternalClient) error {
			_, err := c.ListTasks(t.Context(), TaskQuery{CaseID: 2})
			return err
		},
	}
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadRequest} {
		for name, call := range calls {
			t.Run(name+"/"+http.StatusText(status), func(t *testing.T) {
				s := newHeaderSpy(t)
				s.failStatus = status
				s.echoBody = true
				err := call(s.client())
				if err == nil {
					t.Fatalf("expected an error from HTTP %d", status)
				}
				msg := err.Error()
				for label, secret := range secrets {
					if strings.Contains(msg, secret) {
						t.Errorf("%s leaked into the error message: %s", label, msg)
					}
				}
				if strings.Contains(msg, testUser) {
					t.Errorf("username leaked into the error message: %s", msg)
				}
			})
		}
	}
}

// The sign-in body carries the password. A failing handshake must not surface
// it either -- this is the path the new endpoints reach it through.
func TestSignInFailure_DoesNotLeakThePassword(t *testing.T) {
	s := newHeaderSpy(t)
	s.failStatus = http.StatusInternalServerError
	s.echoBody = true
	_, err := s.client().ListCases(t.Context(), CaseQuery{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), testPass) {
		t.Fatalf("password leaked from a failed handshake: %v", err)
	}
}
