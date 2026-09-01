package client

import (
	"context"
	"errors"
	"fmt"
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

// --- F4: a bounded survey must say that it was bounded --------------------
//
// ScanSettingKeys stops after maxDeepScanEmployees and skips employees whose
// full record cannot be fetched. Both are deliberate. What is not deliberate is
// doing either silently: the guard downstream tells a user that a key exists
// nowhere on their account, and that claim is only honest if the whole account
// was actually examined.

func settingsScanServer(t *testing.T, employees int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ActiveEmployeesList" {
			var b strings.Builder
			b.WriteString("[")
			for i := 1; i <= employees; i++ {
				if i > 1 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"number":%d,"name":"E%d","settings":[{"key":"listed","value":"v"}]}`, i, i)
			}
			b.WriteString("]")
			_, _ = w.Write([]byte(b.String()))
			return
		}
		_, _ = w.Write([]byte(`{"number":1,"name":"E","settings":[{"key":"deep_only","value":"v"}]}`))
	}))
}

func TestScanSettingKeys_SmallAccountIsComplete(t *testing.T) {
	srv := settingsScanServer(t, 3)
	defer srv.Close()

	scan, err := newWebAPIv2(testConfig(srv.URL)).ScanSettingKeys(context.Background())
	if err != nil {
		t.Fatalf("ScanSettingKeys: %v", err)
	}
	if !scan.Complete() {
		t.Errorf("a 3-employee account fits well inside the cap; scan should be complete: %+v", scan)
	}
	if scan.Employees != 3 || scan.Scanned != 3 || scan.Failed != 0 {
		t.Errorf("got %+v, want Employees=3 Scanned=3 Failed=0", scan)
	}
}

func TestScanSettingKeys_TruncationIsReportedNotHidden(t *testing.T) {
	srv := settingsScanServer(t, maxDeepScanEmployees+40)
	defer srv.Close()

	scan, err := newWebAPIv2(testConfig(srv.URL)).ScanSettingKeys(context.Background())
	if err != nil {
		t.Fatalf("ScanSettingKeys: %v", err)
	}
	if scan.Complete() {
		t.Error("the deep scan stopped at the cap; reporting the result as complete is a lie")
	}
	if scan.Employees != maxDeepScanEmployees+40 {
		t.Errorf("Employees = %d, want %d", scan.Employees, maxDeepScanEmployees+40)
	}
	if scan.Scanned != maxDeepScanEmployees {
		t.Errorf("Scanned = %d, want %d", scan.Scanned, maxDeepScanEmployees)
	}
}

func TestScanSettingKeys_FailedFetchesAreCountedAndMakeItIncomplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ActiveEmployeesList" {
			_, _ = w.Write([]byte(`[
				{"number":1,"name":"A","settings":[{"key":"listed","value":"v"}]},
				{"number":2,"name":"B","settings":[{"key":"listed","value":"v"}]}
			]`))
			return
		}
		if r.URL.Query().Get("employeeNumber") == "2" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"number":1,"name":"A","settings":[{"key":"deep_only","value":"v"}]}`))
	}))
	defer srv.Close()

	scan, err := newWebAPIv2(testConfig(srv.URL)).ScanSettingKeys(context.Background())
	if err != nil {
		t.Fatalf("ScanSettingKeys: %v", err)
	}
	if scan.Failed != 1 {
		t.Errorf("Failed = %d, want 1", scan.Failed)
	}
	if scan.Complete() {
		t.Error("one employee's record could not be read; the survey is not complete")
	}
	// The keys it did reach must still come back — degraded, not empty.
	if len(scan.Keys) == 0 {
		t.Error("a partial scan must still return the keys it found")
	}
}
