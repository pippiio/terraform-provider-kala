package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// caseMock serves the handshake plus GetAllJobsSimplePaged and
// GetJobDetailsAdvanced. Shapes copied from payloads observed 2026-09-02;
// values fictional.
type caseMock struct {
	srv *httptest.Server

	active   []map[string]any // returned when archivedJobs=false
	archived []map[string]any // returned when archivedJobs=true

	lastBody     map[string]any
	detailStatus int   // when non-zero, GetJobDetailsAdvanced returns this
	detail500s   int32 // number of 500s to serve before succeeding
	served500    int32
	detailHits   int32
}

func newCaseMock(t *testing.T) *caseMock {
	t.Helper()
	m := &caseMock{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 4242, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})

		case strings.HasSuffix(r.URL.Path, "/api/GetAllJobsSimplePaged/"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			m.lastBody = body
			set := m.active
			if arch, _ := body["archivedJobs"].(bool); arch {
				set = m.archived
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"cases": set, "totalCount": len(set)})

		case strings.HasSuffix(r.URL.Path, "/api/GetJobDetailsAdvanced/"):
			atomic.AddInt32(&m.detailHits, 1)
			if n := atomic.LoadInt32(&m.detail500s); n > 0 && atomic.LoadInt32(&m.served500) < n {
				atomic.AddInt32(&m.served500, 1)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if m.detailStatus != 0 {
				w.WriteHeader(m.detailStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"caseId": 1, "caseNumber": "KA-1", "caseName": "Roof works",
				"economyCaseNumber": "KA-1", "address": "Bagshot Row 1", "zip": "1000",
				"subText": "", "customerId": 7, "customersName": "Frodo Baggins",
				"customersCompany": "Bag End Ltd", "customersEmail": "frodo@example.com",
				"customersTelephone": "+45 00 00 00 00",
				"isFinished":         false, "internalProject": false, "restricted": false, "favorite": false,
				"checklistItemsTotal": 3, "checklistItemsCompleted": 1, "economySyncFailed": false,
				"startDate": "/Date(1788333543793)/", "endDate": nil, "deadline": nil,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *caseMock) client() InternalClient {
	return NewInternal(InternalConfig{
		Endpoint: m.srv.URL, Username: "u", Password: "p",
		MaxRetries: 2, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond,
	})
}

func caseRec(id int, nr, name string, isFinished bool) map[string]any {
	return map[string]any{
		"caseId": id, "caseNumber": nr, "caseName": name, "economyCaseNumber": nr,
		"address": "Bagshot Row 1", "zip": "1000", "subText": "",
		"customersName": "Frodo Baggins", "customersCompany": "Bag End Ltd",
		"customersEmail": "frodo@example.com", "customersTelephone": "+45 00 00 00 00",
		// The list's isFinished tracks archived-ness, not completion. It must
		// not reach the domain type at all (FR13).
		"isFinished": isFinished, "internalProject": false, "restricted": false, "favorite": false,
	}
}

// archivedJobs is a mode switch: the two sets are disjoint and there is no
// "everything" option. Verified against the live tenant 2026-09-02.
func TestListCases_ArchivedIsAModeSwitchNotAnInclusionFlag(t *testing.T) {
	m := newCaseMock(t)
	m.active = []map[string]any{caseRec(1, "KA-1", "Roof works", false)}
	m.archived = []map[string]any{caseRec(2, "KA-2", "Internal Test", true)}

	act, err := m.client().ListCases(t.Context(), CaseQuery{Archived: false})
	if err != nil {
		t.Fatalf("ListCases: %v", err)
	}
	if len(act.Cases) != 1 || act.Cases[0].Number != "KA-1" {
		t.Fatalf("Archived=false returned %+v, want only KA-1", act.Cases)
	}
	if got := m.lastBody["archivedJobs"]; got != false {
		t.Errorf("archivedJobs sent as %v, want false", got)
	}

	arc, err := m.client().ListCases(t.Context(), CaseQuery{Archived: true})
	if err != nil {
		t.Fatalf("ListCases: %v", err)
	}
	if len(arc.Cases) != 1 || arc.Cases[0].Number != "KA-2" {
		t.Fatalf("Archived=true returned %+v, want only KA-2", arc.Cases)
	}
	if got := m.lastBody["archivedJobs"]; got != true {
		t.Errorf("archivedJobs sent as %v, want true", got)
	}
}

// Archived comes from the query mode, never from the response. The list's
// isFinished flips with archive state and would be wrong here.
func TestListCases_ArchivedDerivedFromQueryNotFromIsFinished(t *testing.T) {
	m := newCaseMock(t)
	// Deliberately contradictory: an active case whose isFinished is true, and
	// an archived case whose isFinished is false. Neither must influence Archived.
	m.active = []map[string]any{caseRec(1, "KA-1", "Roof works", true)}
	m.archived = []map[string]any{caseRec(2, "KA-2", "Internal Test", false)}

	act, err := m.client().ListCases(t.Context(), CaseQuery{Archived: false})
	if err != nil {
		t.Fatalf("ListCases(Archived=false): %v", err)
	}
	if len(act.Cases) != 1 {
		t.Fatalf("got %d cases from the non-archived query, want 1", len(act.Cases))
	}
	if act.Cases[0].Archived {
		t.Error("Archived=true on a case returned by the non-archived query")
	}

	arc, err := m.client().ListCases(t.Context(), CaseQuery{Archived: true})
	if err != nil {
		t.Fatalf("ListCases(Archived=true): %v", err)
	}
	if len(arc.Cases) != 1 {
		t.Fatalf("got %d cases from the archived query, want 1", len(arc.Cases))
	}
	if !arc.Cases[0].Archived {
		t.Error("Archived=false on a case returned by the archived query")
	}
}

// finishedJobs had no observable effect upstream and must not be varied.
func TestListCases_FinishedJobsIsSentFalseAndNeverVaried(t *testing.T) {
	m := newCaseMock(t)
	m.active = []map[string]any{caseRec(1, "KA-1", "Roof works", false)}
	for _, archived := range []bool{false, true} {
		if _, err := m.client().ListCases(t.Context(), CaseQuery{Archived: archived}); err != nil {
			t.Fatalf("ListCases: %v", err)
		}
		if got := m.lastBody["finishedJobs"]; got != false {
			t.Errorf("finishedJobs = %v with Archived=%v, want false always", got, archived)
		}
	}
}

func TestListCases_MapsCustomerAndIdentityFields(t *testing.T) {
	m := newCaseMock(t)
	m.active = []map[string]any{caseRec(1, "KA-1", "Roof works", false)}
	scan, err := m.client().ListCases(t.Context(), CaseQuery{})
	if err != nil {
		t.Fatalf("ListCases: %v", err)
	}
	if len(scan.Cases) != 1 {
		t.Fatalf("got %d cases, want 1", len(scan.Cases))
	}
	c := scan.Cases[0]
	if c.ID != 1 || c.Number != "KA-1" || c.Name != "Roof works" {
		t.Errorf("identity = %d/%q/%q", c.ID, c.Number, c.Name)
	}
	if c.CustomerName != "Frodo Baggins" || c.CustomerCompany != "Bag End Ltd" {
		t.Errorf("customer = %q/%q", c.CustomerName, c.CustomerCompany)
	}
	if !scan.Complete() {
		t.Error("all records fetched; Complete() must be true")
	}
}

func TestGetCase_ReturnsDetailFieldsAbsentFromTheList(t *testing.T) {
	m := newCaseMock(t)
	d, err := m.client().GetCase(t.Context(), "KA-1")
	if err != nil {
		t.Fatalf("GetCase: %v", err)
	}
	if d.CustomerID != 7 {
		t.Errorf("CustomerID = %d, want 7 -- absent from the list shape", d.CustomerID)
	}
	if d.ChecklistItemsTotal != 3 || d.ChecklistItemsCompleted != 1 {
		t.Errorf("checklist = %d/%d, want 3/1", d.ChecklistItemsTotal, d.ChecklistItemsCompleted)
	}
	if d.IsFinished {
		t.Error("detail IsFinished should be false here; it means completion, not archived-ness")
	}
	if d.StartDate == nil {
		t.Fatal("StartDate not parsed from /Date(1788333543793)/")
	}
	if got := d.StartDate.UTC().Format(time.RFC3339); got != "2026-09-02T07:19:03Z" {
		t.Errorf("StartDate = %s, want 2026-09-02T07:19:03Z", got)
	}
	if d.EndDate != nil || d.Deadline != nil {
		t.Error("null dates must map to nil, not the zero time")
	}
}

// An unknown caseNr returns HTTP 500 upstream, not 404.
func TestGetCase_ConsistentServerErrorBecomesNotFound(t *testing.T) {
	m := newCaseMock(t)
	m.detailStatus = http.StatusInternalServerError
	_, err := m.client().GetCase(t.Context(), "ZZ-999999")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound -- an unknown caseNr 500s upstream", err)
	}
}

// ...but a TRANSIENT 500 must recover through the retry policy rather than
// being reported as a missing case. This is what keeps the mapping above safe.
func TestGetCase_TransientServerErrorRecoversAndIsNotReportedMissing(t *testing.T) {
	m := newCaseMock(t)
	m.detail500s = 1 // one failure, then success
	d, err := m.client().GetCase(t.Context(), "KA-1")
	if err != nil {
		t.Fatalf("a transient 500 must be retried, not reported as not-found: %v", err)
	}
	if d.Number != "KA-1" {
		t.Errorf("Number = %q, want KA-1", d.Number)
	}
	if hits := atomic.LoadInt32(&m.detailHits); hits < 2 {
		t.Errorf("detail endpoint hit %d times, want >= 2 (one failure then a retry)", hits)
	}
}

func TestParseDotNetDate(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantNil bool
		want    string
		wantErr bool
	}{
		{name: "millisecond epoch", in: "/Date(1788333543793)/", want: "2026-09-02T07:19:03Z"},
		{name: "empty is unset", in: "", wantNil: true},
		{name: "dotnet zero date is unset", in: "/Date(-62135596800000)/", wantNil: true},
		{name: "unparseable is an error", in: "not-a-date", wantErr: true},
		{name: "bare digits are an error", in: "1788333543793", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDotNetDate(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for %q, got %v", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("want nil for %q, got %v", tc.in, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("want %s, got nil", tc.want)
			}
			if g := got.UTC().Format(time.RFC3339); g != tc.want {
				t.Errorf("got %s, want %s", g, tc.want)
			}
		})
	}
}

