package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type caseWriteMock struct {
	srv     *httptest.Server
	paths   []string
	queries []url.Values
	bodies  []map[string]any

	archived         bool
	noArchiveReflect bool

	// failAt fails any request whose path contains it.
	failAt string
	// badCreateBody makes CreateCase answer with something undecodable.
	badCreateBody bool
	// createWithoutNumber makes CreateCase report success but allocate nothing.
	createWithoutNumber bool
}

func newCaseWriteMock(t *testing.T) *caseWriteMock {
	t.Helper()
	m := &caseWriteMock{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":4242}]}`))
			return
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
			return
		}
		m.paths = append(m.paths, r.URL.Path)
		m.queries = append(m.queries, r.URL.Query())

		if m.failAt != "" && strings.Contains(r.URL.Path, m.failAt) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		switch {
		case strings.Contains(r.URL.Path, "GetAllJobsSimplePaged"):
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			// The real key is "archivedJobs". Guessing "archived" made the
			// mock serve an empty set forever, so verification never passed.
			wantArchived, _ := req["archivedJobs"].(bool)
			cases := []any{}
			if wantArchived == m.archived {
				cases = append(cases, map[string]any{
					"caseId": 4, "caseNumber": "KA-4", "caseName": "Test Project 12",
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"cases": cases, "totalCount": len(cases)})
		case strings.Contains(r.URL.Path, "GetJobDetailsAdvanced"):
			// CreateCase reads the case back, so the mock must serve the detail
			// endpoint or the create can never be observed to succeed.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"caseId": 4, "caseNumber": "KA-4", "caseName": "New Job",
				"internalProject": false, "isFinished": false,
			})
		case strings.Contains(r.URL.Path, "ArchiveCase"):
			if !m.noArchiveReflect {
				m.archived = r.URL.Query().Get("archive") == "true"
			}
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			m.bodies = append(m.bodies, body)
			switch {
			case strings.Contains(r.URL.Path, "CreateCase"):
				switch {
				case m.badCreateBody:
					_, _ = w.Write([]byte(`<html>not json</html>`))
				case m.createWithoutNumber:
					_, _ = w.Write([]byte(`{"caseId":0}`))
				default:
					_, _ = w.Write([]byte(`{"caseId":4,"caseNumber":"KA-4","economyCaseNumber":"KA-4","caseName":"New Job","internalProject":false}`))
				}
			default:
				_, _ = w.Write([]byte(`{"success":true}`))
			}
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *caseWriteMock) client() InternalClient {
	return NewInternal(InternalConfig{
		Endpoint: m.srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond,
	})
}

func (m *caseWriteMock) lastBody() map[string]any {
	if len(m.bodies) == 0 {
		return nil
	}
	return m.bodies[len(m.bodies)-1]
}

// CreateCase lives at CAPITAL /Api/, alone among the case endpoints, and
// returns the identity it allocated.
func TestCreateCase_PathAndAllocatedIdentity(t *testing.T) {
	m := newCaseWriteMock(t)
	got, err := m.client().CreateCase(context.Background(), NewCase{
		Name: "New Job", WorkerNr: 1, CustomerNumber: "KA-1", Address: "Site St", Zip: "2200",
	})
	if err != nil {
		t.Fatalf("CreateCase: %v", err)
	}
	if len(m.paths) == 0 || !strings.HasSuffix(m.paths[0], "/Api/CreateCase/") {
		t.Errorf("path = %v, want suffix /Api/CreateCase/ (capital Api is load-bearing)", m.paths)
	}
	if got.ID != 4 || got.Number != "KA-4" {
		t.Errorf("got id=%d number=%q, want the allocated 4 / KA-4", got.ID, got.Number)
	}
}

// ADR-003 constraint 0: a write must never change what the operator did not
// declare. newCustomer:true would create a customer as a side effect of
// creating a case -- and Kala cannot delete customers.
func TestCreateCase_NeverAsksKalaToCreateACustomer(t *testing.T) {
	for _, in := range []NewCase{
		{Name: "Job", WorkerNr: 1, CustomerNumber: "KA-1"},
		{Name: "Internal", WorkerNr: 1, InternalProject: true},
	} {
		m := newCaseWriteMock(t)
		if _, err := m.client().CreateCase(context.Background(), in); err != nil {
			t.Fatalf("CreateCase: %v", err)
		}
		if v, ok := m.lastBody()["newCustomer"]; !ok || v != false {
			t.Errorf("newCustomer = %v (present=%t), must always be false", v, ok)
		}
	}
}

