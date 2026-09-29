package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	fwschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/pippiio/terraform-provider-kala/internal/client"
)

func newCaseAccessResource(fi *fakeInternal) *caseAccessResource {
	return &caseAccessResource{clients: &providerClients{Web: &fakeClient{}, Internal: fi}}
}

func accessResSchema(t *testing.T) fwschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewCaseAccessResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	return resp.Schema
}

func accessResValue(t *testing.T, m caseAccessResourceModel) tftypes.Value {
	t.Helper()
	typ := accessResSchema(t).Type().TerraformType(context.Background())
	str := func(v types.String) tftypes.Value {
		switch {
		case v.IsUnknown():
			return tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
		case v.IsNull():
			return tftypes.NewValue(tftypes.String, nil)
		}
		return tftypes.NewValue(tftypes.String, v.ValueString())
	}
	i64 := func(v types.Int64) tftypes.Value {
		switch {
		case v.IsUnknown():
			return tftypes.NewValue(tftypes.Number, tftypes.UnknownValue)
		case v.IsNull():
			return tftypes.NewValue(tftypes.Number, nil)
		}
		return tftypes.NewValue(tftypes.Number, v.ValueInt64())
	}
	return tftypes.NewValue(typ.(tftypes.Object), map[string]tftypes.Value{
		"id": str(m.ID), "case_number": str(m.CaseNumber),
		"employee_number": i64(m.EmployeeNumber), "case_id": i64(m.CaseID),
	})
}

func plannedAccess() caseAccessResourceModel {
	return caseAccessResourceModel{
		ID: types.StringUnknown(), CaseNumber: types.StringValue("KA-2"),
		EmployeeNumber: types.Int64Value(23), CaseID: types.Int64Unknown(),
	}
}

func existingAccess() caseAccessResourceModel {
	return caseAccessResourceModel{
		ID: types.StringValue("KA-2/23"), CaseNumber: types.StringValue("KA-2"),
		EmployeeNumber: types.Int64Value(23), CaseID: types.Int64Value(2),
	}
}

// fakeKA2 is case KA-2 as found live: restricted, employee 1 granted.
func fakeKA2(granted ...int64) *fakeInternal {
	fi := newFakeInternal()
	fi.caseAccess = map[string]client.CaseAccess{
		"KA-2": {CaseID: 2, Restricted: true, Granted: granted},
	}
	return fi
}

func createAccess(t *testing.T, fi *fakeInternal, m caseAccessResourceModel) *resource.CreateResponse {
	t.Helper()
	sch := accessResSchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(context.Background()), nil)}}
	newCaseAccessResource(fi).Create(context.Background(),
		resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: accessResValue(t, m)}}, resp)
	return resp
}

func readAccess(t *testing.T, fi *fakeInternal, m caseAccessResourceModel) *resource.ReadResponse {
	t.Helper()
	sch := accessResSchema(t)
	st := tfsdk.State{Schema: sch, Raw: accessResValue(t, m)}
	resp := &resource.ReadResponse{State: st}
	newCaseAccessResource(fi).Read(context.Background(), resource.ReadRequest{State: st}, resp)
	return resp
}

func deleteAccess(t *testing.T, fi *fakeInternal, m caseAccessResourceModel) *resource.DeleteResponse {
	t.Helper()
	sch := accessResSchema(t)
	st := tfsdk.State{Schema: sch, Raw: accessResValue(t, m)}
	resp := &resource.DeleteResponse{State: st}
	newCaseAccessResource(fi).Delete(context.Background(), resource.DeleteRequest{State: st}, resp)
	return resp
}

func stateOf(t *testing.T, st tfsdk.State) caseAccessResourceModel {
	t.Helper()
	var m caseAccessResourceModel
	if diags := st.Get(context.Background(), &m); diags.HasError() {
		t.Fatalf("reading state: %v", diags)
	}
	return m
}

func TestCaseAccessResource_Metadata(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewCaseAccessResource().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "kala"}, resp)
	if resp.TypeName != "kala_case_access" {
		t.Errorf("TypeName = %q, want kala_case_access", resp.TypeName)
	}
}

// A grant has nothing the provider can change in place: both identifying
// attributes force replacement, which is also what makes revoke-then-grant the
// natural shape of a change.
func TestCaseAccessResource_IdentityForcesReplacement(t *testing.T) {
	sch := accessResSchema(t)
	if a, ok := sch.Attributes["case_number"].(fwschema.StringAttribute); !ok || len(a.PlanModifiers) == 0 {
		t.Error("case_number must RequiresReplace")
	}
	if a, ok := sch.Attributes["employee_number"].(fwschema.Int64Attribute); !ok || len(a.PlanModifiers) == 0 {
		t.Error("employee_number must RequiresReplace")
	}
}