var _ = fmt.Sprintf

// --- error and edge paths ------------------------------------------------

// An empty 200 on the LIST endpoint means zero cases, mirroring customers and
// employees. It must not be read as an error.
func TestListCases_EmptyBodyIsZeroCases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 1, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p",
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond})
	scan, err := c.ListCases(t.Context(), CaseQuery{})
	if err != nil {
		t.Fatalf("empty 200 on a list read must mean zero cases: %v", err)
	}
	if len(scan.Cases) != 0 || !scan.Complete() {
		t.Errorf("got %d cases, Complete=%v; want 0 and true", len(scan.Cases), scan.Complete())
	}
}

func TestListCases_UndecodableBodyIsADecodeError(t *testing.T) {
	c := badBodyClient(t, "/api/GetAllJobsSimplePaged/")
	if _, err := c.ListCases(t.Context(), CaseQuery{}); !errors.Is(err, ErrDecode) {
		t.Fatalf("err = %v, want ErrDecode", err)
	}
}

func TestGetCase_UndecodableBodyIsADecodeError(t *testing.T) {
	c := badBodyClient(t, "/api/GetJobDetailsAdvanced/")
	if _, err := c.GetCase(t.Context(), "KA-1"); !errors.Is(err, ErrDecode) {
		t.Fatalf("err = %v, want ErrDecode", err)
	}
}

