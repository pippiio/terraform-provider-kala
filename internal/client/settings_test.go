package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

func TestApplyEmployeeSetting_SendsAllRequiredParameters(t *testing.T) {
	var got map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		q := r.URL.Query()
		got = map[string]string{
			"employeeNumber": q.Get("employeeNumber"),
			"key":            q.Get("key"),
			"value":          q.Get("value"),
			"friendlyName":   q.Get("friendlyName"),
			"type":           q.Get("type"),
			"api_key":        q.Get("api_key"),
		}
		_, _ = w.Write([]byte(`{"success":true,"message":"ok"}`))
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	err := c.ApplyEmployeeSetting(context.Background(), 4711, Setting{Key: "k", Value: "v"}, SettingMetadata{
		FriendlyName: "Friendly K",
		Type:         "text",
	})
	if err != nil {
		t.Fatalf("SetEmployeeSetting: %v", err)
	}

	want := map[string]string{
		"employeeNumber": "4711",
		"key":            "k",
		"value":          "v",
		"friendlyName":   "Friendly K",
		"type":           "text",
		"api_key":        "test-key",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("param %s = %q, want %q", k, got[k], v)
		}
	}
}

// friendlyName is required by the API. Sending an empty one would create a
// record we can never correct, since reads never return the field.
func TestApplyEmployeeSetting_RejectsEmptyFriendlyName(t *testing.T) {
	c := newWebAPIv2(testConfig("https://example.test"))

	err := c.ApplyEmployeeSetting(context.Background(), 1, Setting{Key: "k", Value: "v"}, SettingMetadata{})
	if err == nil {
		t.Fatal("want an error when friendlyName is empty")
	}
	if !strings.Contains(err.Error(), "friendly_name") {
		t.Errorf("error should name the missing field, got %q", err.Error())
	}
}

func TestApplyEmployeeSetting_DefaultsTypeToText(t *testing.T) {
	var gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.URL.Query().Get("type")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	if err := c.ApplyEmployeeSetting(context.Background(), 1, Setting{Key: "k", Value: "v"},
		SettingMetadata{FriendlyName: "F"}); err != nil {
		t.Fatalf("SetEmployeeSetting: %v", err)
	}

	if gotType != "text" {
		t.Errorf("type = %q, want the documented default \"text\"", gotType)
	}
}

// The {success, message} shape is undocumented beyond its field names. A
// success:false body must not be reported as a successful write.
func TestApplyEmployeeSetting_SuccessFalseIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"message":"unknown key"}`))
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	err := c.ApplyEmployeeSetting(context.Background(), 1, Setting{Key: "k", Value: "v"},
		SettingMetadata{FriendlyName: "F"})

	if err == nil {
		t.Fatal("want an error when the API reports success:false")
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("error should carry the API message, got %q", err.Error())
	}
}

func TestApplyEmployeeSetting_PropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.MaxRetries = 0
	c := newWebAPIv2(cfg)

	err := c.ApplyEmployeeSetting(context.Background(), 1, Setting{Key: "k", Value: "v"},
		SettingMetadata{FriendlyName: "F"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("want ErrUnauthorized, got %v", err)
	}
}

// ListSettingKeys backs the unknown-key guard. Since there is no delete-setting
// endpoint, a typo is permanent — this is what makes the guard worth having.
func TestListSettingKeys_ReturnsSortedUniqueKeysAcrossEmployees(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"number":1,"name":"A","settings":[{"key":"zulu","value":"1"},{"key":"alpha","value":"2"}]},
			{"number":2,"name":"B","settings":[{"key":"alpha","value":"3"},{"key":"mike","value":"4"}]},
			{"number":3,"name":"C","settings":[]}
		]`))
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	got, err := c.ListSettingKeys(context.Background())
	if err != nil {
		t.Fatalf("ListSettingKeys: %v", err)
	}

	want := []string{"alpha", "mike", "zulu"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("keys must be sorted for stable diagnostics, got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
}

func TestListSettingKeys_EmptyAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := newWebAPIv2(testConfig(srv.URL))
	got, err := c.ListSettingKeys(context.Background())
	if err != nil {
		t.Fatalf("ListSettingKeys: %v", err)
	}
	if got == nil {
		t.Error("want an empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestClosestKey_SuggestsNearMiss(t *testing.T) {
	existing := []string{"default_work_type", "can_approve_hours", "beta_ui"}

	// The exact scenario the guard exists for: a transposed "default".
	// The misspelling is deliberate test data, hence the nolint.
	if got := ClosestKey("defualt_work_type", existing); got != "default_work_type" { //nolint:misspell // intentional typo under test
		t.Errorf("ClosestKey = %q, want default_work_type", got)
	}
	if got := ClosestKey("can_aprove_hours", existing); got != "can_approve_hours" {
		t.Errorf("ClosestKey = %q, want can_approve_hours", got)
	}
}

func TestClosestKey_NoSuggestionWhenNothingIsClose(t *testing.T) {
	existing := []string{"default_work_type", "beta_ui"}

	if got := ClosestKey("completely_unrelated_thing", existing); got != "" {
		t.Errorf("ClosestKey = %q, want \"\" when nothing is near", got)
	}
}

func TestClosestKey_EmptyCandidateSet(t *testing.T) {
	if got := ClosestKey("anything", nil); got != "" {
		t.Errorf("ClosestKey = %q, want empty", got)
	}
}
