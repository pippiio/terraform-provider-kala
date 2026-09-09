package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Regression tests from the 2026-09-01 smoke test against the real Kala API.
// Each encodes behaviour the documentation does not describe and the earlier
// mocks got wrong.

// OBSERVED: requesting a nonexistent employeeNumber returns HTTP 200 with
// content-length: 0 — not 404. Decoding an empty body yields ErrDecode, so
// Read would error instead of detecting drift.
func TestGetEmployee_Empty200BodyMeansNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK) // empty body, exactly as the real API responds
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	_, err := c.GetEmployee(context.Background(), 99999999)

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("an empty 200 body must mean not-found, got %v", err)
	}
}

func TestGetEmployee_WhitespaceOnlyBodyMeansNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("  \n "))
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	if _, err := c.GetEmployee(context.Background(), 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// An empty LIST body is different: it means no employees, not an error.
func TestListEmployees_Empty200BodyIsAnEmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	got, err := c.ListEmployees(context.Background(), ListOptions{PageSize: 10})
	if err != nil {
		t.Fatalf("an empty list body should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// OBSERVED: ActiveEmployeesList under-reports settings. For the same employee
// the list returned 11 keys while ActiveEmployee returned 12, omitting
// "favorite_materials".
//
// This is why the kala_employees data source documents its settings list as
// what the list endpoint reports rather than as the complete set: reading one
// employee can legitimately return keys the list never showed.
func TestListEmployees_UnderReportsSettingsComparedToGetEmployee(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ActiveEmployeesList" {
			_, _ = w.Write([]byte(`[{"number":1,"name":"A","settings":[{"key":"listed_key","value":"v"}]}]`))
			return
		}
		// The single endpoint reveals an extra key.
		_, _ = w.Write([]byte(`{"number":1,"name":"A","settings":[
			{"key":"listed_key","value":"v"},
			{"key":"favorite_materials","value":"v"}
		]}`))
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))

	listed, err := c.ListEmployees(context.Background(), ListOptions{PageSize: 10})
	if err != nil {
		t.Fatalf("ListEmployees: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("len = %d, want 1", len(listed))
	}
	if len(listed[0].Settings) != 1 {
		t.Fatalf("list settings = %v, want just the one the list endpoint reports", listed[0].Settings)
	}

	full, err := c.GetEmployee(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetEmployee: %v", err)
	}

	keys := make(map[string]bool, len(full.Settings))
	for _, s := range full.Settings {
		keys[s.Key] = true
	}
	if !keys["favorite_materials"] {
		t.Errorf("settings = %v, want the key only the single endpoint reports", full.Settings)
	}
}

// OBSERVED: image is null in practice. It must decode to an empty string
// rather than failing.
func TestDecodeEmployee_NullImage(t *testing.T) {
	got, err := decodeEmployee([]byte(`{"number":1,"name":"A","image":null,"settings":[]}`))
	if err != nil {
		t.Fatalf("null image must decode: %v", err)
	}
	if got.Image != "" {
		t.Errorf("Image = %q, want empty", got.Image)
	}
}