func TestGetCase_EmptyBodyIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 1, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p",
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond})
	if _, err := c.GetCase(t.Context(), "KA-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A 401 must surface as unauthorized, NOT be swallowed by the 500-to-not-found
// mapping. Only ErrServer takes that path.
func TestGetCase_UnauthorizedIsNotReportedAsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 1, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p",
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond})
	_, err := c.GetCase(t.Context(), "KA-1")
	if errors.Is(err, ErrNotFound) {
		t.Fatal("a 401 was reported as not-found; only ErrServer may take that path")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

// A date the parser rejects must fail the whole read rather than silently
// yielding a nil timestamp.
func TestGetCase_UnparseableDateFailsTheRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 1, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"caseId": 1, "caseNumber": "KA-1", "startDate": "2026-09-02T07:19:03Z",
			})
		}
	}))
	t.Cleanup(srv.Close)
	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p",
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond})
	_, err := c.GetCase(t.Context(), "KA-1")
	if !errors.Is(err, ErrDecode) {
		t.Fatalf("err = %v, want ErrDecode -- an RFC3339 date is not the .NET wire format", err)
	}
	if !strings.Contains(err.Error(), "startDate") {
		t.Errorf("error should name the offending field, got: %v", err)
	}
}

func TestParseDotNetDate_HandlesTimezoneOffsetSuffix(t *testing.T) {
	got, err := parseDotNetDate("/Date(1788333543793+0200)/")
	if err != nil {
		t.Fatalf("offset suffixes occur in .NET output: %v", err)
	}
	if got == nil {
		t.Fatal("got nil")
	}
	if g := got.UTC().Format(time.RFC3339); g != "2026-09-02T07:19:03Z" {
		t.Errorf("got %s, want 2026-09-02T07:19:03Z -- the epoch is already UTC", g)
	}
}

