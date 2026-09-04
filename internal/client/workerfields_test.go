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

// fieldMock records the path and body of each write, and serves a WorkerInfo
// that reflects them so read-back verification succeeds.
type fieldMock struct {
	srv      *httptest.Server
	paths    []string
	bodies   []map[string]any
	info     map[string]any
	rejectAt string

	// noReflect accepts writes with HTTP 200 but does not apply them to the
	// WorkerInfo served back — the "reported success, changed nothing" shape
	// that read-back verification exists to catch.
	noReflect bool
}

func newFieldMock(t *testing.T) *fieldMock {
	t.Helper()
	m := &fieldMock{info: map[string]any{"workerNr": 3, "workerId": 3}}

	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":4242}]}`))
			return
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
			return
		}

		// Checked before the WorkerInfo branch so a test can refuse the
		// verification read as well as the write.
		if m.rejectAt != "" && strings.Contains(r.URL.Path, m.rejectAt) {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		if strings.Contains(r.URL.Path, "WorkerInfo") {
			_ = json.NewEncoder(w).Encode(m.info)
			return
		}

		m.paths = append(m.paths, r.URL.Path)
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		m.bodies = append(m.bodies, parsed)

		// Reflect the write into the WorkerInfo the mock serves back.
		if m.noReflect {
			w.WriteHeader(http.StatusOK)
			return
		}
		for k, v := range parsed {
			switch k {
			case "phone", "title", "initials", "licensePlate", "department", "leaderNote",
				"isLeader", "isFinance", "isPlanner":
				m.info[k] = v
			case "newName":
				m.info["name"] = v
			case "bossNr":
				// Reflected as the nested object Kala actually returns, so the
				// read-back path is exercised rather than assumed.
				m.info["firstBoss"] = map[string]any{"name": "The Boss", "workerNr": v}
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

// --- welcome email --------------------------------------------------------

func TestSendWelcomeEmail_UsesFormEncodingNotJSON(t *testing.T) {
	var gotContentType, gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":4242}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
		default:
			gotContentType = r.Header.Get("Content-Type")
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			gotBody = string(body)
			if r.Header.Get("kauthtoken") == "" {
				t.Error("welcome email requires kauthtoken")
			}
			_, _ = w.Write([]byte(`{"status":true}`))
		}
	}))
	defer srv.Close()

	c := NewInternal(InternalConfig{
		Endpoint: srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond,
	})
	if err := c.SendWelcomeEmail(context.Background(), "frodo@example.com"); err != nil {
		t.Fatalf("SendWelcomeEmail: %v", err)
	}

	// This endpoint is the odd one out: form-encoded, not JSON.
	if !strings.HasPrefix(gotContentType, "application/x-www-form-urlencoded") {
		t.Errorf("Content-Type = %q, want form encoding", gotContentType)
	}
	if gotBody != "email=frodo%40example.com" {
		t.Errorf("body = %q, want the URL-encoded email", gotBody)
	}
}

// The other JSON endpoints must keep their JSON Content-Type.
func TestOtherEndpointsStillUseJSON(t *testing.T) {
	var gotContentType string
	m := newFieldMock(t)
	m.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
		case strings.Contains(r.URL.Path, "WorkerInfo"):
			_, _ = w.Write([]byte(`{"workerNr":3,"phone":"x"}`))
		default:
			gotContentType = r.Header.Get("Content-Type")
			w.WriteHeader(http.StatusOK)
		}
	})

	_ = m.client().SetWorkerField(context.Background(), 3, FieldPhone, "x")
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", gotContentType)
	}
}

// Sending mail leaves nothing to read back, so the endpoint's own status field
// is the only confirmation there is — a false must not pass silently.
func TestSendWelcomeEmail_StatusFalseIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
		default:
			_, _ = w.Write([]byte(`{"status":false}`))
		}
	}))
	defer srv.Close()

	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond})
	err := c.SendWelcomeEmail(context.Background(), "a@b.c")
	if err == nil {
		t.Fatal("status false must be an error")
	}
	if !strings.Contains(err.Error(), "not sent") {
		t.Errorf("error should say the mail was not sent, got %q", err.Error())
	}
}

func TestSendWelcomeEmail_RejectsEmptyAddress(t *testing.T) {
	m := newFieldMock(t)
	if err := m.client().SendWelcomeEmail(context.Background(), "  "); err == nil {
		t.Error("want a validation error before any request")
	}
	if len(m.paths) != 0 {
		t.Error("no request should be made")
	}
}

func TestSendWelcomeEmail_HTTPFailurePropagates(t *testing.T) {
	m := newFieldMock(t)
	m.rejectAt = "SendWorkerWelcomeEmail"

	if err := m.client().SendWelcomeEmail(context.Background(), "a@b.c"); err == nil {
		t.Error("want the HTTP failure to surface")
	}
}

func TestSendWelcomeEmail_MalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
		default:
			_, _ = w.Write([]byte(`{broken`))
		}
	}))
	defer srv.Close()

	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond})
	if err := c.SendWelcomeEmail(context.Background(), "a@b.c"); !errors.Is(err, ErrDecode) {
		t.Errorf("want ErrDecode, got %v", err)
	}
}

func TestSendWelcomeEmail_NeedsCredentials(t *testing.T) {
	c := NewInternal(InternalConfig{Endpoint: "https://example.test"})
	if err := c.SendWelcomeEmail(context.Background(), "a@b.c"); err == nil {
		t.Error("want a credentials error")
	}
}

// --- name and boss --------------------------------------------------------

