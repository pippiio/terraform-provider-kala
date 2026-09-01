package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	fwschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

// Exercises Create/Read/Update/Delete/ImportState directly against constructed
// plan and state objects — enough plumbing to run the real code paths without
// standing up the whole plugin protocol.

func settingSchema(t *testing.T) fwschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewEmployeeSettingResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func settingValue(t *testing.T, m employeeSettingModel) tftypes.Value {
	t.Helper()
	s := settingSchema(t)
	typ := s.Type().TerraformType(context.Background())

	return tftypes.NewValue(typ.(tftypes.Object), map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, settingID(m.EmployeeNumber.ValueInt64(), m.Key.ValueString())),
		"employee_number": tftypes.NewValue(tftypes.Number, m.EmployeeNumber.ValueInt64()),
		"key":             tftypes.NewValue(tftypes.String, m.Key.ValueString()),
		"value":           tftypes.NewValue(tftypes.String, m.Value.ValueString()),
		"friendly_name":   tftypes.NewValue(tftypes.String, m.FriendlyName.ValueString()),
		"type":            tftypes.NewValue(tftypes.String, m.Type.ValueString()),
		"allow_new_key":   tftypes.NewValue(tftypes.Bool, m.AllowNewKey.ValueBool()),
	})
}

func planFor(t *testing.T, m employeeSettingModel) tfsdk.Plan {
	t.Helper()
	s := settingSchema(t)
	return tfsdk.Plan{Schema: s, Raw: settingValue(t, m)}
}

func stateFor(t *testing.T, m employeeSettingModel) tfsdk.State {
	t.Helper()
	s := settingSchema(t)
	return tfsdk.State{Schema: s, Raw: settingValue(t, m)}
}

func emptyState(t *testing.T) tfsdk.State {
	t.Helper()
	s := settingSchema(t)
	return tfsdk.State{Schema: s, Raw: tftypes.Value{}}
}

func diagsText(d diag.Diagnostics) string {
	var b strings.Builder
	for _, x := range d {
		b.WriteString(x.Summary() + " | " + x.Detail() + "\n")
	}
	return b.String()
}

// --- Create ---------------------------------------------------------------

func TestCreate_WritesVerifiesAndSetsState(t *testing.T) {
	fc := &fakeClient{
		settingKeys: []string{"default_work_type"},
		getEmployee: client.Employee{Number: 4711, Settings: []client.Setting{
			{Key: "default_work_type", Value: "montage"},
		}},
	}
	r := newSettingResource(fc)

	m := settingModelFor(4711, "default_work_type", "montage")
	resp := &resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: planFor(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if !fc.applyCalled {
		t.Error("the setting was never written")
	}
	if fc.appliedSet.Value != "montage" {
		t.Errorf("wrote value %q, want montage", fc.appliedSet.Value)
	}
}

func TestCreate_UnknownKeyBlocksBeforeAnyWrite(t *testing.T) {
	fc := &fakeClient{settingKeys: []string{"default_work_type"}}
	r := newSettingResource(fc)

	m := settingModelFor(4711, "totally_new_key", "v")
	resp := &resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: planFor(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error for an unknown key")
	}
	// Crucially: nothing was written. A blocked key must not leave a partial
	// record, because settings cannot be deleted.
	if fc.applyCalled {
		t.Error("a rejected key must not be written — settings cannot be deleted")
	}
}

func TestCreate_AllowNewKeyPermitsTheWrite(t *testing.T) {
	fc := &fakeClient{
		settingKeys: []string{"other"},
		getEmployee: client.Employee{Number: 1, Settings: []client.Setting{{Key: "brand_new", Value: "v"}}},
	}
	r := newSettingResource(fc)

	m := settingModelFor(1, "brand_new", "v")
	m.AllowNewKey = tfBool(true)

	resp := &resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: planFor(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("allow_new_key should permit the write: %s", diagsText(resp.Diagnostics))
	}
	if !fc.applyCalled {
		t.Error("the setting should have been written")
	}
}

func TestCreate_FailedVerificationIsAnError(t *testing.T) {
	fc := &fakeClient{
		settingKeys: []string{"k"},
		// Read-back returns a different value than was written.
		getEmployee: client.Employee{Number: 1, Settings: []client.Setting{{Key: "k", Value: "wrong"}}},
	}
	r := newSettingResource(fc)

	resp := &resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: planFor(t, settingModelFor(1, "k", "v"))}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an unverified write must fail")
	}
	if !strings.Contains(diagsText(resp.Diagnostics), "verified") {
		t.Errorf("diagnostic should say the write was unverified: %s", diagsText(resp.Diagnostics))
	}
}

// --- Read -----------------------------------------------------------------

