package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// caseAccessMock serves the handshake plus GET /api/GetJobDetailsAdvanced/.
//
// The detail payload is supplied per test so each case documents the upstream
// shape it depends on. Shapes copied from the payload observed 2026-09-25
// against tenant 17221 (see the track's spike-findings.md); values fictional.
type caseAccessMock struct {
	srv *httptest.Server

	detail       map[string]any // encoded when detailStatus is 0
	detailRaw    string         // when non-empty, written verbatim instead
	detailStatus int            // when non-zero, returned instead of a body

	lastQuery  string
	detailHits int

	// GET /api/GrantedWorkers/ -- Kala's access list, observed 2026-09-29 as
	// {"grantedWorkers":[<workerNr>...],"rolesEnabled":<bool>}.
	granted       []int64
	grantedStatus int    // when non-zero, returned instead of a body
	grantedRaw    string // when non-empty, written verbatim instead
	grantedQuery  string
	grantedHits   int
}

func newCaseAccessMock(t *testing.T) *caseAccessMock {
	t.Helper()
	m := &caseAccessMock{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 4242, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})

		case strings.HasSuffix(r.URL.Path, "/api/GetJobDetailsAdvanced/"):
			m.detailHits++
			m.lastQuery = r.URL.RawQuery
			if m.detailStatus != 0 {
				w.WriteHeader(m.detailStatus)
				return
			}
			if m.detailRaw != "" {
				_, _ = w.Write([]byte(m.detailRaw))
				return
			}
			_ = json.NewEncoder(w).Encode(m.detail)

		case strings.HasSuffix(r.URL.Path, "/api/GrantedWorkers/"):
			m.grantedHits++
			m.grantedQuery = r.URL.RawQuery
			if m.grantedStatus != 0 {
				w.WriteHeader(m.grantedStatus)
				return
			}
			if m.grantedRaw != "" {
				_, _ = w.Write([]byte(m.grantedRaw))
				return
			}
			g := m.granted
			if g == nil {
				g = []int64{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"grantedWorkers": g, "rolesEnabled": false})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *caseAccessMock) client() InternalClient {
	return NewInternal(InternalConfig{
		Endpoint: m.srv.URL, Username: "u", Password: "p",
		MaxRetries: 2, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond,
	})
}

// accessItem builds one checklistItems element carrying the given workerNrs.
//
// It deliberately includes registeredHoursTotal as a DECIMAL and the personal
// fields the real collection carries, so every test exercises the two things
// that actually matter: that a fractional numeric does not break the decode,
// and that personal data has somewhere to leak from if the mapping is wrong.
func accessItem(id int, workerNrs ...int64) map[string]any {
	workers := make([]any, 0, len(workerNrs))
	for _, nr := range workerNrs {
		workers = append(workers, map[string]any{
			"workerNr": nr, "name": "Frodo Baggins", "phone": "31620005",
			"title": "Senior Ring Bearer", "initials": "FB",
			"workerImage": nil, "isValidated": true,
		})
	}
	return map[string]any{
		"Id": id, "caseId": 2, "caseNr": "KA-1", "name": "Fit the door",
		"registeredHoursTotal": 0.25, // fractional on purpose — see spike F-2
		"isFinished":           false,
		"workersAssigned":      workers,
	}
}

// accessDetail builds a GetJobDetailsAdvanced payload around the given items.
func accessDetail(caseID int64, restricted bool, total int, items ...map[string]any) map[string]any {
	elems := make([]any, 0, len(items))
	for _, it := range items {
		elems = append(elems, it)
	}
	return map[string]any{
		"caseId": caseID, "caseNumber": "KA-1", "caseName": "Roof works",
		"restricted": restricted, "internalProject": false,
		"checklistItemsTotal": total, "checklistItemsCompleted": 0,
		"registeredHoursTotal": 1.75, // fractional at case level too
		"cost":                 12345,
		"checklistItems":       elems,
	}
}

func TestGetCaseAccess_ReadsGrantFromCaseDetail(t *testing.T) {
	m := newCaseAccessMock(t)
	m.detail = accessDetail(2, true, 1, accessItem(7, 3))

	got, err := m.client().GetCaseAccess(context.Background(), "KA-1")
	if err != nil {
		t.Fatalf("GetCaseAccess: %v", err)
	}

	if got.CaseID != 2 {
		t.Errorf("CaseID = %d, want 2", got.CaseID)
	}
	if !got.Restricted {
		t.Error("Restricted = false, want true")
	}
	if len(got.Assigned) != 1 || got.Assigned[0] != 3 {
		t.Errorf("Assigned = %v, want [3]", got.Assigned)
	}

	// One call. The grant is in the case detail, so there is nothing to follow up.
	if m.detailHits != 1 {
		t.Errorf("GetJobDetailsAdvanced called %d times, want exactly 1", m.detailHits)
	}
	if !strings.Contains(m.lastQuery, "caseNr=KA-1") {
		t.Errorf("query was %q, want it to carry caseNr=KA-1", m.lastQuery)
	}
}

