package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

func newSettingResource(c client.Client) *employeeSettingResource {
	return &employeeSettingResource{client: c}
}

func TestEmployeeSettingResource_Metadata(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewEmployeeSettingResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "kala"}, resp)

	if resp.TypeName != "kala_employee_setting" {
		t.Errorf("TypeName = %q, want kala_employee_setting", resp.TypeName)
	}
}

func TestEmployeeSettingResource_SchemaShape(t *testing.T) {
	resp := &resource.SchemaResponse{}
	NewEmployeeSettingResource().Schema(context.Background(), resource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}

	required := []string{"employee_number", "key", "value", "friendly_name"}
	for _, name := range required {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Errorf("missing attribute %q", name)
			continue
		}
		if !attr.IsRequired() {
			t.Errorf("attribute %q should be Required", name)
		}
	}

	for _, name := range []string{"type", "allow_new_key", "id"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}

	// The write-only nature of friendly_name is non-obvious and load-bearing;
	// users must be told or they will expect drift detection on it.
	fn := resp.Schema.Attributes["friendly_name"]
	if !strings.Contains(strings.ToLower(fn.GetMarkdownDescription()), "write-only") {
		t.Error("friendly_name must be documented as write-only")
	}
}

// --- unknown-key guard (FR8) ---------------------------------------------

func TestValidateKey_AcceptsExistingKey(t *testing.T) {
	fc := &fakeClient{settingKeys: []string{"default_work_type", "beta_ui"}}
	r := newSettingResource(fc)

	if _, err := r.validateKey(context.Background(), "beta_ui", false); err != nil {
		t.Errorf("existing key should validate, got %v", err)
	}
}

func TestValidateKey_RejectsUnknownKeyAndSuggestsNearest(t *testing.T) {
	fc := &fakeClient{settingKeys: []string{"default_work_type", "can_approve_hours"}}
	r := newSettingResource(fc)

	//nolint:misspell // intentional typo: this is the scenario under test
	_, err := r.validateKey(context.Background(), "defualt_work_type", false)
	if err == nil {
		t.Fatal("want an error for an unknown key")
	}

	msg := err.Error()
	if !strings.Contains(msg, "default_work_type") {
		t.Errorf("error should suggest the nearest key, got %q", msg)
	}
	// The permanence is the reason the guard exists; the message must say so.
	if !strings.Contains(msg, "permanently") {
		t.Errorf("error should explain that a typo is permanent, got %q", msg)
	}
	if !strings.Contains(msg, "allow_new_key") {
		t.Errorf("error should name the override, got %q", msg)
	}
}

func TestValidateKey_AllowNewKeySkipsTheCheckEntirely(t *testing.T) {
	// keysErr would fail the check if it ran at all.
	fc := &fakeClient{keysErr: errors.New("ListSettingKeys must not be called")}
	r := newSettingResource(fc)

	if _, err := r.validateKey(context.Background(), "brand_new_key", true); err != nil {
		t.Errorf("allow_new_key should bypass validation, got %v", err)
	}
}

func TestValidateKey_ListFailurePropagates(t *testing.T) {
	fc := &fakeClient{keysErr: errors.New("upstream down")}
	r := newSettingResource(fc)

	_, err := r.validateKey(context.Background(), "k", false)
	if err == nil {
		t.Fatal("want the list error to propagate")
	}
	if !strings.Contains(err.Error(), "upstream down") {
		t.Errorf("error should carry the cause, got %q", err.Error())
	}
}

func TestValidateKey_NoSuggestionWhenNothingIsClose(t *testing.T) {
	fc := &fakeClient{settingKeys: []string{"alpha", "beta"}}
	r := newSettingResource(fc)

	_, err := r.validateKey(context.Background(), "totally_different_thing", false)
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "Did you mean") {
		t.Errorf("should not offer a far-fetched suggestion, got %q", err.Error())
	}
}

// --- write verification (FR2, risk R3) -----------------------------------

