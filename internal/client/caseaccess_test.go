package client

import (
	"context"
	"encoding/json"
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