// badBodyClient serves undecodable JSON on the given path after a successful
// handshake.
func badBodyClient(t *testing.T, path string) InternalClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 1, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})
		case strings.HasSuffix(r.URL.Path, path):
			_, _ = w.Write([]byte(`{"cases": "not-an-array"`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p",
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond})
}

// FR7: financial and hour-registration data is exposed behind an opt-in at the
// provider layer, so the client must be able to supply it. These fields exist
// only on the detail endpoint -- the list shape has none of them.
func TestGetCase_DecodesFinancialFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 1, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"caseId": 1, "caseNumber": "KA-1", "caseName": "Roof works",
				"cost": 1200, "sales": 4000, "result": 2800,
				"invoiced": 1500, "uninvoiced": 2500, "realised": 900,
				"registeredHoursTotal": 37, "billedHours": 30,
			})
		}
	}))
	t.Cleanup(srv.Close)
	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p",
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond})
	d, err := c.GetCase(t.Context(), "KA-1")
	if err != nil {
		t.Fatalf("GetCase: %v", err)
	}
	for name, got := range map[string]int{
		"Cost": d.Cost, "Sales": d.Sales, "Result": d.Result,
		"Invoiced": d.Invoiced, "Uninvoiced": d.Uninvoiced, "Realised": d.Realised,
		"RegisteredHoursTotal": d.RegisteredHoursTotal, "BilledHours": d.BilledHours,
	} {
		if got == 0 {
			t.Errorf("%s = 0; the detail endpoint supplied a value", name)
		}
	}
	if d.Sales != 4000 || d.Result != 2800 {
		t.Errorf("Sales/Result = %d/%d, want 4000/2800", d.Sales, d.Result)
	}
}