func TestRead_RefreshesValueButNotWriteOnlyFields(t *testing.T) {
	// The API changed the value; friendly_name is never returned by any read.
	fc := &fakeClient{getEmployee: client.Employee{
		Number:   4711,
		Settings: []client.Setting{{Key: "k", Value: "changed-upstream"}},
	}}
	r := newSettingResource(fc)

	prior := settingModelFor(4711, "k", "original")
	prior.FriendlyName = tfString("My Label")

	resp := &resource.ReadResponse{State: stateFor(t, prior)}
	r.Read(context.Background(), resource.ReadRequest{State: stateFor(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %s", diagsText(resp.Diagnostics))
	}

	var got employeeSettingModel
	resp.State.Get(context.Background(), &got)

	if got.Value.ValueString() != "changed-upstream" {
		t.Errorf("value = %q, want the refreshed upstream value", got.Value.ValueString())
	}
	// This is risk R1: if Read nulled friendly_name, every plan would show a
	// diff that the user could never resolve.
	if got.FriendlyName.ValueString() != "My Label" {
		t.Errorf("friendly_name = %q, want it preserved from prior state — "+
			"refreshing it would cause a perpetual diff", got.FriendlyName.ValueString())
	}
}

func TestRead_MissingSettingIsDriftNotFailure(t *testing.T) {
	// The setting was removed outside Terraform.
	fc := &fakeClient{getEmployee: client.Employee{Number: 1, Settings: []client.Setting{}}}
	r := newSettingResource(fc)

	prior := settingModelFor(1, "k", "v")
	resp := &resource.ReadResponse{State: stateFor(t, prior)}
	r.Read(context.Background(), resource.ReadRequest{State: stateFor(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a missing setting is drift, not an error: %s", diagsText(resp.Diagnostics))
	}
	if !resp.State.Raw.IsNull() {
		t.Error("state should have been removed so the next apply re-creates it")
	}
}

func TestRead_MissingEmployeeIsDriftNotFailure(t *testing.T) {
	fc := &fakeClient{getErr: client.ErrNotFound}
	r := newSettingResource(fc)

	prior := settingModelFor(1, "k", "v")
	resp := &resource.ReadResponse{State: stateFor(t, prior)}
	r.Read(context.Background(), resource.ReadRequest{State: stateFor(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a vanished employee is drift (TF1.2): %s", diagsText(resp.Diagnostics))
	}
	if !resp.State.Raw.IsNull() {
		t.Error("state should have been removed")
	}
}

// --- Update ---------------------------------------------------------------

func TestUpdate_WritesAndVerifies(t *testing.T) {
	fc := &fakeClient{getEmployee: client.Employee{
		Number: 1, Settings: []client.Setting{{Key: "k", Value: "new"}},
	}}
	r := newSettingResource(fc)

	m := settingModelFor(1, "k", "new")
	resp := &resource.UpdateResponse{State: emptyState(t)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: planFor(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %s", diagsText(resp.Diagnostics))
	}
	if fc.appliedSet.Value != "new" {
		t.Errorf("wrote %q, want new", fc.appliedSet.Value)
	}
}

// Update must not re-run the unknown-key check: the key cannot change here
// (it forces replacement), so listing keys would be a wasted call.
func TestUpdate_DoesNotRevalidateTheKey(t *testing.T) {
	fc := &fakeClient{
		keysErr:     errKeysMustNotBeListed,
		getEmployee: client.Employee{Number: 1, Settings: []client.Setting{{Key: "k", Value: "v"}}},
	}
	r := newSettingResource(fc)

	resp := &resource.UpdateResponse{State: emptyState(t)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: planFor(t, settingModelFor(1, "k", "v"))}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("update should not list setting keys: %s", diagsText(resp.Diagnostics))
	}
}

// --- Delete ---------------------------------------------------------------

// ADR-001 / TF1.1: destroy writes nothing and must warn, naming what remains.
func TestDelete_WritesNothingAndWarnsWithSpecifics(t *testing.T) {
	fc := &fakeClient{}
	r := newSettingResource(fc)

	prior := settingModelFor(4711, "default_work_type", "montage")
	resp := &resource.DeleteResponse{State: stateFor(t, prior)}
	r.Delete(context.Background(), resource.DeleteRequest{State: stateFor(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("delete should not error: %s", diagsText(resp.Diagnostics))
	}
	if fc.applyCalled {
		t.Error("delete must not write upstream (ARCH1.6)")
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("delete must warn — a silent destroy implies a cleanup that did not happen")
	}

	text := diagsText(resp.Diagnostics)
	for _, want := range []string{"default_work_type", "4711", "montage"} {
		if !strings.Contains(text, want) {
			t.Errorf("warning must name %q so the operator can clean up manually; got: %s", want, text)
		}
	}
}

// --- Import ---------------------------------------------------------------

func TestImportState_ParsesIDAndWarnsAboutFriendlyName(t *testing.T) {
	r := newSettingResource(&fakeClient{})

	resp := &resource.ImportStateResponse{State: emptyStateWithNullObject(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "4711:default_work_type"}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %s", diagsText(resp.Diagnostics))
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("import must warn that friendly_name cannot be recovered")
	}
	if !strings.Contains(diagsText(resp.Diagnostics), "friendly_name") {
		t.Error("the warning must name friendly_name")
	}
}

func TestImportState_MalformedIDErrors(t *testing.T) {
	r := newSettingResource(&fakeClient{})

	resp := &resource.ImportStateResponse{State: emptyStateWithNullObject(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "nonsense"}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error for a malformed import ID")
	}
	if !strings.Contains(diagsText(resp.Diagnostics), "employee_number") {
		t.Error("the error should show the expected ID format")
	}
}

// --- helpers --------------------------------------------------------------

var errKeysMustNotBeListed = errors.New("ScanSettingKeys must not be called during update")

func tfBool(b bool) types.Bool { return types.BoolValue(b) }

// emptyStateWithNullObject gives ImportState a state whose Raw is a null object
// of the right type, which is what the framework hands it.
func emptyStateWithNullObject(t *testing.T) tfsdk.State {
	t.Helper()
	s := settingSchema(t)
	typ := s.Type().TerraformType(context.Background())
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(typ, nil)}
}

// --- remaining branches ---------------------------------------------------

func TestSettingResource_Configure(t *testing.T) {
	r := NewEmployeeSettingResource().(*employeeSettingResource)

	// nil ProviderData is normal during early plan walks.
	resp := &resource.ConfigureResponse{}
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("nil ProviderData must not error: %s", diagsText(resp.Diagnostics))
	}

	// Wrong type is a provider bug and must say so.
	resp = &resource.ConfigureResponse{}
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: 42}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("want a diagnostic for the wrong ProviderData type")
	}

	// A real client is stored.
	resp = &resource.ConfigureResponse{}
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: &providerClients{Web: &fakeClient{}}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %s", diagsText(resp.Diagnostics))
	}
	if r.client == nil {
		t.Error("client was not stored")
	}
}

func TestCreate_UnconfiguredClientErrors(t *testing.T) {
	r := &employeeSettingResource{}
	resp := &resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: planFor(t, settingModelFor(1, "k", "v"))}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("want a diagnostic when the client is nil")
	}
}

func TestRead_UnconfiguredClientErrors(t *testing.T) {
	r := &employeeSettingResource{}
	prior := settingModelFor(1, "k", "v")
	resp := &resource.ReadResponse{State: stateFor(t, prior)}
	r.Read(context.Background(), resource.ReadRequest{State: stateFor(t, prior)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("want a diagnostic when the client is nil")
	}
}

func TestUpdate_UnconfiguredClientErrors(t *testing.T) {
	r := &employeeSettingResource{}
	resp := &resource.UpdateResponse{State: emptyState(t)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: planFor(t, settingModelFor(1, "k", "v"))}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("want a diagnostic when the client is nil")
	}
}

func TestUpdate_WriteFailurePropagates(t *testing.T) {
	fc := &fakeClient{applyErr: errors.New("write rejected")}
	r := newSettingResource(fc)

	resp := &resource.UpdateResponse{State: emptyState(t)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: planFor(t, settingModelFor(1, "k", "v"))}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want the write error to surface")
	}
	if !strings.Contains(diagsText(resp.Diagnostics), "write rejected") {
		t.Errorf("diagnostic should carry the cause: %s", diagsText(resp.Diagnostics))
	}
}