// ChangeWorkerName breaks BOTH Change* patterns: it keeps the trailing slash
// and spells the identifier "workerId". Probing found Kala accepts every
// variant, but what is sent here is what Kala's own web client sends — the only
// variant it is safe to assume will keep working. Pinned so a tidy-up cannot
// quietly "correct" it into the general rule.
func TestSetWorkerName_UsesTheObservedPathAndIdentifierKey(t *testing.T) {
	m := newFieldMock(t)

	if err := m.client().SetWorkerField(context.Background(), 3, FieldName, "New Name"); err != nil {
		t.Fatalf("SetWorkerField(name): %v", err)
	}

	if len(m.paths) != 1 || m.paths[0] != "/api/ChangeWorkerName/" {
		t.Errorf("paths = %v, want /api/ChangeWorkerName/ WITH the trailing slash", m.paths)
	}
	if _, ok := m.bodies[0]["workerId"]; !ok {
		t.Errorf("body = %v, want the identifier under \"workerId\" — not workerID, not workerNr",
			m.bodies[0])
	}
	if m.bodies[0]["newName"] != "New Name" {
		t.Errorf("body = %v, want newName carrying the value", m.bodies[0])
	}
}

// ARCH1.8: a rename is only real if it reads back. This is the exact shape
// SetEmailNew was caught in — HTTP 200, nothing changed.
func TestSetWorkerName_FailsWhenUnverified(t *testing.T) {
	m := newFieldMock(t)
	m.info["name"] = "Old Name"
	m.noReflect = true

	err := m.client().SetWorkerField(context.Background(), 3, FieldName, "New Name")
	if err == nil {
		t.Fatal("an unverified rename must fail")
	}
	if !strings.Contains(err.Error(), "reads back as") {
		t.Errorf("error should report the mismatch, got %q", err.Error())
	}
}

// ChangeBoss is a third spelling: "workerNr", where the other Change* endpoints
// use "workerID". It also reads back as a nested object rather than a scalar.
func TestSetWorkerBoss_SendsWorkerNrAndVerifiesTheNestedReadBack(t *testing.T) {
	m := newFieldMock(t)

	if err := m.client().SetWorkerBoss(context.Background(), 3, 4); err != nil {
		t.Fatalf("SetWorkerBoss: %v", err)
	}

	if len(m.paths) != 1 || m.paths[0] != "/api/ChangeBoss/" {
		t.Errorf("paths = %v, want /api/ChangeBoss/", m.paths)
	}
	if _, ok := m.bodies[0]["workerNr"]; !ok {
		t.Errorf("body = %v, want the identifier under \"workerNr\"", m.bodies[0])
	}
	if m.bodies[0]["bossNr"] != float64(4) {
		t.Errorf("body = %v, want bossNr = 4", m.bodies[0])
	}
}

func TestSetWorkerBoss_FailsWhenUnverified(t *testing.T) {
	m := newFieldMock(t)
	m.info["firstBoss"] = map[string]any{"name": "Someone Else", "workerNr": 9}
	m.noReflect = true

	err := m.client().SetWorkerBoss(context.Background(), 3, 4)
	if err == nil {
		t.Fatal("an unverified boss change must fail")
	}
	if !strings.Contains(err.Error(), "reads back as 9") {
		t.Errorf("error should name what it actually read, got %q", err.Error())
	}
}

// An employee reporting to themselves is refused locally. The read-back would
// report success, so the guard cannot rely on it.
func TestSetWorkerBoss_RefusesSelfReference(t *testing.T) {
	m := newFieldMock(t)

	if err := m.client().SetWorkerBoss(context.Background(), 3, 3); err == nil {
		t.Fatal("an employee must not be able to be their own boss")
	}
	if len(m.paths) != 0 {
		t.Errorf("the guard must reject before calling Kala, got requests to %v", m.paths)
	}
}

// A worker with no boss has no firstBoss object at all. Zero means none, not
// "employee 0", which cannot exist.
func TestWorkerInfo_WithoutABossDecodesToZero(t *testing.T) {
	m := newFieldMock(t)

	info, err := m.client().GetWorkerInfo(context.Background(), 3)
	if err != nil {
		t.Fatalf("GetWorkerInfo: %v", err)
	}
	if info.BossNumber != 0 || info.BossName != "" {
		t.Errorf("BossNumber/BossName = %d/%q, want 0 and empty for a worker with no boss",
			info.BossNumber, info.BossName)
	}
}

// A refused ChangeBoss must surface as an error, not be swallowed into a
// read-back comparison against a value that never changed.
func TestSetWorkerBoss_PropagatesHTTPFailure(t *testing.T) {
	m := newFieldMock(t)
	m.rejectAt = "ChangeBoss"

	err := m.client().SetWorkerBoss(context.Background(), 3, 4)
	if err == nil {
		t.Fatal("a refused boss change must error")
	}
	if !strings.Contains(err.Error(), "setting the boss") {
		t.Errorf("error should name the operation, got %q", err.Error())
	}
}

// If the write lands but verification cannot run, that is not a success: an
// unverifiable write is reported as such rather than assumed good (ARCH1.8).
func TestSetWorkerBoss_UnverifiableWriteIsAnError(t *testing.T) {
	m := newFieldMock(t)
	m.rejectAt = "WorkerInfo"

	err := m.client().SetWorkerBoss(context.Background(), 3, 4)
	if err == nil {
		t.Fatal("a boss change that cannot be verified must error")
	}
	if !strings.Contains(err.Error(), "could not verify") {
		t.Errorf("error should say verification failed, got %q", err.Error())
	}
}
