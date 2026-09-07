package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// customerWriteMock serves the handshake, the two customer write endpoints, and the
// list read the write path verifies against.
//
// Response shapes are copied from payloads observed 2026-09-07 against tenant
// 17221, including the awkward parts: the {"status","customerId"} envelope on
// create, and `additionalFields` riding along on every record.
type customerWriteMock struct {
	srv    *httptest.Server
	paths  []string
	bodies []map[string]any

	// store is the served customer list, keyed by id.
	store map[int64]map[string]any
	next  int64

	// noReflect accepts writes with HTTP 200 but does not apply them, the
	// "reported success, changed nothing" shape read-back exists to catch.
	noReflect bool

	// errorEnvelope makes writes answer HTTP 200 carrying Kala's in-body
	// failure. The status line is never evidence a write landed.
	errorEnvelope bool

	// truncate makes the list read report more records than it returns, so
	// a selection miss cannot be distinguished from a genuine absence.
	truncate bool

	// dropOnCreate names a field the CREATE endpoint silently discards while
	// still reporting success -- the real, unresolved question about `ean`.
	dropOnCreate string

	// noCustomerID makes create answer Success while omitting the identifier.
	noCustomerID bool

	// rejectAt fails any request whose path contains it, so a test can refuse
	// the write or the verification read independently.
	rejectAt string

	// badJSON makes create answer with a body that is not JSON at all.
	badJSON bool
}

