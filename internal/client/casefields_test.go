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

// caseFieldMock serves the handshake, GetJobDetailsAdvanced, and the case
// field-setters, reflecting writes into the case it serves back.
//
// It is deliberately STRICT: it rejects a write whose previous value does not
// match what it holds, exactly as Kala does. A permissive mock is what let the
// GetAllJobsSimplePaged defects ship, so this one refuses what the real API
// refuses.
type caseFieldMock struct {
	srv    *httptest.Server
	paths  []string
	bodies []map[string]any

	name, address, zip, phone string

	noReflect bool
	rejectAt  string

	// moveAddressOnNextWrite simulates someone else changing the case between
	// this client's read and its write.
	moveAddressOnNextWrite string

	// failReadAfter makes the Nth and later case reads fail, so the
	// verification read can break independently of the write.
	failReadAfter int
	reads         int
}

func newCaseFieldMock(t *testing.T) *caseFieldMock {
	t.Helper()
	m := &caseFieldMock{name: "Test Project 12", address: "Testcenterstien", zip: "2200", phone: ""}

	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"secureLoginToken":"t","companies":[{"id":4242}]}`))
			return
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
			return
		}

		if m.rejectAt != "" && strings.Contains(r.URL.Path, m.rejectAt) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if strings.Contains(r.URL.Path, "GetJobDetailsAdvanced") {
			m.reads++
			if m.failReadAfter > 0 && m.reads >= m.failReadAfter {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			m.paths = append(m.paths, r.URL.Path)
			m.bodies = append(m.bodies, nil)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"caseId": 4, "caseNumber": "KA-4", "caseName": m.name,
				"address": m.address, "zip": m.zip,
				// The real key. An invented one made read-back silently fail.
				"customersTelephone": m.phone,
				"isFinished":         false, "internalProject": false,
			})
			return
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.paths = append(m.paths, r.URL.Path)
		m.bodies = append(m.bodies, body)

		str := func(k string) string {
			if v, ok := body[k].(string); ok {
				return v
			}
			return ""
		}

		// Compare-and-swap, exactly as Kala does it: an unmatched previous
		// value is refused with an HTML page whose title is the explanation.
		refuse := func(what string) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("<html><head><title>Previous " + what +
				" doesn't match</title></head></html>"))
		}

		switch {
		case strings.Contains(r.URL.Path, "RenameCaseCustomerPhoneNumber"):
			if str("oldPhoneNumber") != m.phone {
				refuse("phone number")
				return
			}
			if !m.noReflect {
				m.phone = str("newPhoneNumber")
			}
			_, _ = w.Write([]byte(`{"success":true,"newPhoneNumber":"` + str("newPhoneNumber") + `"}`))
		case strings.Contains(r.URL.Path, "RenameCase"):
			if str("previousCaseName") != m.name {
				refuse("case name")
				return
			}
			if !m.noReflect {
				m.name = str("newCaseName")
			}
			_, _ = w.Write([]byte(`{"success":true,"newCaseName":"` + str("newCaseName") + `"}`))
		case strings.Contains(r.URL.Path, "ChangeCaseAddress"):
			if m.moveAddressOnNextWrite != "" {
				m.address = m.moveAddressOnNextWrite
				m.moveAddressOnNextWrite = ""
			}
			if str("previousAddress") != m.address {
				refuse("address")
				return
			}
			if !m.noReflect {
				m.address = str("newAddress")
			}
			_, _ = w.Write([]byte(`{"success":true,"newAddress":"` + str("newAddress") + `"}`))
		case strings.Contains(r.URL.Path, "ChangeCaseZip"):
			if str("previousZip") != m.zip {
				refuse("zip")
				return
			}
			if !m.noReflect {
				m.zip = str("newZip")
			}
			_, _ = w.Write([]byte(`{"success":true,"newZip":"` + str("newZip") + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *caseFieldMock) client() InternalClient {
	return NewInternal(InternalConfig{
		Endpoint: m.srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond,
	})
}

// The path, the previous-key spelling, and the new-value key differ per
// endpoint. Three say "previous", one says "old". Every difference fails
// silently upstream, so all four are pinned.
func TestSetCaseField_UsesTheRightPathAndKeys(t *testing.T) {
	cases := []struct {
		field             CaseField
		wantPath          string
		wantPrev, wantNew string
	}{
		{CaseFieldName, "/api/RenameCase/", "previousCaseName", "newCaseName"},
		{CaseFieldAddress, "/api/ChangeCaseAddress/", "previousAddress", "newAddress"},
		{CaseFieldZip, "/api/ChangeCaseZip/", "previousZip", "newZip"},
		{CaseFieldContactPhone, "/api/RenameCaseCustomerPhoneNumber/", "oldPhoneNumber", "newPhoneNumber"},
	}
	for _, tc := range cases {
		t.Run(string(tc.field), func(t *testing.T) {
			m := newCaseFieldMock(t)
			if err := m.client().SetCaseField(context.Background(), "KA-4", tc.field, "changed"); err != nil {
				t.Fatalf("SetCaseField: %v", err)
			}
			var write map[string]any
			var path string
			for i, p := range m.paths {
				if !strings.Contains(p, "GetJobDetails") {
					path, write = p, m.bodies[i]
				}
			}
			if !strings.HasSuffix(path, tc.wantPath) {
				t.Errorf("path = %q, want suffix %q", path, tc.wantPath)
			}
			if write["caseNr"] != "KA-4" {
				t.Errorf("caseNr = %v; writes key on the STRING number, reads on the integer id", write["caseNr"])
			}
			if _, ok := write[tc.wantPrev]; !ok {
				t.Errorf("body omits %q; Kala validates the previous value", tc.wantPrev)
			}
			if write[tc.wantNew] != "changed" {
				t.Errorf("%s = %v, want %q", tc.wantNew, write[tc.wantNew], "changed")
			}
		})
	}
}

