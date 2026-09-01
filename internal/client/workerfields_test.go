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

// fieldMock records the path and body of each write, and serves a WorkerInfo
// that reflects them so read-back verification succeeds.
type fieldMock struct {
	srv      *httptest.Server
	paths    []string
	bodies   []map[string]any
	info     map[string]any
	rejectAt string
}

func newFieldMock(t *testing.T) *fieldMock {
	t.Helper()
	m := &fieldMock{info: map[string]any{"workerNr": 3, "workerId": 3}}

	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":17221}]}`))
			return
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
			return
		case strings.Contains(r.URL.Path, "WorkerInfo"):
			_ = json.NewEncoder(w).Encode(m.info)
			return
		}

		if m.rejectAt != "" && strings.Contains(r.URL.Path, m.rejectAt) {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		m.paths = append(m.paths, r.URL.Path)
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		m.bodies = append(m.bodies, parsed)

		// Reflect the write into the WorkerInfo the mock serves back.
		for k, v := range parsed {
			switch k {
			case "phone", "title", "initials", "licensePlate", "department", "leaderNote",
				"isLeader", "isFinance", "isPlanner":
				m.info[k] = v
			case "newDateOfEmployment":
				if s, ok := v.(string); ok {
					m.info["dateOfEmployment"] = strings.SplitN(s, "T", 2)[0]
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *fieldMock) client() InternalClient {
	return NewInternal(InternalConfig{
		Endpoint: m.srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond,
	})
}

// The identifier key and trailing slash differ per endpoint. Getting either
// wrong fails silently upstream, so both are pinned here.
func TestSetWorkerField_UsesCorrectPathAndIdentifierKey(t *testing.T) {
	cases := []struct {
		field    WorkerField
		wantPath string
		wantID   string
		valueKey string
	}{
		{FieldPhone, "/api/SetPhone/", "workerNr", "phone"},
		{FieldTitle, "/api/SetTitle/", "workerNr", "title"},
		{FieldInitials, "/api/SetWorkerInitials/", "workerNr", "initials"},
		{FieldLicensePlate, "/api/SetLicensePlate/", "workerNr", "licensePlate"},
		// No trailing slash, and workerID rather than workerNr.
		{FieldDepartment, "/api/ChangeWorkerDepartment", "workerID", "department"},
		{FieldLeaderNote, "/api/ChangeLeaderNote", "workerID", "leaderNote"},
	}

	for _, tc := range cases {
		t.Run(string(tc.field), func(t *testing.T) {
			m := newFieldMock(t)
			if err := m.client().SetWorkerField(context.Background(), 3, tc.field, "value"); err != nil {
				t.Fatalf("SetWorkerField: %v", err)
			}
			if len(m.paths) != 1 {
				t.Fatalf("want one request, got %v", m.paths)
			}
			if m.paths[0] != tc.wantPath {
				t.Errorf("path = %q, want %q (trailing slash is significant)", m.paths[0], tc.wantPath)
			}
			if _, ok := m.bodies[0][tc.wantID]; !ok {
				t.Errorf("body %v is missing the identifier key %q", m.bodies[0], tc.wantID)
			}
			if got := m.bodies[0][tc.valueKey]; got != "value" {
				t.Errorf("%s = %v, want \"value\"", tc.valueKey, got)
			}
		})
	}
}

func TestSetWorkerRole_UsesCorrectPathAndKey(t *testing.T) {
	cases := []struct {
		role     WorkerRole
		wantPath string
		valueKey string
	}{
		{RoleLeader, "/api/SetLeaderRole/", "isLeader"},
		{RoleFinance, "/api/SetFinanceRole/", "isFinance"},
		{RolePlanner, "/api/SetPlannerRole/", "isPlanner"},
	}

	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			m := newFieldMock(t)
			if err := m.client().SetWorkerRole(context.Background(), 3, tc.role, true); err != nil {
				t.Fatalf("SetWorkerRole: %v", err)
			}
			if m.paths[0] != tc.wantPath {
				t.Errorf("path = %q, want %q", m.paths[0], tc.wantPath)
			}
			if got := m.bodies[0][tc.valueKey]; got != true {
				t.Errorf("%s = %v, want true", tc.valueKey, got)
			}
		})
	}
}

func TestSetWorkerField_UnknownFieldIsRejected(t *testing.T) {
	m := newFieldMock(t)
	if err := m.client().SetWorkerField(context.Background(), 3, WorkerField("nope"), "v"); err == nil {
		t.Error("want an error for an unknown field")
	}
	if len(m.paths) != 0 {
		t.Error("no request should be made")
	}
}

func TestSetWorkerRole_UnknownRoleIsRejected(t *testing.T) {
	m := newFieldMock(t)
	if err := m.client().SetWorkerRole(context.Background(), 3, WorkerRole("nope"), true); err == nil {
		t.Error("want an error for an unknown role")
	}
}

