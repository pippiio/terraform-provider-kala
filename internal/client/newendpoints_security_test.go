package client

import (
	"context"
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

	// handshakeOK lets the SignIn/SelectCompany pair succeed even when
	// failStatus is set, so a test can fail ONLY the endpoint call. Without
	// it the handshake fails first and the request body under test is never
	// sent -- which makes any assertion about that body vacuous.
	handshakeOK bool
}

func newHeaderSpy(t *testing.T) *headerSpy {
	t.Helper()
	s := &headerSpy{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			if s.echoBody && s.failStatus != 0 && !s.handshakeOK {
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

// kacompany accompanies kauthtoken on every authed request. Already
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

// no credential may reach an error message. The server here is
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

// --- write endpoints -------------------------------------------------------
//
// Writes differ from reads in a way that matters here: their request BODIES
// carry the record. A server that echoes the request back inside an error --
// which Kala demonstrably does; its webapiv2 500 page returns the full query
// string, api_key included -- therefore surfaces whatever was written.

var writeCalls = map[string]func(InternalClient) error{
	"AddCustomer": func(c InternalClient) error {
		_, err := c.AddCustomer(t0ctx(), piiInput())
		return err
	},
	"EditCustomer": func(c InternalClient) error {
		_, err := c.EditCustomer(t0ctx(), 2, piiInput())
		return err
	},
}

func t0ctx() context.Context { return context.Background() }

// Deliberately distinctive values so a leak is unambiguous rather than a
// coincidental substring match.
func piiInput() CustomerInput {
	return CustomerInput{
		FirstName: "Bilbo", LastName: "Baggins", Company: "Bag End Ltd",
		Phone: "+4520000001", Address: "Bagshot Row 1", Zip: "2200",
		Email: "bilbo-unique-marker@example.com", CVR: "87654321",
		Description: "Managed by Terraform", EAN: "5790000000000",
	}
}

// FR10 for the write path: kacompany accompanies kauthtoken here too. The call
// is expected to fail (the spy serves no matching record to read back); the
// headers are recorded on the request regardless, which is what is asserted.
func TestCustomerWrites_SendCompanyAndAuthHeaders(t *testing.T) {
	for name, call := range writeCalls {
		t.Run(name, func(t *testing.T) {
			s := newHeaderSpy(t)
			_ = call(s.client())
			if s.company != "4242" {
				t.Errorf("kacompany = %q on %s, want 4242", s.company, s.pathSeen)
			}
			if s.authtoken != testToken {
				t.Errorf("kauthtoken = %q on %s", s.authtoken, s.pathSeen)
			}
		})
	}
}

// SEC1.3 on the write path. The sign-in body carrying the password is reached
// through these calls exactly as it is through the reads.
func TestCustomerWrites_CredentialsNeverAppearInErrors(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadRequest} {
		for name, call := range writeCalls {
			t.Run(name+"/"+http.StatusText(status), func(t *testing.T) {
				s := newHeaderSpy(t)
				s.failStatus = status
				s.echoBody = true

				err := call(s.client())
				if err == nil {
					t.Fatalf("expected an error from HTTP %d", status)
				}
				msg := err.Error()
				for label, secret := range map[string]string{
					"password": testPass, "session token": testToken,
				} {
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

// SEC1.5 applied to the write path: log identifiers, not records.
//
// A failing write whose body is echoed back would otherwise put a customer's
// email, phone, and address into a Terraform diagnostic -- and from there into
// CI logs, which are routinely readable by more people than the .tf file is.
// The operator authored this data, but that is not the same as consenting to
// broadcast it on every upstream 500.
func TestCustomerWrites_PersonalDataNeverAppearsInErrors(t *testing.T) {
	pii := map[string]string{
		"email":   "bilbo-unique-marker@example.com",
		"phone":   "+4520000001",
		"address": "Bagshot Row 1",
		"cvr":     "87654321",
	}
	for name, call := range writeCalls {
		t.Run(name, func(t *testing.T) {
			s := newHeaderSpy(t)
			s.failStatus = http.StatusInternalServerError
			s.echoBody = true
			// Without this the handshake fails first and the write body is
			// never sent, making every assertion below vacuously true.
			s.handshakeOK = true

			err := call(s.client())
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(s.pathSeen, "Customer") {
				t.Fatalf("the write was never reached (last path %q); the assertions below would be vacuous", s.pathSeen)
			}
			msg := err.Error()
			for label, value := range pii {
				if strings.Contains(msg, value) {
					t.Errorf("customer %s leaked into the error message: %s", label, msg)
				}
			}
		})
	}
}

// The counterpart to the redaction above, and the reason personalDataKeys is a
// list rather than "redact the whole body": an error that names no record is
// not diagnosable. Identifiers must survive.
func TestCustomerWrites_IdentifiersSurviveRedaction(t *testing.T) {
	s := newHeaderSpy(t)
	s.failStatus = http.StatusInternalServerError
	s.echoBody = true
	s.handshakeOK = true

	_, err := s.client().EditCustomer(t0ctx(), 2, piiInput())
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()

	if !strings.Contains(msg, "customerId") {
		t.Errorf("customerId was redacted; an error naming no record cannot be acted on: %s", msg)
	}
	// The company name is how an operator recognises which customer this is,
	// and it is not personal data -- a company is not a person.
	if !strings.Contains(msg, "Bag End Ltd") {
		t.Errorf("company name was redacted; it is the record's human label, not personal data: %s", msg)
	}
}
