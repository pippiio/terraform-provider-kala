package client

import (
	"errors"
	"strings"
	"testing"
)

// The webapiv2 documentation lists Employee's field NAMES but specifies no
// types and gives no example payload. These tests pin the decoding assumptions
// so that Phase 4's smoke test against the real API has something concrete to
// contradict (risk R1), and so an upstream field rename fails loudly rather
// than zero-filling silently (risk R8).

func TestDecodeEmployee_FullPayload(t *testing.T) {
	body := `{
		"number": 4711,
		"name": "Anders Jensen",
		"title": "Montør",
		"image": "https://app.kala.dk/img/4711.jpg",
		"phone": "+45 12 34 56 78",
		"isAdmin": false,
		"isLeader": true,
		"settings": [
			{"key": "default_work_type", "value": "montage"},
			{"key": "can_approve_hours", "value": "true"}
		]
	}`

	got, err := decodeEmployee([]byte(body))
	if err != nil {
		t.Fatalf("decodeEmployee: %v", err)
	}

	if got.Number != 4711 {
		t.Errorf("Number = %d, want 4711", got.Number)
	}
	if got.Name != "Anders Jensen" {
		t.Errorf("Name = %q, want %q", got.Name, "Anders Jensen")
	}
	if got.Title != "Montør" {
		t.Errorf("Title = %q, want %q (non-ASCII must survive)", got.Title, "Montør")
	}
	if got.IsAdmin {
		t.Error("IsAdmin = true, want false")
	}
	if !got.IsLeader {
		t.Error("IsLeader = false, want true")
	}
	if len(got.Settings) != 2 {
		t.Fatalf("len(Settings) = %d, want 2", len(got.Settings))
	}
	if got.Settings[0].Key != "default_work_type" || got.Settings[0].Value != "montage" {
		t.Errorf("Settings[0] = %+v, want {default_work_type montage}", got.Settings[0])
	}
}

// Settings absent / null / empty must all yield an empty non-nil slice.
// A nil slice would surface in Terraform as null rather than [], producing a
// spurious diff against a config that declares no settings.
func TestDecodeEmployee_SettingsAbsentNullOrEmpty(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"settings key absent", `{"number":1,"name":"A"}`},
		{"settings null", `{"number":1,"name":"A","settings":null}`},
		{"settings empty array", `{"number":1,"name":"A","settings":[]}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeEmployee([]byte(tc.body))
			if err != nil {
				t.Fatalf("decodeEmployee: %v", err)
			}
			if got.Settings == nil {
				t.Error("Settings is nil; want empty non-nil slice so Terraform sees [] not null")
			}
			if len(got.Settings) != 0 {
				t.Errorf("len(Settings) = %d, want 0", len(got.Settings))
			}
		})
	}
}

// number is the record's identity. An employee without one is never valid, so
// its absence must fail loudly rather than decode to zero (risk R8: Go's
// encoding/json zero-fills missing fields, so a rename degrades silently).
func TestDecodeEmployee_MissingOrZeroNumberIsAnError(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"number absent", `{"name":"A"}`},
		{"number zero", `{"number":0,"name":"A"}`},
		{"number null", `{"number":null,"name":"A"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeEmployee([]byte(tc.body))
			if err == nil {
				t.Fatal("want an error for a record with no identity, got nil")
			}
			if !errors.Is(err, ErrDecode) {
				t.Errorf("want errors.Is(err, ErrDecode), got %v", err)
			}
		})
	}
}

// Optional fields stay lenient — only identity is strict.
func TestDecodeEmployee_OptionalFieldsMayBeAbsent(t *testing.T) {
	got, err := decodeEmployee([]byte(`{"number":99}`))
	if err != nil {
		t.Fatalf("decodeEmployee: %v", err)
	}
	if got.Number != 99 {
		t.Errorf("Number = %d, want 99", got.Number)
	}
	if got.Name != "" || got.Title != "" || got.Phone != "" {
		t.Errorf("absent optional fields should be empty, got %+v", got)
	}
}

// Unknown fields must be tolerated: the API is undocumented enough that new
// fields appearing is likelier than not, and they must not break existing users.
func TestDecodeEmployee_UnknownFieldsTolerated(t *testing.T) {
	got, err := decodeEmployee([]byte(`{"number":7,"name":"A","brandNewField":123,"another":{"x":1}}`))
	if err != nil {
		t.Fatalf("unknown fields must not fail decoding: %v", err)
	}
	if got.Number != 7 {
		t.Errorf("Number = %d, want 7", got.Number)
	}
}

func TestDecodeEmployee_MalformedJSON(t *testing.T) {
	_, err := decodeEmployee([]byte(`{"number": `))
	if err == nil {
		t.Fatal("want an error for malformed JSON, got nil")
	}
	if !errors.Is(err, ErrDecode) {
		t.Errorf("want errors.Is(err, ErrDecode), got %v", err)
	}
}

func TestDecodeEmployeeList_Success(t *testing.T) {
	body := `[{"number":1,"name":"A"},{"number":2,"name":"B"}]`

	got, err := decodeEmployeeList([]byte(body))
	if err != nil {
		t.Fatalf("decodeEmployeeList: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[1].Number != 2 || got[1].Name != "B" {
		t.Errorf("got[1] = %+v, want {2 B}", got[1])
	}
}

func TestDecodeEmployeeList_EmptyArray(t *testing.T) {
	got, err := decodeEmployeeList([]byte(`[]`))
	if err != nil {
		t.Fatalf("decodeEmployeeList: %v", err)
	}
	if got == nil {
		t.Error("want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// One bad record must fail the whole page rather than being silently dropped —
// silently returning 49 of 50 employees would be a correctness bug that
// Terraform would interpret as a deletion.
func TestDecodeEmployeeList_RejectsPageWithInvalidRecord(t *testing.T) {
	body := `[{"number":1,"name":"A"},{"name":"no-identity"}]`

	_, err := decodeEmployeeList([]byte(body))
	if err == nil {
		t.Fatal("want an error when any record in the page is invalid, got nil")
	}
	if !errors.Is(err, ErrDecode) {
		t.Errorf("want errors.Is(err, ErrDecode), got %v", err)
	}
	if !strings.Contains(err.Error(), "index 1") {
		t.Errorf("error should identify which record failed, got %q", err.Error())
	}
}
