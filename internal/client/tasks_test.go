package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// taskMock serves the handshake plus POST /Case/GetChecklistItemsPaged/.
// Shapes copied from a payload observed 2026-09-02; values fictional.
type taskMock struct {
	srv *httptest.Server

	items    []map[string]any
	total    *int // nil reproduces an unknown caseId: totalCount ABSENT
	lastBody map[string]any
	bodies   []map[string]any
}

func newTaskMock(t *testing.T) *taskMock {
	t.Helper()
	m := &taskMock{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 4242, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})
		case strings.HasSuffix(r.URL.Path, "/Case/GetChecklistItemsPaged/"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			m.lastBody = body
			m.bodies = append(m.bodies, body)
			out := map[string]any{
				"items":                    m.items,
				"caseTotalCount":           4,
				"caseFinishedCount":        1,
				"caseTotalNormTime":        0,
				"caseTotalRegisteredHours": 0,
			}
			if m.total != nil {
				out["totalCount"] = *m.total
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *taskMock) client() InternalClient {
	return NewInternal(InternalConfig{
		Endpoint: m.srv.URL, Username: "u", Password: "p",
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond,
	})
}

func taskRec(id int, name string, respWorkerNr any, finished bool) map[string]any {
	return map[string]any{
		"Id": id, "name": name, "caseId": 2, "caseNr": "KA-2", "checklistId": 11,
		"description": nil, "statusName": "Færdig", "createdBy": "Frodo Baggins",
		"respWorkerNr": respWorkerNr, "workersAssigned": []any{}, "assignedToMe": false,
		"deadline": "/Date(1819869509023)/", "timeAdded": "/Date(1788333543793)/",
		"isFinished": finished, "timeFinished": "/Date(1788333586263)/",
		"workerFinishedBy":     "Frodo Baggins",
		"registeredHoursTotal": 0, "billedHours": 0,
		"invoiceMode": "REG_HOURS&SPECIAL", "priceFixed": 550,
		"noteRequired": false, "imageRequired": false, "hasImage": false,
	}
}

func intp(i int) *int     { return &i }
func i64p(i int64) *int64 { return &i }

// caseId addresses the endpoint; it is not an optional filter. A zero value
// must fail before any request is issued.
func TestListTasks_ZeroCaseIDIsRejectedWithoutARequest(t *testing.T) {
	m := newTaskMock(t)
	m.total = intp(0)
	_, err := m.client().ListTasks(t.Context(), TaskQuery{})
	if err == nil {
		t.Fatal("a zero CaseID must be rejected")
	}
	if len(m.bodies) != 0 {
		t.Errorf("issued %d requests, want 0 -- reject before calling upstream", len(m.bodies))
	}
	if !strings.Contains(strings.ToLower(err.Error()), "case") {
		t.Errorf("error should name the offending field, got: %v", err)
	}
}

// An unknown caseId returns 200 with items:[] and totalCount ABSENT, while a
// real case with no tasks returns totalCount:0. The two must not collapse.
func TestListTasks_AbsentTotalCountMeansUnknownCase(t *testing.T) {
	m := newTaskMock(t)
	m.items = nil
	m.total = nil // absent
	_, err := m.client().ListTasks(t.Context(), TaskQuery{CaseID: 999999})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound -- an absent totalCount means the case does not exist", err)
	}
}

func TestListTasks_ZeroTotalCountMeansCaseExistsWithNoTasks(t *testing.T) {
	m := newTaskMock(t)
	m.items = nil
	m.total = intp(0)
	scan, err := m.client().ListTasks(t.Context(), TaskQuery{CaseID: 2})
	if err != nil {
		t.Fatalf("a real case with no tasks is not an error: %v", err)
	}
	if len(scan.Tasks) != 0 {
		t.Errorf("got %d tasks, want 0", len(scan.Tasks))
	}
	if !scan.Complete() {
		t.Error("an empty case was fully read; Complete() must be true")
	}
}

func TestListTasks_MapsWireToDomain(t *testing.T) {
	m := newTaskMock(t)
	m.items = []map[string]any{taskRec(5, "Secret task", 1, true)}
	m.total = intp(1)
	scan, err := m.client().ListTasks(t.Context(), TaskQuery{CaseID: 2})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(scan.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(scan.Tasks))
	}
	k := scan.Tasks[0]
	if k.ID != 5 {
		t.Errorf("ID = %d, want 5 -- upstream spells this field 'Id', capitalised", k.ID)
	}
	if k.Name != "Secret task" || k.CaseID != 2 || k.CaseNumber != "KA-2" || k.ChecklistID != 11 {
		t.Errorf("identity = %q/%d/%q/%d", k.Name, k.CaseID, k.CaseNumber, k.ChecklistID)
	}
	if k.StatusName != "Færdig" {
		t.Errorf("StatusName = %q -- upstream language, not translated", k.StatusName)
	}
	if k.AssigneeWorkerNr == nil || *k.AssigneeWorkerNr != 1 {
		t.Errorf("AssigneeWorkerNr = %v, want 1", k.AssigneeWorkerNr)
	}
	if !k.IsFinished || k.FinishedBy != "Frodo Baggins" {
		t.Errorf("completion = %v/%q", k.IsFinished, k.FinishedBy)
	}
	if k.TimeAdded == nil || k.TimeAdded.UTC().Format(time.RFC3339) != "2026-09-02T07:19:03Z" {
		t.Errorf("TimeAdded = %v", k.TimeAdded)
	}
	if k.TimeFinished == nil || k.TimeFinished.UTC().Format(time.RFC3339) != "2026-09-02T07:19:46Z" {
		t.Errorf("TimeFinished = %v", k.TimeFinished)
	}
	if k.Deadline == nil {
		t.Error("Deadline not parsed")
	}
	if k.InvoiceMode != "REG_HOURS&SPECIAL" || k.PriceFixed == nil || *k.PriceFixed != 550 {
		t.Errorf("billing = %q/%v", k.InvoiceMode, k.PriceFixed)
	}
	if scan.CaseTotal != 4 || scan.CaseFinished != 1 {
		t.Errorf("case counters = %d/%d, want 4/1", scan.CaseTotal, scan.CaseFinished)
	}
}