func TestCaseAccessResource_DescriptionSaysDestroyRevokesAndDriftRestores(t *testing.T) {
	d := strings.ToLower(accessResSchema(t).MarkdownDescription)
	for _, want := range []string{"revoke", "restore", "roles"} {
		if !strings.Contains(d, want) {
			t.Errorf("the resource description must mention %q; got: %s", want, d)
		}
	}
}

// --- Create -------------------------------------------------------------------

func TestCaseAccessResource_CreateGrants(t *testing.T) {
	fi := fakeKA2(1)
	resp := createAccess(t, fi, plannedAccess())
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", resp.Diagnostics)
	}
	if len(fi.setCaseAccessCalls) != 1 || fi.setCaseAccessCalls[0] != (setCaseAccessCall{"KA-2", 23, true}) {
		t.Fatalf("want one grant of 23 on KA-2; got %v", fi.setCaseAccessCalls)
	}
	m := stateOf(t, resp.State)
	if m.ID.ValueString() != "KA-2/23" || m.CaseID.ValueInt64() != 2 {
		t.Errorf("state id/case_id = %q/%d, want KA-2/23 / 2", m.ID.ValueString(), m.CaseID.ValueInt64())
	}
}

// Adopting must not write: GrantAccess sends role:[] and could clear roles
// given in the Kala UI.
func TestCaseAccessResource_CreateAdoptsAnExistingGrantWithoutWriting(t *testing.T) {
	fi := fakeKA2(1, 23)
	resp := createAccess(t, fi, plannedAccess())
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", resp.Diagnostics)
	}
	if len(fi.setCaseAccessCalls) != 0 {
		t.Errorf("an existing grant must be adopted without writing; got %v", fi.setCaseAccessCalls)
	}
	if stateOf(t, resp.State).ID.ValueString() != "KA-2/23" {
		t.Error("the adopted grant must be recorded in state")
	}
}

func TestCaseAccessResource_CreateOnUnrestrictedCaseWarns(t *testing.T) {
	fi := fakeKA2(1)
	a := fi.caseAccess["KA-2"]
	a.Restricted = false
	fi.caseAccess["KA-2"] = a

	resp := createAccess(t, fi, plannedAccess())
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", resp.Diagnostics)
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("granting on an unrestricted case must warn that the grant has no effect until it is restricted")
	}
}

func TestCaseAccessResource_CreateOnMissingCaseErrorsWithoutWriting(t *testing.T) {
	fi := newFakeInternal()
	resp := createAccess(t, fi, plannedAccess())
	if !resp.Diagnostics.HasError() {
		t.Fatal("a missing case must error")
	}
	var onCaseNumber bool
	for _, d := range resp.Diagnostics.Errors() {
		if dp, ok := d.(interface{ Path() path.Path }); ok && dp.Path().Equal(path.Root("case_number")) {
			onCaseNumber = true
		}
	}
	if !onCaseNumber {
		t.Errorf("the error must point at case_number; got %v", resp.Diagnostics)
	}
	if len(fi.setCaseAccessCalls) != 0 {
		t.Errorf("nothing may be written for a missing case; got %v", fi.setCaseAccessCalls)
	}
}

func TestCaseAccessResource_CreateSurfacesAFailedWrite(t *testing.T) {
	fi := fakeKA2(1)
	fi.setCaseAccessErr = errors.New("kala rejected the request: Medarbejderen findes ikke")
	resp := createAccess(t, fi, plannedAccess())
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "Medarbejderen findes ikke") {
		t.Fatalf("want Kala's message in the error; got %v", resp.Diagnostics)
	}
}

// --- Read: removed outside Terraform is drift, and the plan restores it -----

func TestCaseAccessResource_ReadKeepsAPresentGrant(t *testing.T) {
	resp := readAccess(t, fakeKA2(1, 23), existingAccess())
	if resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
		t.Fatalf("a present grant must stay in state; diags %v", resp.Diagnostics)
	}
}