func TestUpdate_FailedVerificationIsAnError(t *testing.T) {
	fc := &fakeClient{getEmployee: client.Employee{Number: 1, Settings: []client.Setting{{Key: "k", Value: "stale"}}}}
	r := newSettingResource(fc)

	resp := &resource.UpdateResponse{State: emptyState(t)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: planFor(t, settingModelFor(1, "k", "fresh"))}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("an unverified update must fail")
	}
}

func TestCreate_WriteFailurePropagates(t *testing.T) {
	fc := &fakeClient{settingKeys: []string{"k"}, applyErr: errors.New("upstream refused")}
	r := newSettingResource(fc)

	resp := &resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: planFor(t, settingModelFor(1, "k", "v"))}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want the write error to surface")
	}
}

func TestRead_UpstreamErrorIsNotSilentlySwallowed(t *testing.T) {
	fc := &fakeClient{getErr: client.ErrUnauthorized}
	r := newSettingResource(fc)

	prior := settingModelFor(1, "k", "v")
	resp := &resource.ReadResponse{State: stateFor(t, prior)}
	r.Read(context.Background(), resource.ReadRequest{State: stateFor(t, prior)}, resp)

	// A 401 is a real failure, not drift — it must not silently drop the resource.
	if !resp.Diagnostics.HasError() {
		t.Error("an auth failure must error rather than be treated as drift")
	}
}
