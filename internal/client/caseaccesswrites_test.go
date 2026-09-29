package client

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The body the Kala UI sends, captured 2026-09-29. Every key matters: the
// endpoint is keyed on workerNumber (not workerNr) and caseNr (a string), and
// role must be an empty ARRAY -- a nil slice would marshal as null.
func TestSetCaseAccess_GrantSendsTheCapturedBody(t *testing.T) {
	m := newCaseAccessMock(t)
	m.granted = []int64{1}

	if err := m.client().SetCaseAccess(context.Background(), "KA-2", 23, true); err != nil {
		t.Fatalf("SetCaseAccess(grant): %v", err)
	}
	if len(m.grantBodies) != 1 {
		t.Fatalf("GrantAccess called %d times, want 1", len(m.grantBodies))
	}
	raw, _ := json.Marshal(m.grantBodies[0])
	for _, want := range []string{`"workerNumber":23`, `"caseNr":"KA-2"`, `"access":true`, `"role":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("GrantAccess body %s is missing %s", raw, want)
		}
	}
}

func TestSetCaseAccess_RevokeSendsAccessFalseAndIsConfirmed(t *testing.T) {
	m := newCaseAccessMock(t)
	m.granted = []int64{1, 23}

	if err := m.client().SetCaseAccess(context.Background(), "KA-2", 23, false); err != nil {
		t.Fatalf("SetCaseAccess(revoke): %v", err)
	}
	if len(m.grantBodies) != 1 || m.grantBodies[0]["access"] != false {
		t.Fatalf("want one GrantAccess with access=false; got %v", m.grantBodies)
	}
	// Read-back happened: the write is only trusted once the list confirms it.
	if m.grantedHits == 0 {
		t.Error("the revoke was not verified by reading the access list back")
	}
}

// ARCH1.8: a write the read-back does not confirm is an error, however the
// write itself answered.
func TestSetCaseAccess_UnconfirmedWriteIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		before  []int64
		granted bool
	}{
		{"grant not reflected", []int64{1}, true},
		{"revoke not reflected", []int64{1, 23}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newCaseAccessMock(t)
			m.granted = tc.before
			m.grantNoReflect = true

			err := m.client().SetCaseAccess(context.Background(), "KA-2", 23, tc.granted)
			if err == nil {
				t.Fatal("an unconfirmed write must error")
			}
			if !strings.Contains(err.Error(), "23") || !strings.Contains(err.Error(), "KA-2") {
				t.Errorf("the error must name the employee and the case; got %v", err)
			}
		})
	}
}

// A refusal arrives as HTTP 200 with {"status":"Error"}; Kala's message is the
// most specific explanation available and must reach the operator verbatim.
func TestSetCaseAccess_RefusedWriteSurfacesKalasMessage(t *testing.T) {
	m := newCaseAccessMock(t)
	m.granted = []int64{1}
	m.grantResponse = `{"status":"Error","message":"Medarbejderen findes ikke"}`

	err := m.client().SetCaseAccess(context.Background(), "KA-2", 99, true)
	if err == nil || !strings.Contains(err.Error(), "Medarbejderen findes ikke") {
		t.Fatalf("want Kala's refusal message; got %v", err)
	}
	if m.grantedHits != 0 {
		t.Error("a refused write must not be followed by a read-back that could mask it")
	}
}

// The write went out, but the access list could not be read back: the change
// may or may not have landed, and the error must say it was sent but not
// verified rather than claim either outcome.
func TestSetCaseAccess_UnverifiableWriteSaysSo(t *testing.T) {
	m := newCaseAccessMock(t)
	m.granted = []int64{1}
	m.grantedStatus = 500

	err := m.client().SetCaseAccess(context.Background(), "KA-2", 23, true)
	if err == nil || !strings.Contains(err.Error(), "could not be verified") {
		t.Fatalf("want 'sent but could not be verified'; got %v", err)
	}
	if len(m.grantBodies) != 1 {
		t.Errorf("the write itself must have been sent; got %d", len(m.grantBodies))
	}
}