// ARCH1.8 again: a write that does not take effect must fail.
func TestSetWorkerField_UnverifiedWriteFails(t *testing.T) {
	m := newFieldMock(t)
	// Serve a WorkerInfo that never reflects the write.
	m.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
		case strings.Contains(r.URL.Path, "WorkerInfo"):
			_, _ = w.Write([]byte(`{"workerNr":3,"phone":"unchanged"}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	err := m.client().SetWorkerField(context.Background(), 3, FieldPhone, "new")
	if err == nil {
		t.Fatal("an unverified write must fail")
	}
	if !strings.Contains(err.Error(), "reads back as") {
		t.Errorf("error should report the mismatch, got %q", err.Error())
	}
}

func TestSetWorkerField_HTTPFailurePropagates(t *testing.T) {
	m := newFieldMock(t)
	m.rejectAt = "SetPhone"

	if err := m.client().SetWorkerField(context.Background(), 3, FieldPhone, "x"); err == nil {
		t.Error("want the HTTP failure to surface")
	}
}

// --- date of employment ---------------------------------------------------

func TestSetWorkerDateOfEmployment_SendsMidnightUTCAtOffsetZero(t *testing.T) {
	m := newFieldMock(t)

	if err := m.client().SetWorkerDateOfEmployment(context.Background(), 3, "2026-03-15"); err != nil {
		t.Fatalf("SetWorkerDateOfEmployment: %v", err)
	}

	body := m.bodies[0]
	if got := body["newDateOfEmployment"]; got != "2026-03-15T00:00:00.000Z" {
		t.Errorf("newDateOfEmployment = %v, want midnight UTC", got)
	}
	// The offset is what shifts the stored date across midnight; it must be 0.
	if got := body["gmtOffset"]; got != float64(0) {
		t.Errorf("gmtOffset = %v, want 0 — a non-zero offset can move the stored date a day", got)
	}
	if _, ok := body["workerID"]; !ok {
		t.Errorf("body %v must use workerID, not workerNr", body)
	}
}

func TestSetWorkerDateOfEmployment_RejectsNonISODates(t *testing.T) {
	m := newFieldMock(t)
	c := m.client()

	for _, bad := range []string{"", "15-03-2026", "2026-3-15", "2026-13-01", "2026-02-30", "2026-03-15T00:00:00Z"} {
		if err := c.SetWorkerDateOfEmployment(context.Background(), 3, bad); err == nil {
			t.Errorf("date %q should be rejected", bad)
		}
	}
	if len(m.paths) != 0 {
		t.Error("no request should be made for an invalid date")
	}
}

func TestIsISODate(t *testing.T) {
	for _, ok := range []string{"2026-01-01", "2024-02-29"} {
		if !isISODate(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"2023-02-29", "2026-1-1", "", "not-a-date"} {
		if isISODate(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestSetWorkerRole_UnverifiedWriteFails(t *testing.T) {
	m := newFieldMock(t)
	m.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
		case strings.Contains(r.URL.Path, "WorkerInfo"):
			_, _ = w.Write([]byte(`{"workerNr":3,"isLeader":false}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	err := m.client().SetWorkerRole(context.Background(), 3, RoleLeader, true)
	if err == nil {
		t.Fatal("an unverified role write must fail")
	}
	if !strings.Contains(err.Error(), "reads back as") {
		t.Errorf("error should report the mismatch, got %q", err.Error())
	}
}

func TestSetWorkerDateOfEmployment_UnverifiedWriteFails(t *testing.T) {
	m := newFieldMock(t)
	m.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
		case strings.Contains(r.URL.Path, "WorkerInfo"):
			// The offset hazard in miniature: stored a day off what was sent.
			_, _ = w.Write([]byte(`{"workerNr":3,"dateOfEmployment":"2026-03-14"}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	err := m.client().SetWorkerDateOfEmployment(context.Background(), 3, "2026-03-15")
	if err == nil {
		t.Fatal("a date stored a day off must fail rather than cause a perpetual diff")
	}
	if !strings.Contains(err.Error(), "2026-03-14") {
		t.Errorf("error should show what was actually stored, got %q", err.Error())
	}
}

func TestSetWorkerRole_HTTPFailurePropagates(t *testing.T) {
	m := newFieldMock(t)
	m.rejectAt = "SetLeaderRole"

	if err := m.client().SetWorkerRole(context.Background(), 3, RoleLeader, true); err == nil {
		t.Error("want the HTTP failure to surface")
	}
}

func TestSetWorkerDateOfEmployment_HTTPFailurePropagates(t *testing.T) {
	m := newFieldMock(t)
	m.rejectAt = "ChangeDateOfEmployment"

	if err := m.client().SetWorkerDateOfEmployment(context.Background(), 3, "2026-03-15"); err == nil {
		t.Error("want the HTTP failure to surface")
	}
}

func TestFieldWrites_RequireCredentials(t *testing.T) {
	c := NewInternal(InternalConfig{Endpoint: "https://example.test"})
	ctx := context.Background()

	if err := c.SetWorkerField(ctx, 1, FieldPhone, "x"); err == nil {
		t.Error("SetWorkerField wants credentials")
	}
	if err := c.SetWorkerRole(ctx, 1, RoleLeader, true); err == nil {
		t.Error("SetWorkerRole wants credentials")
	}
	if err := c.SetWorkerDateOfEmployment(ctx, 1, "2026-01-01"); err == nil {
		t.Error("SetWorkerDateOfEmployment wants credentials")
	}
}

func TestPostJSON_MarshalFailureIsReported(t *testing.T) {
	m := newFieldMock(t)
	c := m.client().(*internalAPI)

	// A channel cannot be marshalled to JSON.
	err := c.postJSON(context.Background(), "/api/Whatever", map[string]any{"bad": make(chan int)})
	if err == nil {
		t.Fatal("want a marshal error")
	}
	if !strings.Contains(err.Error(), "building request") {
		t.Errorf("error should name the stage, got %q", err.Error())
	}
}