func newCustomerWriteMock(t *testing.T) *customerWriteMock {
	t.Helper()
	m := &customerWriteMock{store: map[int64]map[string]any{}, next: 4}

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

		if strings.Contains(r.URL.Path, "GetCustomersPaged2") {
			list := make([]map[string]any, 0, len(m.store))
			for _, c := range m.store {
				list = append(list, c)
			}
			total := len(list)
			if m.truncate {
				list, total = nil, 99
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"customers": list, "totalCount": total,
			})
			return
		}

		var parsed map[string]any
		_ = json.NewDecoder(r.Body).Decode(&parsed)
		m.paths = append(m.paths, r.URL.Path)
		m.bodies = append(m.bodies, parsed)

		if m.errorEnvelope {
			_, _ = w.Write([]byte(`{"status":"Error","message":"Kunden kunne ikke gemmes."}`))
			return
		}

		if m.badJSON {
			_, _ = w.Write([]byte(`<html>not json</html>`))
			return
		}

		switch {
		case strings.Contains(r.URL.Path, "AddCustomer"):
			id := m.next
			m.next++
			if !m.noReflect {
				if m.dropOnCreate != "" {
					delete(parsed, m.dropOnCreate)
				}
				m.store[id] = writeRecord(id, parsed)
			}
			if m.noCustomerID {
				_, _ = w.Write([]byte(`{"status":"Success"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "Success", "customerId": id})
		case strings.Contains(r.URL.Path, "EditCustomer"):
			if !m.noReflect {
				id := int64(parsed["customerId"].(float64))
				m.store[id] = writeRecord(id, parsed)
			}
			_, _ = w.Write([]byte(`{"status":"Success"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// record renders a write body as the list payload Kala serves back.
func writeRecord(id int64, in map[string]any) map[string]any {
	get := func(k string) any {
		if v, ok := in[k]; ok && v != nil {
			return v
		}
		return ""
	}
	return map[string]any{
		"id": id, "number": "KA-" + strconv.FormatInt(id, 10),
		"firstName": get("firstName"), "lastName": get("lastName"),
		"company": get("company"), "phone": get("phone"), "address": get("address"),
		"zip": get("zip"), "city": nil, "email": get("email"), "cvr": get("cvr"),
		"description": get("description"), "ean": get("ean"),
		"caseCount": 0, "caseTitles": nil, "additionalFields": map[string]any{},
	}
}

func (m *customerWriteMock) client() InternalClient {
	return NewInternal(InternalConfig{
		Endpoint: m.srv.URL, Username: "u", Password: "p", retryBaseDur: time.Microsecond,
	})
}

func fullInput() CustomerInput {
	return CustomerInput{
		FirstName: "Bilbo", LastName: "Baggins", Company: "Bag End Ltd",
		Phone: "+45 20 00 00 01", Address: "Bagshot Row 1", Zip: "2200",
		Email: "bilbo@example.com", CVR: "12345678",
		Description: "Managed by Terraform", EAN: "5790000000000",
	}
}

// ADR-003 constraint 4: create sends the FULL field set. Which fields the
// endpoint accepts is not trustworthy by inference -- a claim that AddCustomer
// rejects cvr was drawn from one payload's omission and was wrong.
func TestAddCustomer_SendsFullFieldSetToCorrectPath(t *testing.T) {
	m := newCustomerWriteMock(t)
	if _, err := m.client().AddCustomer(context.Background(), fullInput()); err != nil {
		t.Fatalf("AddCustomer: %v", err)
	}
	if len(m.paths) == 0 {
		t.Fatal("no request was made")
	}
	if !strings.HasSuffix(m.paths[0], "/api/AddCustomer/") {
		t.Errorf("path = %q, want suffix /api/AddCustomer/ (trailing slash is load-bearing)", m.paths[0])
	}
	for _, key := range []string{
		"firstName", "lastName", "company", "phone", "address", "zip", "email",
		"cvr", "description", "ean",
	} {
		if _, ok := m.bodies[0][key]; !ok {
			t.Errorf("create body omits %q; the full field set must be sent", key)
		}
	}
}

// Create echoes the identity Kala allocated. This is what makes FR5 possible:
// state can be recorded before any follow-up call runs.
func TestAddCustomer_ReturnsAllocatedIdentity(t *testing.T) {
	m := newCustomerWriteMock(t)
	got, err := m.client().AddCustomer(context.Background(), fullInput())
	if err != nil {
		t.Fatalf("AddCustomer: %v", err)
	}
	if got.ID != 4 {
		t.Errorf("ID = %d, want 4 (from the create response envelope)", got.ID)
	}
	if got.Company != "Bag End Ltd" {
		t.Errorf("Company = %q, want the persisted value read back", got.Company)
	}
	if got.Description != "Managed by Terraform" {
		t.Errorf("Description = %q; description is carried by the payload and must be mapped", got.Description)
	}
}

// A refused write answers HTTP 200 with an in-body error. The status line is
// never evidence that a write landed.
func TestAddCustomer_InBodyErrorIsFailure(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.errorEnvelope = true
	if _, err := m.client().AddCustomer(context.Background(), fullInput()); err == nil {
		t.Fatal("a 200 carrying {\"status\":\"Error\"} must be an error, not a success")
	}
}

// ARCH1.8: an unconfirmed write is an error, not a success.
func TestAddCustomer_UnverifiedWriteFails(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.noReflect = true
	if _, err := m.client().AddCustomer(context.Background(), fullInput()); err == nil {
		t.Fatal("create that did not persist must fail read-back verification")
	}
}

// EditCustomer keys on the INTEGER customerId, while pricing keys on the STRING
// customerNumber. Same entity, two keys; getting it wrong fails silently.
func TestEditCustomer_KeysOnIntegerCustomerID(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.store[2] = writeRecord(2, map[string]any{"company": "Old"})
	in := fullInput()
	if _, err := m.client().EditCustomer(context.Background(), 2, in); err != nil {
		t.Fatalf("EditCustomer: %v", err)
	}
	if len(m.bodies) == 0 {
		t.Fatal("no request was made")
	}
	if !strings.HasSuffix(m.paths[0], "/api/EditCustomer/") {
		t.Errorf("path = %q, want suffix /api/EditCustomer/", m.paths[0])
	}
	id, ok := m.bodies[0]["customerId"]
	if !ok {
		t.Fatal("edit body omits customerId; it is the identifier this endpoint keys on")
	}
	if n, isNum := id.(float64); !isNum || int64(n) != 2 {
		t.Errorf("customerId = %v (%T), want integer 2", id, id)
	}
}

// EditCustomer is a full-record replace: an omitted field is BLANKED upstream.
func TestEditCustomer_SendsEveryFieldSoOmissionCannotBlank(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.store[2] = writeRecord(2, map[string]any{"company": "Old"})
	if _, err := m.client().EditCustomer(context.Background(), 2, fullInput()); err != nil {
		t.Fatalf("EditCustomer: %v", err)
	}
	if len(m.bodies) == 0 {
		t.Fatal("no request was made")
	}
	for _, key := range []string{
		"firstName", "lastName", "company", "phone", "address", "zip", "email",
		"cvr", "description", "ean",
	} {
		if _, ok := m.bodies[0][key]; !ok {
			t.Errorf("edit body omits %q -- a full-record replace would blank it upstream", key)
		}
	}
}

func TestEditCustomer_UnverifiedWriteFails(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.store[2] = writeRecord(2, map[string]any{"company": "Old"})
	m.noReflect = true
	if _, err := m.client().EditCustomer(context.Background(), 2, fullInput()); err == nil {
		t.Fatal("edit that did not persist must fail read-back verification")
	}
}

// Kala has no by-id customer endpoint, so GetCustomer selects from a list read.
// Absence from an INCOMPLETE read proves nothing and must not be ErrNotFound.
func TestGetCustomer_PartialReadIsNotNotFound(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.truncate = true
	_, err := m.client().GetCustomer(context.Background(), 7)
	if err == nil {
		t.Fatal("a miss inside a truncated read must be an error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("a miss inside a truncated read must NOT be ErrNotFound -- absence from a partial read proves nothing")
	}
}

// ADR-003 constraint 4 in action. Which fields create accepts is not
// trustworthy by inference, so a value that did not land is converged by an
// edit rather than reported as a failure or assumed unsupported. `ean` is the
// real open case this exists for.
func TestAddCustomer_ConvergesFieldsCreateSilentlyDropped(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.dropOnCreate = "ean"

	got, err := m.client().AddCustomer(context.Background(), fullInput())
	if err != nil {
		t.Fatalf("AddCustomer: %v", err)
	}
	if got.EAN != "5790000000000" {
		t.Errorf("EAN = %q, want the configured value converged by a follow-up edit", got.EAN)
	}

	var sawEdit bool
	for _, p := range m.paths {
		if strings.HasSuffix(p, "/api/EditCustomer/") {
			sawEdit = true
		}
	}
	if !sawEdit {
		t.Error("a field dropped on create must be converged by EditCustomer, not left unset")
	}
}

// Kala has no delete. A caller that loses the id of a record it just created
// has stranded it permanently, so the id must survive a read-back failure
// alongside the error (FR5).
func TestAddCustomer_ReadBackFailureStillReturnsTheAllocatedID(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.noReflect = true

	got, err := m.client().AddCustomer(context.Background(), fullInput())
	if err == nil {
		t.Fatal("want an error when the created record cannot be read back")
	}
	if got.ID == 0 {
		t.Fatal("the allocated id must be returned even on failure -- without it the record is stranded and Kala has no delete")
	}
}

// A create that reports success without an identifier is a failure: the record
// exists and cannot be addressed.
func TestAddCustomer_SuccessWithoutIDIsAnError(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.noCustomerID = true

	if _, err := m.client().AddCustomer(context.Background(), fullInput()); err == nil {
		t.Fatal("success without a customerId must be an error -- the new record cannot be identified")
	}
}

// The counterpart to the truncated-read case: when the read DID cover
// everything, absence is genuine and must be ErrNotFound so callers can treat
// it as drift (TF1.2).
func TestGetCustomer_AbsentFromCompleteReadIsNotFound(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.store[1] = writeRecord(1, map[string]any{"company": "Bag End Ltd"})

	_, err := m.client().GetCustomer(context.Background(), 99)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound when the read covered every record", err)
	}
}

// The create response is the only place the new id exists. A body that cannot
// be decoded is a decode error, not a silent zero id.
func TestAddCustomer_UndecodableResponseIsADecodeError(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.badJSON = true

	_, err := m.client().AddCustomer(context.Background(), fullInput())
	if !errors.Is(err, ErrDecode) {
		t.Errorf("err = %v, want ErrDecode", err)
	}
}

func TestEditCustomer_HTTPFailurePropagates(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.store[2] = writeRecord(2, map[string]any{"company": "Old"})
	m.rejectAt = "EditCustomer"

	if _, err := m.client().EditCustomer(context.Background(), 2, fullInput()); err == nil {
		t.Fatal("an HTTP failure on the write must propagate")
	}
}

// The write may have landed; without a read-back that cannot be established,
// so the id is returned alongside the error rather than the record being
// reported as updated.
func TestEditCustomer_ReadBackFailureReturnsIDAndError(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.store[2] = writeRecord(2, map[string]any{"company": "Old"})
	m.rejectAt = "GetCustomersPaged2"

	got, err := m.client().EditCustomer(context.Background(), 2, fullInput())
	if err == nil {
		t.Fatal("want an error when the record cannot be read back")
	}
	if got.ID != 2 {
		t.Errorf("ID = %d, want 2 so the caller can still address the record", got.ID)
	}
}

func TestGetCustomer_ListFailurePropagates(t *testing.T) {
	m := newCustomerWriteMock(t)
	m.rejectAt = "GetCustomersPaged2"

	if _, err := m.client().GetCustomer(context.Background(), 1); err == nil {
		t.Fatal("a failed list read must propagate, not read as not-found")
	}
}