func TestVerify_SucceedsWhenValueMatches(t *testing.T) {
	fc := &fakeClient{getEmployee: client.Employee{
		Number:   1,
		Settings: []client.Setting{{Key: "k", Value: "v"}},
	}}
	r := newSettingResource(fc)

	if err := r.verify(context.Background(), settingModelFor(1, "k", "v")); err != nil {
		t.Errorf("verify should pass, got %v", err)
	}
}

// HTTP 200 does not prove the write landed; a differing read-back must fail.
func TestVerify_FailsWhenValueDiffers(t *testing.T) {
	fc := &fakeClient{getEmployee: client.Employee{
		Number:   1,
		Settings: []client.Setting{{Key: "k", Value: "something-else"}},
	}}
	r := newSettingResource(fc)

	err := r.verify(context.Background(), settingModelFor(1, "k", "v"))
	if err == nil {
		t.Fatal("want an error when the read-back differs")
	}
	if !strings.Contains(err.Error(), "something-else") {
		t.Errorf("error should show what was actually read, got %q", err.Error())
	}
}

func TestVerify_FailsWhenSettingAbsentAfterWrite(t *testing.T) {
	fc := &fakeClient{getEmployee: client.Employee{Number: 1, Settings: []client.Setting{}}}
	r := newSettingResource(fc)

	if err := r.verify(context.Background(), settingModelFor(1, "k", "v")); err == nil {
		t.Fatal("want an error when the setting is missing after writing it")
	}
}

func TestVerify_PropagatesReadError(t *testing.T) {
	fc := &fakeClient{getErr: errors.New("read failed")}
	r := newSettingResource(fc)

	if err := r.verify(context.Background(), settingModelFor(1, "k", "v")); err == nil {
		t.Fatal("want the read error to propagate")
	}
}

// --- import (FR6) ---------------------------------------------------------

func TestParseSettingID(t *testing.T) {
	number, key, err := parseSettingID("4711:default_work_type")
	if err != nil {
		t.Fatalf("parseSettingID: %v", err)
	}
	if number != 4711 || key != "default_work_type" {
		t.Errorf("got (%d, %q), want (4711, default_work_type)", number, key)
	}
}

func TestParseSettingID_KeyMayContainColons(t *testing.T) {
	_, key, err := parseSettingID("1:ns:sub:key")
	if err != nil {
		t.Fatalf("parseSettingID: %v", err)
	}
	if key != "ns:sub:key" {
		t.Errorf("key = %q, want ns:sub:key — only the first colon separates", key)
	}
}

func TestParseSettingID_Malformed(t *testing.T) {
	for _, id := range []string{"", "4711", "4711:", ":key", "abc:key", "0:key"} {
		if _, _, err := parseSettingID(id); err == nil {
			t.Errorf("parseSettingID(%q) should have failed", id)
		}
	}
}

func TestParseSettingID_ErrorShowsExpectedFormat(t *testing.T) {
	_, _, err := parseSettingID("nonsense")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "<employee_number>:<key>") {
		t.Errorf("error should show the expected format, got %q", err.Error())
	}
}

func TestSettingID_RoundTrips(t *testing.T) {
	id := settingID(4711, "some_key")
	number, key, err := parseSettingID(id)
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	if number != 4711 || key != "some_key" {
		t.Errorf("round trip lost data: (%d, %q)", number, key)
	}
}

// --- misc -----------------------------------------------------------------

func TestIsNotFound_UsesSentinelNotMessageText(t *testing.T) {
	if !isNotFound(client.ErrNotFound) {
		t.Error("ErrNotFound should be recognised")
	}
	// A message that merely contains the words must NOT match — that would make
	// the check brittle to unrelated wording.
	if isNotFound(errors.New("the config file was not found on disk")) {
		t.Error("isNotFound must match the sentinel, not message text")
	}
	if isNotFound(nil) {
		t.Error("nil is not a not-found error")
	}
}