// Decision D3: revoked in the Kala UI -> removed from state -> the next plan
// proposes creating it again, which restores the access.
func TestCaseAccessResource_ReadRemovesARevokedGrant(t *testing.T) {
	resp := readAccess(t, fakeKA2(1), existingAccess())
	if resp.Diagnostics.HasError() {
		t.Fatalf("drift is not an error: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a revoked grant must be removed from state so the plan proposes restoring it")
	}
}

func TestCaseAccessResource_ReadRemovesAGrantOnAMissingCase(t *testing.T) {
	resp := readAccess(t, newFakeInternal(), existingAccess())
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Errorf("a missing case is drift (TF1.2); diags %v", resp.Diagnostics)
	}
}

func TestCaseAccessResource_ReadErrorsOnOtherFailures(t *testing.T) {
	fi := fakeKA2(1, 23)
	fi.getCaseAccessErr = errors.New("kala: server error")
	resp := readAccess(t, fi, existingAccess())
	if !resp.Diagnostics.HasError() {
		t.Error("a failure that is not absence must error, not remove the resource")
	}
}

// --- Delete revokes -------------------------------------------------------------

func TestCaseAccessResource_DeleteRevokes(t *testing.T) {
	fi := fakeKA2(1, 23)
	resp := deleteAccess(t, fi, existingAccess())
	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete: %v", resp.Diagnostics)
	}
	if len(fi.setCaseAccessCalls) != 1 || fi.setCaseAccessCalls[0] != (setCaseAccessCall{"KA-2", 23, false}) {
		t.Fatalf("want one revoke of 23 on KA-2; got %v", fi.setCaseAccessCalls)
	}
}

func TestCaseAccessResource_DeleteOfAnAlreadyRevokedGrantWritesNothing(t *testing.T) {
	fi := fakeKA2(1)
	resp := deleteAccess(t, fi, existingAccess())
	if resp.Diagnostics.HasError() || len(fi.setCaseAccessCalls) != 0 {
		t.Errorf("nothing to revoke; diags %v, calls %v", resp.Diagnostics, fi.setCaseAccessCalls)
	}
}

func TestCaseAccessResource_DeleteOnAMissingCaseWritesNothing(t *testing.T) {
	fi := newFakeInternal()
	resp := deleteAccess(t, fi, existingAccess())
	if resp.Diagnostics.HasError() || len(fi.setCaseAccessCalls) != 0 {
		t.Errorf("a missing case has no grant to revoke; diags %v, calls %v", resp.Diagnostics, fi.setCaseAccessCalls)
	}
}

func TestCaseAccessResource_DeleteSurfacesAFailedRevoke(t *testing.T) {
	fi := fakeKA2(1, 23)
	fi.setCaseAccessErr = errors.New("kala: revoking was not confirmed")
	if resp := deleteAccess(t, fi, existingAccess()); !resp.Diagnostics.HasError() {
		t.Error("a revoke that did not land must fail the destroy, or access would silently remain")
	}
}

// --- Import -------------------------------------------------------------------

func TestCaseAccessResource_ImportByCaseAndEmployee(t *testing.T) {
	sch := accessResSchema(t)
	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(context.Background()), nil)}}
	newCaseAccessResource(fakeKA2(23)).ImportState(context.Background(), resource.ImportStateRequest{ID: "KA-2/23"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("ImportState: %v", resp.Diagnostics)
	}
	var nr types.Int64
	var cn types.String
	resp.State.GetAttribute(context.Background(), path.Root("employee_number"), &nr)
	resp.State.GetAttribute(context.Background(), path.Root("case_number"), &cn)
	if nr.ValueInt64() != 23 || cn.ValueString() != "KA-2" {
		t.Errorf("imported case_number/employee_number = %q/%d, want KA-2/23", cn.ValueString(), nr.ValueInt64())
	}
}

func TestCaseAccessResource_ImportRejectsMalformedIDs(t *testing.T) {
	for _, id := range []string{"KA-2", "KA-2/", "/23", "KA-2/x", "KA-2/0"} {
		sch := accessResSchema(t)
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(context.Background()), nil)}}
		newCaseAccessResource(fakeKA2()).ImportState(context.Background(), resource.ImportStateRequest{ID: id}, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("import ID %q must be rejected", id)
		}
	}
}

func TestProvider_RegistersCaseAccessResource(t *testing.T) {
	for _, f := range (&kalaProvider{}).Resources(context.Background()) {
		var resp resource.MetadataResponse
		f().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "kala"}, &resp)
		if resp.TypeName == "kala_case_access" {
			return
		}
	}
	t.Error("kala_case_access is not registered in the provider's Resources")
}
