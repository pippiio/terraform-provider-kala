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
	if len(got.EmployeeNumbers) != 1 || got.EmployeeNumbers[0] != 3 {
		t.Errorf("EmployeeNumbers = %v, want [3]", got.EmployeeNumbers)
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
	if len(got.EmployeeNumbers) == 0 {
		t.Fatal("EmployeeNumbers is empty; the assertions above would pass trivially")
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
	if len(got.EmployeeNumbers) != len(want) {
		t.Fatalf("EmployeeNumbers = %v, want %v (deduplicated and sorted)", got.EmployeeNumbers, want)
	}
	for i, w := range want {
		if got.EmployeeNumbers[i] != w {
			t.Errorf("EmployeeNumbers[%d] = %d, want %d (got %v)", i, got.EmployeeNumbers[i], w, got.EmployeeNumbers)
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
	// Both numbers, so the operator can see how far the read got.
	for _, want := range []string{"2", "5"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name the count %s; got: %v", want, err)
		}
	}
}

// TestGetCaseAccess_DecodesFractionalNumerics is the regression guard for spike
// finding F-2, and it proves the guard guards something rather than asserting it.
//
// The payload is the shape of the ARCHIVED case in the development tenant, which
// carries registeredHoursTotal = 0.25. The narrow type must decode it; the
// shipped wireCaseDetail must NOT, and that second assertion is the point. If
// someone "tidies up" caseaccess.go to reuse wireCaseDetail, the first assertion
// fails. If the shipped bug is ever fixed, the second fails and this test should
// then be simplified rather than deleted.
//
// It is also the unit-level evidence for AC13: the same code path serves archived
// and unarchived cases, there being no archival branch at all. The live evidence
// is the probe, which read the archived case successfully (spike F-3, F-6).
func TestGetCaseAccess_DecodesFractionalNumerics(t *testing.T) {
	m := newCaseAccessMock(t)
	m.detail = accessDetail(1, false, 2, accessItem(7, 3), accessItem(8, 3))

	got, err := m.client().GetCaseAccess(context.Background(), "KA-1")
	if err != nil {
		t.Fatalf("the narrow decode must survive a fractional numeric, got: %v", err)
	}
	if len(got.EmployeeNumbers) != 1 || got.EmployeeNumbers[0] != 3 {
		t.Errorf("EmployeeNumbers = %v, want [3]", got.EmployeeNumbers)
	}

	// The other half of the guard: the shipped wide decode still fails on this
	// exact payload, so the narrow type is doing real work.
	raw, err := json.Marshal(m.detail)
	if err != nil {
		t.Fatalf("marshalling the fixture: %v", err)
	}
	var wide wireCaseDetail
	if err := json.Unmarshal(raw, &wide); err == nil {
		t.Error("wireCaseDetail decoded a fractional registeredHoursTotal; " +
			"spike F-2 appears fixed, so simplify this test rather than deleting it")
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