func TestApply_PassesWriteOnlyMetadataThrough(t *testing.T) {
	fc := &fakeClient{}
	r := newSettingResource(fc)

	m := settingModelFor(4711, "k", "v")
	m.FriendlyName = tfString("Friendly Label")
	m.Type = tfString("bool")

	if err := r.apply(context.Background(), m); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if !fc.applyCalled {
		t.Fatal("ApplyEmployeeSetting was not called")
	}
	if fc.appliedNumber != 4711 {
		t.Errorf("employee number = %d, want 4711", fc.appliedNumber)
	}
	if fc.appliedMeta.FriendlyName != "Friendly Label" || fc.appliedMeta.Type != "bool" {
		t.Errorf("write-only metadata not passed through: %+v", fc.appliedMeta)
	}
}

func TestRequireClient_UnconfiguredProducesDiagnostic(t *testing.T) {
	r := &employeeSettingResource{}
	resp := &resource.CreateResponse{}

	if r.requireClient(&resp.Diagnostics) {
		t.Error("requireClient should report false when the client is nil")
	}
	if !resp.Diagnostics.HasError() {
		t.Error("want a diagnostic")
	}
}

// --- test helpers ---------------------------------------------------------

func tfString(s string) types.String { return types.StringValue(s) }

func settingModelFor(number int64, key, value string) employeeSettingModel {
	return employeeSettingModel{
		EmployeeNumber: types.Int64Value(number),
		Key:            types.StringValue(key),
		Value:          types.StringValue(value),
		FriendlyName:   types.StringValue("Friendly"),
		Type:           types.StringValue("text"),
		AllowNewKey:    types.BoolValue(false),
	}
}

// --- F4: the guard must not claim more than the survey supports -----------

func TestValidateKey_PartialSurveySaysSoInsteadOfClaimingTheKeyExistsNowhere(t *testing.T) {
	fc := &fakeClient{
		settingKeys: []string{"default_work_type"},
		scanPartial: client.SettingKeyScan{
			Keys: []string{"default_work_type"}, Employees: 640, Scanned: 200, Failed: 0,
		},
	}
	r := &employeeSettingResource{client: fc}

	_, err := r.validateKey(context.Background(), "favorite_materials", false)
	if err == nil {
		t.Fatal("an unknown key must still be rejected — settings cannot be deleted")
	}

	msg := err.Error()
	if strings.Contains(msg, "is not in use anywhere on this Kala account") {
		t.Error("only 200 of 640 employees were surveyed; claiming the key exists nowhere is false")
	}
	for _, want := range []string{"200", "640", "allow_new_key"} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic does not mention %q:\n%s", want, msg)
		}
	}
}

func TestValidateKey_CompleteSurveyStillClaimsTheKeyExistsNowhere(t *testing.T) {
	fc := &fakeClient{
		scanPartial: client.SettingKeyScan{
			Keys: []string{"default_work_type"}, Employees: 12, Scanned: 12, Failed: 0,
		},
	}
	r := &employeeSettingResource{client: fc}

	_, err := r.validateKey(context.Background(), "favorite_materials", false)
	if err == nil {
		t.Fatal("want a rejection")
	}
	if !strings.Contains(err.Error(), "is not in use anywhere on this Kala account") {
		t.Errorf("a complete survey supports the stronger claim:\n%s", err.Error())
	}
}

func TestValidateKey_PartialSurveyStillSuggestsTheNearestKey(t *testing.T) {
	fc := &fakeClient{
		scanPartial: client.SettingKeyScan{
			Keys: []string{"default_work_type"}, Employees: 640, Scanned: 200, Failed: 3,
		},
	}
	r := &employeeSettingResource{client: fc}

	_, err := r.validateKey(context.Background(), "default_work_typ", false)
	if err == nil {
		t.Fatal("want a rejection")
	}
	if !strings.Contains(err.Error(), `Did you mean "default_work_type"?`) {
		t.Errorf("a partial survey must still suggest what it did find:\n%s", err.Error())
	}
}