// TestGetCaseAccess_CarriesIdentifiersOnly is a REGRESSION GUARD, not a test
// that drove code: wireCaseAccessWorker declares only workerNr, so this passes
// the first time it runs. That is worth stating rather than hiding, because a
// test passing immediately is normally a sign of testing the wrong thing.
//
// What it guards is real. The upstream collection carries name, phone, title,
// initials and workerImage, and the only thing keeping them out of Terraform
// state is that the wire struct does not mention them. Adding one field to that
// struct -- the obvious thing to do when someone wants to show names in the data
// source -- would breach SEC1.5 silently. This fails if that happens.
//
// It asserts over the marshalled result rather than field by field, so it
// catches a new field regardless of what it is called.
func TestGetCaseAccess_CarriesIdentifiersOnly(t *testing.T) {
	m := newCaseAccessMock(t)
	m.detail = accessDetail(2, true, 2, accessItem(7, 3), accessItem(8, 4))

	got, err := m.client().GetCaseAccess(context.Background(), "KA-1")
	if err != nil {
		t.Fatalf("GetCaseAccess: %v", err)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling the result: %v", err)
	}

	// Every personal-data value accessItem puts on the wire.
	for _, personal := range []string{
		"Frodo Baggins", "31620005", "Senior Ring Bearer", "FB",
	} {
		if strings.Contains(string(encoded), personal) {
			t.Errorf("CaseAccess carries %q; the wire struct must not declare personal fields (SEC1.5).\nGot: %s",
				personal, encoded)
		}
	}

	// And prove the read actually produced something, so an empty result
	// cannot satisfy the assertions above.
	if len(got.Assigned) == 0 {
		t.Fatal("Assigned is empty; the assertions above would pass trivially")
	}
}

// TestGetCaseAccess_DeduplicatesAndSorts drives real behaviour.
//
// A job link is shared per (worker, case), so a worker assigned to five of a
// case's items appears in workersAssigned five times. The set must report them
// once. Sorting is asserted here too: the provider layer turns this into an
// unordered set, but a client returning arbitrary order makes its own tests
// flaky, so the client's contract is deterministic.
func TestGetCaseAccess_DeduplicatesAndSorts(t *testing.T) {
	m := newCaseAccessMock(t)
	m.detail = accessDetail(2, true, 3,
		accessItem(7, 9, 3),
		accessItem(8, 3, 5),
		accessItem(9, 9),
	)

	got, err := m.client().GetCaseAccess(context.Background(), "KA-1")
	if err != nil {
		t.Fatalf("GetCaseAccess: %v", err)
	}

	want := []int64{3, 5, 9}
	if len(got.Assigned) != len(want) {
		t.Fatalf("Assigned = %v, want %v (deduplicated and sorted)", got.Assigned, want)
	}
	for i, w := range want {
		if got.Assigned[i] != w {
			t.Errorf("Assigned[%d] = %d, want %d (got %v)", i, got.Assigned[i], w, got.Assigned)
		}
	}
}

// TestGetCaseAccess_TruncatedItemsErrorsNamingBothCounts covers tasks 2.6 and
// 2.7, which finding F-1 collapsed into one behaviour: with the items embedded
// rather than paginated, "an incomplete read" IS the count mismatch.
//
// checklistItemsTotal is what makes this detectable at all. Without it a
// truncated payload would yield a short grant that looks complete, and a short
// grant is indistinguishable from a case fewer people are assigned to.
func TestGetCaseAccess_TruncatedItemsErrorsNamingBothCounts(t *testing.T) {
	m := newCaseAccessMock(t)
	// Upstream says five items; it serves two.
	m.detail = accessDetail(2, true, 5, accessItem(7, 3), accessItem(8, 4))

	_, err := m.client().GetCaseAccess(context.Background(), "KA-1")
	if err == nil {
		t.Fatal("a truncated payload must error, not return a short grant")
	}
	// Assert the PHRASE, not the digits. Asserting on "2" and "5" separately
	// would match incidentally -- "500" from the server-error path contains
	// both -- so the test would pass against the wrong failure.
	if !strings.Contains(err.Error(), "returned 2 of 5") {
		t.Errorf("error must name how far the read got and how far it should have, "+
			"as \"returned 2 of 5\"; got: %v", err)
	}
	// And it must not be mistaken for a missing case: the case exists, the
	// response was short.
	if errors.Is(err, ErrNotFound) {
		t.Errorf("a truncated payload is not an absent case; got: %v", err)
	}
}

