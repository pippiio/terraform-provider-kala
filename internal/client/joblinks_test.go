package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type jobLinkMock struct {
	srv     *httptest.Server
	paths   []string
	bodies  []map[string]any
	queries []url.Values

	// assigned maps item id -> worker numbers, which is how the read path
	// actually sees assignment: through the task list, not a link endpoint.
	assigned map[int64][]int64
	linkID   int64
	truncate bool
	failAt   string
	noLinkID bool
}

func newJobLinkMock(t *testing.T) *jobLinkMock {
	t.Helper()
	m := &jobLinkMock{assigned: map[int64][]int64{}, linkID: 4}

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

		if strings.Contains(r.URL.Path, "GetChecklistItemsPaged") {
			// Honour the page parameter. A mock that serves the same page
			// forever lets the client paginate its way to "complete" against
			// an endpoint that would have run out -- which is how the
			// truncation guard silently passed here the first time.
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			page, _ := req["page"].(float64)

			items := []any{}
			for _, id := range []int64{1, 2, 3} {
				workers := []any{}
				for _, nr := range m.assigned[id] {
					// The real payload carries name/phone/title too; only the
					// identifier is mapped, so only it is asserted on.
					workers = append(workers, map[string]any{"workerNr": nr, "name": "Someone"})
				}
				items = append(items, map[string]any{
					"Id": id, "name": "Item", "caseId": 4, "caseNr": "KA-4",
					"workersAssigned": workers,
				})
			}
			total := len(items)
			if m.truncate {
				total = 99
			}
			if page > 0 {
				items = []any{} // exhausted
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "totalCount": total})
			return
		}

		if strings.Contains(r.URL.Path, "RemoveJoblinkChecklistItem") {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.bodies = append(m.bodies, body)

		switch {
		case strings.Contains(r.URL.Path, "NewJobLinkNoTimeCaseId"):
			if m.noLinkID {
				_, _ = w.Write([]byte(`{"caseNr":"KA-4","checklistIds":[]}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": m.linkID, "caseNr": "KA-4", "caseId": 4,
				"workerNr": body["workerNr"], "checklistIds": []int64{},
			})
		case strings.Contains(r.URL.Path, "UpdateJobLinkChecklist"):
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *jobLinkMock) client() InternalClient {
	return NewInternal(InternalConfig{
		Endpoint: m.srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond,
	})
}

// The response spells the identifier `id`; the request that consumes it spells
// it `jobLinkId`. One value, two names.
func TestEnsureJobLink_ReturnsTheLinkAndItsID(t *testing.T) {
	m := newJobLinkMock(t)
	link, err := m.client().EnsureJobLink(context.Background(), 4, 1)
	if err != nil {
		t.Fatalf("EnsureJobLink: %v", err)
	}
	if link.ID != 4 {
		t.Errorf("ID = %d, want 4 -- taken from the response's `id`", link.ID)
	}
	if link.CaseNumber != "KA-4" || link.WorkerNr != 1 {
		t.Errorf("link = %+v", link)
	}
	if !strings.HasSuffix(m.paths[0], "/api/NewJobLinkNoTimeCaseId/") {
		t.Errorf("path = %q", m.paths[0])
	}
}

// A link without an id cannot have its items set, so it is a failure rather
// than a link that quietly covers nothing.
func TestEnsureJobLink_MissingIDIsAnError(t *testing.T) {
	m := newJobLinkMock(t)
	m.noLinkID = true
	if _, err := m.client().EnsureJobLink(context.Background(), 4, 1); err == nil {
		t.Fatal("a job link with no id must be an error")
	}
}

func TestSetJobLinkChecklist_SendsTheWholeSet(t *testing.T) {
	m := newJobLinkMock(t)
	if err := m.client().SetJobLinkChecklist(context.Background(), 4, []int64{1, 3}); err != nil {
		t.Fatalf("SetJobLinkChecklist: %v", err)
	}
	body := m.bodies[len(m.bodies)-1]
	if body["jobLinkId"] == nil {
		t.Errorf("body omits jobLinkId: %v", body)
	}
	ids, ok := body["checklistIds"].([]any)
	if !ok || len(ids) != 2 {
		t.Errorf("checklistIds = %v, want the whole set [1 3]", body["checklistIds"])
	}
}

// An empty set must travel as [], not null. Null is not "cover nothing"; it is
// the absence of an instruction, and the two are different requests.
func TestSetJobLinkChecklist_EmptySetIsAnArrayNotNull(t *testing.T) {
	m := newJobLinkMock(t)
	if err := m.client().SetJobLinkChecklist(context.Background(), 4, nil); err != nil {
		t.Fatalf("SetJobLinkChecklist: %v", err)
	}
	body := m.bodies[len(m.bodies)-1]
	if body["checklistIds"] == nil {
		t.Error("an empty set was sent as null; it must be an empty array")
	}
}

// Detaching needs no jobLinkId, which is what makes destroy work for a
// resource imported without one.
func TestRemoveJobLinkChecklistItem_KeysOnCaseItemAndWorker(t *testing.T) {
	m := newJobLinkMock(t)
	if err := m.client().RemoveJobLinkChecklistItem(context.Background(), "KA-4", 3, 1); err != nil {
		t.Fatalf("RemoveJobLinkChecklistItem: %v", err)
	}
	var q url.Values
	for i, p := range m.paths {
		if strings.Contains(p, "RemoveJoblinkChecklistItem") {
			q = m.queries[i]
		}
	}
	if q.Get("caseNr") != "KA-4" || q.Get("checklistItemId") != "3" || q.Get("workerNr") != "1" {
		t.Errorf("query = %v", q)
	}
	for _, b := range m.bodies {
		if b["jobLinkId"] != nil {
			t.Error("detaching must not require a jobLinkId")
		}
	}
}

// The read path is the TASK LIST. Kala exposes no way to read a job link.
func TestAssignedTaskIDs_ReadsFromTheTaskList(t *testing.T) {
	m := newJobLinkMock(t)
	m.assigned[1] = []int64{1, 3}
	m.assigned[3] = []int64{1}

	ids, err := m.client().AssignedTaskIDs(context.Background(), 4, 1)
	if err != nil {
		t.Fatalf("AssignedTaskIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v, want items 1 and 3", ids)
	}

	other, err := m.client().AssignedTaskIDs(context.Background(), 4, 3)
	if err != nil {
		t.Fatalf("AssignedTaskIDs: %v", err)
	}
	if len(other) != 1 || other[0] != 1 {
		t.Errorf("worker 3 ids = %v, want just item 1", other)
	}
}

// A partial read cannot establish a SET. Reporting a subset as the whole would
// make the next apply delete assignments it simply never saw.
func TestAssignedTaskIDs_PartialReadIsAnError(t *testing.T) {
	m := newJobLinkMock(t)
	m.truncate = true
	if _, err := m.client().AssignedTaskIDs(context.Background(), 4, 1); err == nil {
		t.Fatal("a truncated read cannot establish which items a worker covers")
	}
}

func TestJobLink_FailuresPropagate(t *testing.T) {
	for name, call := range map[string]func(InternalClient) error{
		"EnsureJobLink": func(c InternalClient) error {
			_, err := c.EnsureJobLink(context.Background(), 4, 1)
			return err
		},
		"SetJobLinkChecklist": func(c InternalClient) error {
			return c.SetJobLinkChecklist(context.Background(), 4, []int64{1})
		},
		"RemoveJobLinkChecklistItem": func(c InternalClient) error {
			return c.RemoveJobLinkChecklistItem(context.Background(), "KA-4", 1, 1)
		},
		"AssignedTaskIDs": func(c InternalClient) error {
			_, err := c.AssignedTaskIDs(context.Background(), 4, 1)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := newJobLinkMock(t)
			m.failAt = "api/"
			if strings.Contains(name, "Assigned") {
				m.failAt = "GetChecklistItemsPaged"
			}
			if err := call(m.client()); err == nil {
				t.Errorf("%s must propagate an HTTP failure", name)
			}
		})
	}
}

func TestEnsureJobLink_UndecodableResponseIsADecodeError(t *testing.T) {
	m := newJobLinkMock(t)
	m.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":4242}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"t"}`))
		default:
			_, _ = w.Write([]byte(`<html>not json</html>`))
		}
	})
	if _, err := m.client().EnsureJobLink(context.Background(), 4, 1); err == nil {
		t.Fatal("an undecodable job link response must be an error")
	}
}

// An unknown caseId answers HTTP 200 with items:[] and NO totalCount, while a
// real but empty case answers totalCount:0. Conflating them would report a
// case that does not exist as one with no work on it -- and, for assignment,
// would report "this worker covers nothing" instead of "there is no case".
func TestAssignedTaskIDs_UnknownCaseIsNotFound(t *testing.T) {
	m := newJobLinkMock(t)
	m.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":4242}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"t"}`))
		default:
			// No totalCount at all -- the unknown-case shape.
			_, _ = w.Write([]byte(`{"items":[]}`))
		}
	})

	_, err := m.client().AssignedTaskIDs(context.Background(), 999, 1)
	if err == nil {
		t.Fatal("an unknown case must be an error, not an empty assignment set")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