// The previous value must come from a FRESH READ, not from a caller's memory.
// State is stale after external drift and always after import, and a stale
// previous value is refused.
func TestSetCaseField_ReadsBeforeWriting(t *testing.T) {
	m := newCaseFieldMock(t)
	if err := m.client().SetCaseField(context.Background(), "KA-4", CaseFieldName, "Renamed"); err != nil {
		t.Fatalf("SetCaseField: %v", err)
	}
	if len(m.paths) == 0 || !strings.Contains(m.paths[0], "GetJobDetailsAdvanced") {
		t.Fatalf("the case must be read before the write; paths = %v", m.paths)
	}
	var sentPrev any
	for i, p := range m.paths {
		if strings.Contains(p, "RenameCase") {
			sentPrev = m.bodies[i]["previousCaseName"]
		}
	}
	if sentPrev != "Test Project 12" {
		t.Errorf("previousCaseName = %v, want the value just read from upstream", sentPrev)
	}
}

// A value that already matches needs no write. A no-op write is a request that
// can only fail -- and against a compare-and-swap endpoint it is a race waiting
// to happen.
func TestSetCaseField_NoChangeMakesNoWrite(t *testing.T) {
	m := newCaseFieldMock(t)
	if err := m.client().SetCaseField(context.Background(), "KA-4", CaseFieldName, "Test Project 12"); err != nil {
		t.Fatalf("SetCaseField: %v", err)
	}
	for _, p := range m.paths {
		if strings.Contains(p, "RenameCase") {
			t.Errorf("an unchanged value was written anyway: %v", m.paths)
		}
	}
}

// HTTP 200 is a claim, not proof.
func TestSetCaseField_UnverifiedWriteFails(t *testing.T) {
	m := newCaseFieldMock(t)
	m.noReflect = true
	if err := m.client().SetCaseField(context.Background(), "KA-4", CaseFieldName, "Renamed"); err == nil {
		t.Fatal("a write that did not land must fail read-back verification")
	}
}

// A concurrent change is reported as ErrConflict, with Kala's own explanation,
// and is NOT retried -- it is deterministic.
func TestSetCaseField_ConcurrentChangeIsAConflict(t *testing.T) {
	m := newCaseFieldMock(t)
	// The read returns "Test Project 12"; the store moves underneath before
	// the write lands, which is exactly the race the previous value guards.
	m.name = "Test Project 12"
	c := m.client()

	// Prime the session so the handshake does not confuse the path assertions.
	_, _ = c.GetCase(context.Background(), "KA-4")

	// The read sees this value; the mock then moves it before the write lands,
	// which is exactly the race the previous value guards against.
	m.address = "Testcenterstien"
	m.moveAddressOnNextWrite = "moved by someone else"

	err := c.SetCaseField(context.Background(), "KA-4", CaseFieldAddress, "New Street")
	if err == nil {
		t.Fatal("a concurrent change must be reported, not silently overwritten")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict so callers can tell a race from a server fault", err)
	}
	if !strings.Contains(err.Error(), "doesn't match") {
		t.Errorf("Kala's own explanation must survive into the message: %v", err)
	}
}

func TestSetCaseField_UnknownFieldIsRejected(t *testing.T) {
	m := newCaseFieldMock(t)
	if err := m.client().SetCaseField(context.Background(), "KA-4", CaseField("nonsense"), "x"); err == nil {
		t.Fatal("an unknown field must be refused rather than silently ignored")
	}
}

func TestSetCaseField_ReadFailurePropagates(t *testing.T) {
	m := newCaseFieldMock(t)
	m.rejectAt = "GetJobDetailsAdvanced"
	if err := m.client().SetCaseField(context.Background(), "KA-4", CaseFieldName, "Renamed"); err == nil {
		t.Fatal("if the case cannot be read, the previous value is unknown and the write must not proceed")
	}
}

// The write may have landed. Without a read-back that cannot be established,
// so the failure must say so rather than report either outcome as fact --
// especially here, where retrying would re-run a compare-and-swap whose
// previous value is now unknown.
func TestSetCaseField_ReadBackFailureIsReported(t *testing.T) {
	m := newCaseFieldMock(t)
	m.failReadAfter = 2 // the pre-write read succeeds; the verification read does not

	err := m.client().SetCaseField(context.Background(), "KA-4", CaseFieldName, "Renamed")
	if err == nil {
		t.Fatal("an unverifiable write must not be reported as success")
	}
	if !strings.Contains(err.Error(), "read back") {
		t.Errorf("the message must say verification failed, not that the write did: %v", err)
	}
}
