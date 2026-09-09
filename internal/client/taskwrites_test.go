package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type taskWriteMock struct {
	srv    *httptest.Server
	paths  []string
	bodies []map[string]any

	// store is the item served back by the LIST endpoint. It is deliberately
	// separate from the create/update responses, because Kala's three payloads
	// for one item disagree -- and the list is the one the client verifies
	// against.
	store map[int64]map[string]any
	next  int64

	// listDriftMs reproduces the observed upstream behaviour: the list read
	// returns a deadline a couple of milliseconds later than the one written.
	listDriftMs int64

	noReflect bool
	failAt    string

	noIDInResponse bool
	badBody        bool
	ignoreName     bool
	dropDeadline   bool
}

func newTaskWriteMock(t *testing.T) *taskWriteMock {
	t.Helper()
	m := &taskWriteMock{store: map[int64]map[string]any{}, next: 4, listDriftMs: 2}

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

		if m.failAt != "" && strings.Contains(r.URL.Path, m.failAt) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if strings.Contains(r.URL.Path, "GetChecklistItemsPaged") {
			items := make([]any, 0, len(m.store))
			for _, it := range m.store {
				items = append(items, it)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "totalCount": len(items)})
			return
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.bodies = append(m.bodies, body)

		id := m.next
		if v, ok := body["cliId"].(float64); ok {
			id = int64(v)
		} else {
			m.next++
		}

		if !m.noReflect {
			item := m.itemFor(id, body)
			if m.ignoreName {
				item["name"] = "Old"
			}
			if m.dropDeadline {
				delete(item, "deadline")
			}
			m.store[id] = item
		}
		if m.badBody {
			_, _ = w.Write([]byte(`<html>not json</html>`))
			return
		}
		if m.noIDInResponse {
			_, _ = w.Write([]byte(`{"text":"whatever","isFinished":false}`))
			return
		}
		// The real create and update responses have DIFFERENT shapes; both are
		// thinner than the list. Only the id is trusted from either.
		_, _ = w.Write([]byte(`{"text":"whatever","Id":` +
			strconv.FormatInt(id, 10) + `,"isFinished":false}`))
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// itemFor renders a write body as the LIST payload Kala serves back.
//
// The list spells the field `name` -- it is only the WRITE RESPONSES that say
// `text`. Verifying against the list therefore avoids that asymmetry entirely.
// The deadline comes back a couple of milliseconds later than it was written,
// which is the drift the second-precision decision exists for.
func (m *taskWriteMock) itemFor(id int64, in map[string]any) map[string]any {
	item := map[string]any{
		"Id": id, "name": in["name"], "isFinished": false,
		"caseId": 1, "caseNr": in["caseNr"], "checklistId": 11,
		"noteRequired": in["noteRequired"], "imageRequired": in["imageRequired"],
		"description": in["description"], "invoiceMode": in["invoiceMode"],
		"priceFixed": in["priceFixed"],
	}
	if s, ok := in["deadline"].(string); ok && s != "" {
		if ts, err := time.Parse(time.RFC3339, s); err == nil {
			ms := ts.UnixMilli() + m.listDriftMs
			item["deadline"] = "/Date(" + strconv.FormatInt(ms, 10) + ")/"
		}
	}
	return item
}

func (m *taskWriteMock) client() InternalClient {
	return NewInternal(InternalConfig{
		Endpoint: m.srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond,
	})
}

func taskInput() TaskInput {
	d := time.Date(2026, 9, 30, 15, 11, 32, 0, time.UTC)
	price := 500
	return TaskInput{
		CaseNumber: "KA-1", CaseID: 1, Name: "Mount gutter", NoteRequired: true,
		Deadline: &d, InvoiceMode: "REG_HOURS&STANDARD", PriceFixed: &price,
	}
}

func (m *taskWriteMock) writeBody(t *testing.T, contains string) map[string]any {
	t.Helper()
	for i, p := range m.paths {
		if strings.Contains(p, contains) && i < len(m.bodies) {
			return m.bodies[i]
		}
	}
	for _, b := range m.bodies {
		return b
	}
	t.Fatalf("no %s request was made; paths = %v", contains, m.paths)
	return nil
}

func TestCreateTask_ReturnsAllocatedIdentity(t *testing.T) {
	m := newTaskWriteMock(t)
	got, err := m.client().CreateTask(context.Background(), taskInput())
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if got.ID != 4 {
		t.Errorf("ID = %d, want the allocated 4", got.ID)
	}
	if got.Name != "Mount gutter" {
		t.Errorf("Name = %q; the field is `name` on write and `text` on read, and the "+
			"mapping must absorb that", got.Name)
	}
}

// Create carries no cliId and no description -- the endpoint accepts neither.
func TestCreateTask_OmitsUpdateOnlyFields(t *testing.T) {
	m := newTaskWriteMock(t)
	if _, err := m.client().CreateTask(context.Background(), taskInput()); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	body := m.writeBody(t, "AddChecklistItem")
	for _, key := range []string{"cliId", "description", "normTime"} {
		if _, present := body[key]; present {
			t.Errorf("create body sent %q, which AddChecklistItem does not accept", key)
		}
	}
	if body["caseNr"] != "KA-1" {
		t.Errorf("caseNr = %v; creates key the case by STRING number", body["caseNr"])
	}
}

// UpdateChecklistItem is a full-record replace keyed on BOTH identifiers.
func TestUpdateTask_SendsEveryFieldAndBothIdentifiers(t *testing.T) {
	m := newTaskWriteMock(t)
	m.store[4] = m.itemFor(4, map[string]any{"name": "Old", "caseNr": "KA-1"})

	if _, err := m.client().UpdateTask(context.Background(), 4, taskInput()); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	body := m.writeBody(t, "UpdateChecklistItem")

	if body["cliId"] == nil || body["caseNr"] != "KA-1" {
		t.Errorf("update must carry BOTH cliId (int) and caseNr (string): %v", body)
	}
	for _, key := range []string{
		"name", "deadline", "noteRequired", "imageRequired", "description",
		"normTime", "invoiceMode", "priceFixed",
	} {
		if _, present := body[key]; !present {
			t.Errorf("update omits %q -- a full-record replace would blank it upstream", key)
		}
	}
}

// Kala's list read returns a deadline a couple of milliseconds later
// than the one written, so millisecond precision could never converge. The
// client truncates to the second on the way out and on the way back.
func TestTaskDeadline_SecondPrecisionSurvivesUpstreamDrift(t *testing.T) {
	m := newTaskWriteMock(t)
	m.listDriftMs = 2

	got, err := m.client().CreateTask(context.Background(), taskInput())
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if got.Deadline == nil {
		t.Fatal("deadline was not read back")
	}
	want := time.Date(2026, 9, 30, 15, 11, 32, 0, time.UTC)
	if !got.Deadline.UTC().Equal(want) {
		t.Errorf("deadline = %s, want %s truncated to the second so the plan converges",
			got.Deadline.UTC(), want)
	}

	body := m.writeBody(t, "AddChecklistItem")
	if s, ok := body["deadline"].(string); ok && strings.Contains(s, ".") {
		t.Errorf("deadline was sent with sub-second precision (%s), which cannot round-trip", s)
	}
}

func TestCreateTask_UnverifiedWriteFails(t *testing.T) {
	m := newTaskWriteMock(t)
	m.noReflect = true
	if _, err := m.client().CreateTask(context.Background(), taskInput()); err == nil {
		t.Fatal("a create that did not persist must fail read-back verification")
	}
}

func TestUpdateTask_UnverifiedWriteFails(t *testing.T) {
	m := newTaskWriteMock(t)
	m.store[4] = m.itemFor(4, map[string]any{"name": "Old", "caseNr": "KA-1"})
	m.noReflect = true
	if _, err := m.client().UpdateTask(context.Background(), 4, taskInput()); err == nil {
		t.Fatal("an update that did not persist must fail read-back verification")
	}
}

func TestCreateTask_HTTPFailurePropagates(t *testing.T) {
	m := newTaskWriteMock(t)
	m.failAt = "AddChecklistItem"
	if _, err := m.client().CreateTask(context.Background(), taskInput()); err == nil {
		t.Fatal("an HTTP failure on create must propagate")
	}
}

// AddChecklistItem does not accept a description, but UpdateChecklistItem
// writes one. A task configured with a description therefore takes TWO calls,
// and the create must be followed automatically rather than silently dropping
// the value.
func TestCreateTask_WithDescriptionFollowsUpWithAnUpdate(t *testing.T) {
	m := newTaskWriteMock(t)
	in := taskInput()
	in.Description = "Check the flashing too"

	got, err := m.client().CreateTask(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if got.Description != "Check the flashing too" {
		t.Errorf("Description = %q; create cannot set it, so an update must follow", got.Description)
	}

	var sawCreate, sawUpdate bool
	for _, p := range m.paths {
		sawCreate = sawCreate || strings.Contains(p, "AddChecklistItem")
		sawUpdate = sawUpdate || strings.Contains(p, "UpdateChecklistItem")
	}
	if !sawCreate || !sawUpdate {
		t.Errorf("want both a create and a follow-up update; paths = %v", m.paths)
	}
}

// A task with no description takes ONE call. The follow-up exists to carry a
// value, not as an unconditional second write.
func TestCreateTask_WithoutDescriptionMakesNoSecondWrite(t *testing.T) {
	m := newTaskWriteMock(t)
	if _, err := m.client().CreateTask(context.Background(), taskInput()); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	for _, p := range m.paths {
		if strings.Contains(p, "UpdateChecklistItem") {
			t.Errorf("an unnecessary second write was made: %v", m.paths)
		}
	}
}

// A deadline the caller did not set is sent as null, not as a zero time --
// which would otherwise write 0001-01-01 upstream.
func TestCreateTask_NoDeadlineSendsNull(t *testing.T) {
	m := newTaskWriteMock(t)
	in := taskInput()
	in.Deadline = nil

	if _, err := m.client().CreateTask(context.Background(), in); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	body := m.writeBody(t, "AddChecklistItem")
	if v, ok := body["deadline"]; !ok || v != nil {
		t.Errorf("deadline = %v (present=%t), want an explicit null", v, ok)
	}
}

// The verification read failing is not the same as the write failing, and the
// id must survive either way.
func TestCreateTask_ReadBackFailureReturnsTheID(t *testing.T) {
	m := newTaskWriteMock(t)
	m.failAt = "GetChecklistItemsPaged"

	got, err := m.client().CreateTask(context.Background(), taskInput())
	if err == nil {
		t.Fatal("want an error when the item cannot be read back")
	}
	if got.ID != 4 {
		t.Errorf("ID = %d; the item exists and Kala cannot delete it, so its id must survive", got.ID)
	}
}

// A write that reports success without an id leaves an item that cannot be
// addressed.
func TestCreateTask_SuccessWithoutIDIsAnError(t *testing.T) {
	m := newTaskWriteMock(t)
	m.noIDInResponse = true
	if _, err := m.client().CreateTask(context.Background(), taskInput()); err == nil {
		t.Fatal("success without an item id must be an error")
	}
}

func TestCreateTask_UndecodableResponseIsADecodeError(t *testing.T) {
	m := newTaskWriteMock(t)
	m.badBody = true
	if _, err := m.client().CreateTask(context.Background(), taskInput()); err == nil {
		t.Fatal("an undecodable write response must be an error")
	}
}

// A name that came back different means the write did not land.
func TestUpdateTask_NameMismatchFailsVerification(t *testing.T) {
	m := newTaskWriteMock(t)
	m.store[4] = m.itemFor(4, map[string]any{"name": "Old", "caseNr": "KA-1"})
	m.ignoreName = true

	if _, err := m.client().UpdateTask(context.Background(), 4, taskInput()); err == nil {
		t.Fatal("a name that did not change must fail read-back verification")
	}
}

// A deadline that vanished upstream is a failed write, not a null result.
func TestCreateTask_MissingDeadlineFailsVerification(t *testing.T) {
	m := newTaskWriteMock(t)
	m.dropDeadline = true
	if _, err := m.client().CreateTask(context.Background(), taskInput()); err == nil {
		t.Fatal("a deadline that did not persist must fail verification")
	}
}