// TestGetCaseAccess_DecodesFractionalNumerics keeps the case-access read immune
// to decimals, whatever the rest of the client does.
//
// The payload is the shape of the ARCHIVED case in the development tenant, which
// carries registeredHoursTotal = 0.25. Until 2026-09-27 this test also asserted
// that the shipped wireCaseDetail FAILED on the same payload -- that was what
// proved the narrow type was doing real work. The decimal decode bug is now
// fixed, the second assertion fired as designed, and this test was simplified
// as its own failure message instructed, rather than deleted.
//
// It remains the unit-level evidence for AC13: the same code path serves archived
// and unarchived cases, there being no archival branch at all. The live evidence
// is the probe, which read the archived case successfully (spike F-3, F-6).
func TestGetCaseAccess_DecodesFractionalNumerics(t *testing.T) {
	m := newCaseAccessMock(t)
	m.detail = accessDetail(1, false, 2, accessItem(7, 3), accessItem(8, 3))

	got, err := m.client().GetCaseAccess(context.Background(), "KA-1")
	if err != nil {
		t.Fatalf("the narrow decode must survive a fractional numeric, got: %v", err)
	}
	if len(got.Assigned) != 1 || got.Assigned[0] != 3 {
		t.Errorf("Assigned = %v, want [3]", got.Assigned)
	}
}

// TestGetCaseAccess_EmptyBodyMeansNotFound follows the convention established
// against the real API: Kala answers some unknown identifiers with HTTP 200 and
// no body, so an empty body is absence, not a decode failure.
func TestGetCaseAccess_EmptyBodyMeansNotFound(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty", ""},
		{"whitespace only", "  \n\t "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newCaseAccessMock(t)
			m.detailRaw = tc.body
			if tc.body == "" {
				m.detailRaw = " " // force the raw path; "" means "encode m.detail"
			}

			_, err := m.client().GetCaseAccess(context.Background(), "KA-1")
			if !errorIsNotFound(err) {
				t.Errorf("an empty body must mean not-found, got %v", err)
			}
		})
	}
}

// TestGetCaseAccess_PersistentServerErrorDoesNotClaimAbsence covers AC14.
//
// An unknown caseNr answers HTTP 500, so absence and a server fault are
// indistinguishable at this endpoint. cases.go:334 resolves that by reporting
// ErrNotFound, which is defensible — but the MESSAGE must not assert the case
// does not exist, because during an outage that sends the operator hunting for a
// typo that is not there.
func TestGetCaseAccess_PersistentServerErrorDoesNotClaimAbsence(t *testing.T) {
	m := newCaseAccessMock(t)
	m.detailStatus = http.StatusInternalServerError

	_, err := m.client().GetCaseAccess(context.Background(), "KA-1")
	if err == nil {
		t.Fatal("a persistent 500 must error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "KA-1") {
		t.Errorf("the error must name the case number; got: %v", err)
	}
	// It must acknowledge the ambiguity rather than state absence as fact.
	if !strings.Contains(msg, "500") {
		t.Errorf("the error must disclose that upstream answered 500, so the operator "+
			"can tell absence from an outage; got: %v", err)
	}
}

func errorIsNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), ErrNotFound.Error())
}