// The two shapes differ by OMISSION, not by blanking. An internal project
// carries no customer block at all.
func TestCreateCase_InternalProjectOmitsTheCustomerBlock(t *testing.T) {
	m := newCaseWriteMock(t)
	if _, err := m.client().CreateCase(context.Background(), NewCase{
		Name: "Internal", WorkerNr: 1, InternalProject: true, Address: "HQ",
	}); err != nil {
		t.Fatalf("CreateCase: %v", err)
	}
	body := m.lastBody()
	if body["internalProject"] != true {
		t.Errorf("internalProject = %v, want true", body["internalProject"])
	}
	for _, key := range []string{"customerNr", "company", "cvr", "email", "phone", "zip"} {
		if _, present := body[key]; present {
			t.Errorf("internal project sent %q; the customer block must be OMITTED, not blanked", key)
		}
	}
}

func TestCreateCase_CustomerFacingCarriesTheCustomerNumber(t *testing.T) {
	m := newCaseWriteMock(t)
	if _, err := m.client().CreateCase(context.Background(), NewCase{
		Name: "Job", WorkerNr: 1, CustomerNumber: "KA-1",
	}); err != nil {
		t.Fatalf("CreateCase: %v", err)
	}
	if m.lastBody()["customerNr"] != "KA-1" {
		t.Errorf("customerNr = %v, want the STRING KA-1", m.lastBody()["customerNr"])
	}
	if m.lastBody()["internalProject"] != false {
		t.Errorf("internalProject = %v, want false", m.lastBody()["internalProject"])
	}
}

// Archive is a WRITE PERFORMED BY GET, with query parameters.
func TestSetCaseArchived_UsesGETWithQueryParameters(t *testing.T) {
	m := newCaseWriteMock(t)
	if err := m.client().SetCaseArchived(context.Background(), "KA-4", true); err != nil {
		t.Fatalf("SetCaseArchived: %v", err)
	}
	var q url.Values
	for i, p := range m.paths {
		if strings.Contains(p, "ArchiveCase") {
			q = m.queries[i]
		}
	}
	if q.Get("archive") != "true" || q.Get("caseNr") != "KA-4" {
		t.Errorf("query = %v, want archive=true & caseNr=KA-4", q)
	}
}

// `archived` is not a response field -- it is derived from WHICH SET the case
// appears in. Verification therefore means confirming it moved between the two
// disjoint lists (ADR-003, AC8).
func TestSetCaseArchived_VerifiedBySetMembership(t *testing.T) {
	m := newCaseWriteMock(t)
	m.noArchiveReflect = true
	if err := m.client().SetCaseArchived(context.Background(), "KA-4", true); err == nil {
		t.Fatal("an archive that did not move the case between sets must fail verification")
	}
}

func TestSetCaseArchived_RoundTrips(t *testing.T) {
	c := newCaseWriteMock(t).client()
	if err := c.SetCaseArchived(context.Background(), "KA-4", true); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := c.SetCaseArchived(context.Background(), "KA-4", false); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
}

// The identifier key changes with DIRECTION: newCustomerId when assigning a
// customer, oldCustomerId when converting to internal. Same endpoint, same
// relationship, two names.
func TestSetCaseCustomer_KeyDependsOnDirection(t *testing.T) {
	t.Run("assign a customer", func(t *testing.T) {
		m := newCaseWriteMock(t)
		if err := m.client().SetCaseCustomer(context.Background(), "KA-1", 2, false); err != nil {
			t.Fatalf("SetCaseCustomer: %v", err)
		}
		b := m.lastBody()
		if b["newCustomerId"] == nil {
			t.Errorf("assigning must key on newCustomerId, got %v", b)
		}
		if b["internalProject"] != false {
			t.Errorf("internalProject = %v, want false", b["internalProject"])
		}
	})
	t.Run("convert to internal", func(t *testing.T) {
		m := newCaseWriteMock(t)
		if err := m.client().SetCaseCustomer(context.Background(), "KA-1", 1, true); err != nil {
			t.Fatalf("SetCaseCustomer: %v", err)
		}
		b := m.lastBody()
		if b["oldCustomerId"] == nil {
			t.Errorf("converting to internal must key on oldCustomerId, got %v", b)
		}
		if b["internalProject"] != true {
			t.Errorf("internalProject = %v, want true", b["internalProject"])
		}
	})
}

