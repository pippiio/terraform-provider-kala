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

		switch {
		case strings.Contains(r.URL.Path, "AddCustomer"):
			id := m.next
			m.next++
			if !m.noReflect {
				m.store[id] = writeRecord(id, parsed)
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