// TestGetCaseAccess_NonServerErrorIsNotReportedAsAbsence keeps the ErrServer
// special case honest.
//
// GetCaseAccess translates a persistent 500 into not-found because this endpoint
// answers 500 for an unknown case number. Nothing else may be translated that
// way: an expired session or a refused credential has nothing to do with whether
// the case exists, and reporting it as "no such case" would send the operator
// looking for a typo while their credentials are the problem.
func TestGetCaseAccess_NonServerErrorIsNotReportedAsAbsence(t *testing.T) {
	m := newCaseAccessMock(t)
	m.detailStatus = http.StatusUnauthorized

	_, err := m.client().GetCaseAccess(context.Background(), "KA-1")
	if err == nil {
		t.Fatal("a 401 must error")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("want ErrUnauthorized propagated unchanged, got %v", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("a credential failure must not be reported as a missing case, got %v", err)
	}
}

// TestGetCaseAccess_MalformedBodyIsADecodeError separates a genuinely broken
// response from an absent one. An empty body means the case is missing; a body
// that is present but not JSON means the endpoint or the client is wrong, and
// those two must not produce the same diagnostic.
func TestGetCaseAccess_MalformedBodyIsADecodeError(t *testing.T) {
	m := newCaseAccessMock(t)
	m.detailRaw = `{"caseId": 2, "checklistItems": [`

	_, err := m.client().GetCaseAccess(context.Background(), "KA-1")
	if !errors.Is(err, ErrDecode) {
		t.Errorf("want ErrDecode for a truncated JSON body, got %v", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("a malformed body is not an absent case, got %v", err)
	}
}

// --- Granted: Kala's own access list ----------------------------------------
//
// The operator established on 2026-09-29 that Kala keeps TWO lists per case:
// employees GRANTED access (GET /api/GrantedWorkers/) and employees ASSIGNED to
// its tasks. They are different things. On a restricted case every assigned
// employee should also be granted, or they cannot see the tasks they are
// assigned to -- and case KA-2 was found live with exactly that broken: employee
// 23 assigned, access revoked.

// The shape of case KA-2 as observed: restricted, 23 assigned but not granted.
func TestGetCaseAccess_ReadsGrantedAndAssignedSeparately(t *testing.T) {
	m := newCaseAccessMock(t)
	m.detail = accessDetail(2, true, 1, accessItem(7, 1, 23))
	m.granted = []int64{1}

	got, err := m.client().GetCaseAccess(context.Background(), "KA-2")
	if err != nil {
		t.Fatalf("GetCaseAccess: %v", err)
	}
	if len(got.Granted) != 1 || got.Granted[0] != 1 {
		t.Errorf("Granted = %v, want [1] — Kala's list, not the assignments", got.Granted)
	}
	if len(got.Assigned) != 2 || got.Assigned[0] != 1 || got.Assigned[1] != 23 {
		t.Errorf("Assigned = %v, want [1 23]", got.Assigned)
	}
	if m.grantedHits != 1 {
		t.Errorf("GrantedWorkers called %d times, want 1", m.grantedHits)
	}
	if !strings.Contains(m.grantedQuery, "caseNr=KA-2") {
		t.Errorf("GrantedWorkers query was %q, want caseNr=KA-2", m.grantedQuery)
	}
}

func TestGetCaseAccess_GrantedIsDeduplicatedAndSorted(t *testing.T) {
	m := newCaseAccessMock(t)
	m.detail = accessDetail(2, true, 1, accessItem(7, 1))
	m.granted = []int64{9, 3, 9}

	got, err := m.client().GetCaseAccess(context.Background(), "KA-2")
	if err != nil {
		t.Fatalf("GetCaseAccess: %v", err)
	}
	if len(got.Granted) != 2 || got.Granted[0] != 3 || got.Granted[1] != 9 {
		t.Errorf("Granted = %v, want [3 9]", got.Granted)
	}
}

// An unreadable access list must never become an empty one. On a restricted
// case an empty list states that NOBODY has access -- a confident, wrong answer.
// The case is known to exist (its detail read succeeded), so none of these is
// "not found" either.
func TestGetCaseAccess_UnreadableGrantsAreAnErrorNotAnEmptyList(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		raw    string
	}{
		{"persistent 500", http.StatusInternalServerError, ""},
		{"empty body", 0, " "},
		{"key missing", 0, `{"rolesEnabled":false}`},
		{"not JSON", 0, `<html>nope</html>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newCaseAccessMock(t)
			m.detail = accessDetail(2, true, 1, accessItem(7, 1))
			m.grantedStatus = tc.status
			m.grantedRaw = tc.raw

			got, err := m.client().GetCaseAccess(context.Background(), "KA-2")
			if err == nil {
				t.Fatalf("want an error; got Granted=%v, which would read as a real answer", got.Granted)
			}
			if errors.Is(err, ErrNotFound) {
				t.Errorf("the case exists — its detail read succeeded — so this is not not-found: %v", err)
			}
		})
	}
}

// The two reads are ordered: the detail read establishes that the case exists,
// so a failure there must stop before the access list is requested. No half
// result, and no second request against a case already known to be missing.
func TestGetCaseAccess_DetailFailureStopsBeforeGrants(t *testing.T) {
	m := newCaseAccessMock(t)
	m.detailStatus = http.StatusInternalServerError

	if _, err := m.client().GetCaseAccess(context.Background(), "KA-9"); err == nil {
		t.Fatal("a failed detail read must error")
	}
	if m.grantedHits != 0 {
		t.Errorf("GrantedWorkers was called %d times after the detail read failed", m.grantedHits)
	}
}