// updateCustomerAddress:true would overwrite the case address as a side effect
// of changing the customer -- a change the plan never mentioned.
func TestSetCaseCustomer_NeverOverwritesTheCaseAddress(t *testing.T) {
	m := newCaseWriteMock(t)
	if err := m.client().SetCaseCustomer(context.Background(), "KA-1", 2, false); err != nil {
		t.Fatalf("SetCaseCustomer: %v", err)
	}
	if v, ok := m.lastBody()["updateCustomerAddress"]; !ok || v != false {
		t.Errorf("updateCustomerAddress = %v (present=%t), must always be false", v, ok)
	}
}

func customerFacing() NewCase {
	return NewCase{Name: "Job", WorkerNr: 1, CustomerNumber: "KA-1"}
}

func TestCreateCase_HTTPFailurePropagates(t *testing.T) {
	m := newCaseWriteMock(t)
	m.failAt = "CreateCase"
	if _, err := m.client().CreateCase(context.Background(), customerFacing()); err == nil {
		t.Fatal("an HTTP failure on create must propagate")
	}
}

func TestCreateCase_UndecodableResponseIsADecodeError(t *testing.T) {
	m := newCaseWriteMock(t)
	m.badCreateBody = true
	if _, err := m.client().CreateCase(context.Background(), customerFacing()); err == nil {
		t.Fatal("an undecodable create response must be an error")
	}
}

// A create that reports success without a case number leaves a record that
// cannot be addressed -- the same failure shape AddCustomer guards against.
func TestCreateCase_SuccessWithoutANumberIsAnError(t *testing.T) {
	m := newCaseWriteMock(t)
	m.createWithoutNumber = true
	if _, err := m.client().CreateCase(context.Background(), customerFacing()); err == nil {
		t.Fatal("success without a case number must be an error; the case cannot be identified")
	}
}

// FR5. The case exists upstream and Kala cannot delete it, so its identity
// travels with the error rather than being discarded.
func TestCreateCase_ReadBackFailureStillReturnsTheIdentity(t *testing.T) {
	m := newCaseWriteMock(t)
	m.failAt = "GetJobDetailsAdvanced"

	got, err := m.client().CreateCase(context.Background(), customerFacing())
	if err == nil {
		t.Fatal("want an error when the created case cannot be read back")
	}
	if got.Number != "KA-4" || got.ID != 4 {
		t.Errorf("got id=%d number=%q; the identity must survive the failure, "+
			"because the case exists and cannot be deleted", got.ID, got.Number)
	}
}

func TestSetCaseArchived_HTTPFailurePropagates(t *testing.T) {
	m := newCaseWriteMock(t)
	m.failAt = "ArchiveCase"
	if err := m.client().SetCaseArchived(context.Background(), "KA-4", true); err == nil {
		t.Fatal("an HTTP failure on archive must propagate")
	}
}

func TestSetCaseArchived_ReadBackFailureIsReported(t *testing.T) {
	m := newCaseWriteMock(t)
	m.failAt = "GetAllJobsSimplePaged"
	err := m.client().SetCaseArchived(context.Background(), "KA-4", true)
	if err == nil {
		t.Fatal("an archive that cannot be verified must not be reported as success")
	}
	if !strings.Contains(err.Error(), "read back") {
		t.Errorf("the message must say verification failed: %v", err)
	}
}

// The failure message names which set the case was expected in; both spellings
// must appear for the right direction.
func TestSetCaseArchived_UnarchiveFailureNamesTheActiveSet(t *testing.T) {
	m := newCaseWriteMock(t)
	m.archived = true
	m.noArchiveReflect = true
	err := m.client().SetCaseArchived(context.Background(), "KA-4", false)
	if err == nil {
		t.Fatal("want a verification failure")
	}
	if !strings.Contains(err.Error(), "active set") {
		t.Errorf("the message must name the set the case should have moved to: %v", err)
	}
}

func TestSetCaseCustomer_FailurePropagates(t *testing.T) {
	m := newCaseWriteMock(t)
	m.failAt = "ChangeCaseCustomer"
	if err := m.client().SetCaseCustomer(context.Background(), "KA-1", 2, false); err == nil {
		t.Fatal("a failed customer change must report an error")
	}
}