func TestListTasks_UnassignedTaskHasNilAssignee(t *testing.T) {
	m := newTaskMock(t)
	m.items = []map[string]any{taskRec(5, "Secret task", nil, false)}
	m.total = intp(1)
	scan, err := m.client().ListTasks(t.Context(), TaskQuery{CaseID: 2})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(scan.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(scan.Tasks))
	}
	if scan.Tasks[0].AssigneeWorkerNr != nil {
		t.Errorf("AssigneeWorkerNr = %v, want nil for an unassigned task", scan.Tasks[0].AssigneeWorkerNr)
	}
}

// search, nameContains, and onlyUnfinished are all honoured upstream and must
// be forwarded rather than reimplemented locally.
func TestListTasks_ServerSideFiltersAreForwarded(t *testing.T) {
	m := newTaskMock(t)
	m.items = nil
	m.total = intp(0)
	_, err := m.client().ListTasks(t.Context(), TaskQuery{
		CaseID: 2, Search: "roof", NameContains: "gutter", OnlyUnfinished: true,
	})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if got := m.lastBody["caseId"]; got != float64(2) {
		t.Errorf("caseId = %v, want 2", got)
	}
	if got := m.lastBody["search"]; got != "roof" {
		t.Errorf("search = %v, want roof", got)
	}
	if got := m.lastBody["nameContains"]; got != "gutter" {
		t.Errorf("nameContains = %v, want gutter", got)
	}
	if got := m.lastBody["onlyUnfinished"]; got != true {
		t.Errorf("onlyUnfinished = %v, want true", got)
	}
}

func TestListTasks_AssigneeFilterIsAppliedClientSide(t *testing.T) {
	m := newTaskMock(t)
	m.items = []map[string]any{
		taskRec(1, "Gutter", 7, false),
		taskRec(2, "Roof", 9, false),
		taskRec(3, "Unassigned", nil, false),
	}
	m.total = intp(3)
	scan, err := m.client().ListTasks(t.Context(), TaskQuery{CaseID: 2, AssigneeWorkerNr: i64p(7)})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(scan.Tasks) != 1 || scan.Tasks[0].ID != 1 {
		t.Fatalf("got %+v, want only task 1", scan.Tasks)
	}
	// Upstream has no assignee parameter, so nothing about it may be sent.
	if _, sent := m.lastBody["respWorkerNr"]; sent {
		t.Error("an assignee parameter was sent upstream; none exists")
	}
}

// The trap: a client-side filter narrows the RESULT, not the READ. Reporting
// the filtered count as coverage would make every assignee query look like a
// truncated page.
func TestListTasks_ClientSideFilterDoesNotMakeTheReadLookIncomplete(t *testing.T) {
	m := newTaskMock(t)
	m.items = []map[string]any{
		taskRec(1, "Gutter", 7, false),
		taskRec(2, "Roof", 9, false),
		taskRec(3, "Wall", 9, false),
	}
	m.total = intp(3)
	scan, err := m.client().ListTasks(t.Context(), TaskQuery{CaseID: 2, AssigneeWorkerNr: i64p(7)})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(scan.Tasks) != 1 {
		t.Fatalf("got %d tasks after filtering, want 1", len(scan.Tasks))
	}
	if scan.Fetched != 3 {
		t.Errorf("Fetched = %d, want 3 -- records RECEIVED, not records surviving the filter", scan.Fetched)
	}
	if !scan.Complete() {
		t.Error("all 3 of 3 records were received; filtering must not report the read as incomplete")
	}
}

func TestListTasks_CappedReadReportsItselfIncomplete(t *testing.T) {
	m := newTaskMock(t)
	m.items = []map[string]any{taskRec(1, "Gutter", 7, false), taskRec(2, "Roof", 9, false)}
	m.total = intp(10)
	scan, err := m.client().ListTasks(t.Context(), TaskQuery{CaseID: 2, PageSize: 2, MaxPages: 2})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if scan.Fetched != 4 {
		t.Fatalf("Fetched = %d, want 4 (2 pages x 2)", scan.Fetched)
	}
	if scan.Complete() {
		t.Error("received 4 of 10; Complete() must be false")
	}
}

func TestListTasks_UndecodableBodyIsADecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 1, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})
		default:
			_, _ = w.Write([]byte(`{"items": "not-an-array"`))
		}
	}))
	t.Cleanup(srv.Close)
	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p",
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond})
	if _, err := c.ListTasks(t.Context(), TaskQuery{CaseID: 2}); !errors.Is(err, ErrDecode) {
		t.Fatalf("err = %v, want ErrDecode", err)
	}
}

func TestListTasks_UnparseableDateFailsTheRead(t *testing.T) {
	m := newTaskMock(t)
	rec := taskRec(1, "Gutter", 7, false)
	rec["deadline"] = "2027-09-02T07:18:29Z"
	m.items = []map[string]any{rec}
	m.total = intp(1)
	_, err := m.client().ListTasks(t.Context(), TaskQuery{CaseID: 2})
	if !errors.Is(err, ErrDecode) {
		t.Fatalf("err = %v, want ErrDecode -- RFC3339 is not the .NET wire format", err)
	}
	if !strings.Contains(err.Error(), "deadline") {
		t.Errorf("error should name the offending field, got: %v", err)
	}
}
