package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	fwschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/pippiio/terraform-provider-kala/internal/client"
)

func newCaseResource(fi *fakeInternal) *caseResource {
	return &caseResource{clients: &providerClients{Web: &fakeClient{}, Internal: fi}}
}

func caseResSchema(t *testing.T) fwschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewCaseResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func caseResValue(t *testing.T, m caseResourceModel) tftypes.Value {
	t.Helper()
	typ := caseResSchema(t).Type().TerraformType(context.Background())
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
	bl := func(v types.Bool) tftypes.Value {
		switch {
		case v.IsUnknown():
			return tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue)
		case v.IsNull():
			return tftypes.NewValue(tftypes.Bool, nil)
		}
		return tftypes.NewValue(tftypes.Bool, v.ValueBool())
	}
	return tftypes.NewValue(typ.(tftypes.Object), map[string]tftypes.Value{
		"id": i64(m.ID), "number": str(m.Number), "name": str(m.Name),
		"internal_project": bl(m.InternalProject), "customer_number": str(m.CustomerNumber),
		"worker_number": i64(m.WorkerNumber), "address": str(m.Address), "zip": str(m.Zip),
		"contact_phone": str(m.ContactPhone), "archived": bl(m.Archived),
		"customer_id": i64(m.CustomerID), "customer_company": str(m.CustomerCompany),
		"is_finished": bl(m.IsFinished),
	})
}

func caseResPlan(t *testing.T, m caseResourceModel) tfsdk.Plan {
	return tfsdk.Plan{Schema: caseResSchema(t), Raw: caseResValue(t, m)}
}

func caseResState(t *testing.T, m caseResourceModel) tfsdk.State {
	return tfsdk.State{Schema: caseResSchema(t), Raw: caseResValue(t, m)}
}

func emptyCaseResState(t *testing.T) tfsdk.State {
	return tfsdk.State{Schema: caseResSchema(t), Raw: tftypes.Value{}}
}

// planned is a case as it looks before apply: identity unknown.
func plannedCase() caseResourceModel {
	return caseResourceModel{
		ID: types.Int64Unknown(), Number: types.StringUnknown(),
		Name: types.StringValue("Roof job"), InternalProject: types.BoolValue(false),
		CustomerNumber: types.StringValue("KA-1"), WorkerNumber: types.Int64Value(1),
		Address: types.StringValue("Bagshot Row 1"), Zip: types.StringValue("2200"),
		ContactPhone: types.StringNull(), Archived: types.BoolValue(false),
		CustomerID: types.Int64Unknown(), CustomerCompany: types.StringUnknown(),
		IsFinished: types.BoolUnknown(),
	}
}

// existing is a case already in state.
func existingCase() caseResourceModel {
	m := plannedCase()
	m.ID, m.Number = types.Int64Value(4), types.StringValue("KA-4")
	m.CustomerID, m.CustomerCompany = types.Int64Value(1), types.StringValue("Bag End Ltd")
	m.IsFinished = types.BoolValue(false)
	m.ContactPhone = types.StringValue("")
	return m
}

func TestCaseResource_Metadata(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewCaseResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "kala"}, resp)
	if resp.TypeName != "kala_case" {
		t.Errorf("TypeName = %q, want kala_case", resp.TypeName)
	}
}

// Destroy archives rather than only removing state, which is the one thing
// that differs from kala_customer. The schema must say so.
func TestCaseResource_SchemaDocumentsArchiveOnDestroy(t *testing.T) {
	d := strings.ToLower(caseResSchema(t).MarkdownDescription)
	for _, want := range []string{"archives", "irreversible", "import"} {
		if !strings.Contains(d, want) {
			t.Errorf("schema description does not mention %q", want)
		}
	}
}

// The two shapes are different requests upstream, so getting it wrong is
// caught at PLAN time rather than discovered at apply after other resources
// have already been created.
func TestCaseResource_CustomerAndInternalAreMutuallyExclusive(t *testing.T) {
	cases := []struct {
		name     string
		internal bool
		customer types.String
		wantErr  bool
	}{
		{"customer-facing with a customer", false, types.StringValue("KA-1"), false},
		{"internal with no customer", true, types.StringNull(), false},
		{"customer-facing with NO customer", false, types.StringNull(), true},
		{"internal WITH a customer", true, types.StringValue("KA-1"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := plannedCase()
			m.InternalProject = types.BoolValue(tc.internal)
			m.CustomerNumber = tc.customer

			resp := &resource.ValidateConfigResponse{}
			caseCustomerValidator{}.ValidateResource(context.Background(),
				resource.ValidateConfigRequest{
					Config: tfsdk.Config{Schema: caseResSchema(t), Raw: caseResValue(t, m)},
				}, resp)

			if got := resp.Diagnostics.HasError(); got != tc.wantErr {
				t.Errorf("HasError = %t, want %t: %s", got, tc.wantErr, diagsText(resp.Diagnostics))
			}
		})
	}
}

// An unresolved value is not a violation -- a customer created in the same
// apply is unknown at plan time, and rejecting that would make the two
// resources unusable together.
func TestCaseResource_UnknownCustomerIsNotAViolation(t *testing.T) {
	m := plannedCase()
	m.CustomerNumber = types.StringUnknown()

	resp := &resource.ValidateConfigResponse{}
	caseCustomerValidator{}.ValidateResource(context.Background(),
		resource.ValidateConfigRequest{
			Config: tfsdk.Config{Schema: caseResSchema(t), Raw: caseResValue(t, m)},
		}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("an unknown customer_number must not fail validation: %s", diagsText(resp.Diagnostics))
	}
}

func TestCreateCase_RecordsAllocatedIdentity(t *testing.T) {
	fi := newFakeInternal()
	r := newCaseResource(fi)

	resp := &resource.CreateResponse{State: emptyCaseResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: caseResPlan(t, plannedCase())}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if !fi.createCaseCalled {
		t.Fatal("CreateCase was not called")
	}
	if fi.caseIn.CustomerNumber != "KA-1" || fi.caseIn.Name != "Roof job" {
		t.Errorf("CreateCase got %+v", fi.caseIn)
	}

	var got caseResourceModel
	resp.State.Get(context.Background(), &got)
	if got.ID.ValueInt64() != 4 || got.Number.ValueString() != "KA-4" {
		t.Errorf("id=%d number=%q, want 4 / KA-4", got.ID.ValueInt64(), got.Number.ValueString())
	}
}

// the case exists upstream and cannot be deleted, so a failure after
// creation must still leave it in state.
func TestCreateCase_FailureAfterCreationStillRecordsState(t *testing.T) {
	fi := newFakeInternal()
	fi.createCaseErr = errContext("read-back failed")
	r := newCaseResource(fi)

	resp := &resource.CreateResponse{State: emptyCaseResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: caseResPlan(t, plannedCase())}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed create must report an error")
	}
	var got caseResourceModel
	resp.State.Get(context.Background(), &got)
	if got.Number.ValueString() != "KA-4" {
		t.Fatalf("number = %q; the case exists and cannot be deleted, so it must be in state",
			got.Number.ValueString())
	}
}

// Cases are PER-FIELD: only what changed is written, unlike kala_customer.
func TestUpdateCase_WritesOnlyChangedFields(t *testing.T) {
	fi := newFakeInternal()
	fi.cases["KA-4"] = client.CaseDetail{Case: client.Case{
		ID: 4, Number: "KA-4", Name: "Roof job", Address: "Bagshot Row 1", Zip: "2200",
	}}
	r := newCaseResource(fi)

	state := existingCase()
	plan := existingCase()
	plan.Name = types.StringValue("Roof job, phase 2")

	resp := &resource.UpdateResponse{State: caseResState(t, state)}
	r.Update(context.Background(),
		resource.UpdateRequest{Plan: caseResPlan(t, plan), State: caseResState(t, state)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %s", diagsText(resp.Diagnostics))
	}
	if fi.fieldWrites[client.CaseFieldName] != "Roof job, phase 2" {
		t.Errorf("the name change was not written: %v", fi.fieldWrites)
	}
	if _, wrote := fi.fieldWrites[client.CaseFieldAddress]; wrote {
		t.Error("an unchanged address was written; cases are per-field, so omission is safe")
	}
}

func TestUpdateCase_CustomerChangeGoesThroughSetCaseCustomer(t *testing.T) {
	fi := newFakeInternal()
	fi.cases["KA-4"] = client.CaseDetail{Case: client.Case{ID: 4, Number: "KA-4"}}
	r := newCaseResource(fi)

	state := existingCase()
	plan := existingCase()
	plan.InternalProject = types.BoolValue(true)
	plan.CustomerNumber = types.StringNull()

	resp := &resource.UpdateResponse{State: caseResState(t, state)}
	r.Update(context.Background(),
		resource.UpdateRequest{Plan: caseResPlan(t, plan), State: caseResState(t, state)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.customerChanges) == 0 {
		t.Fatal("converting to an internal project must call SetCaseCustomer")
	}
	if fi.customerChanges[0][1] != true {
		t.Errorf("internalProject = %v, want true", fi.customerChanges[0][1])
	}
}

// Cases are the exception to destroy-writes-nothing: destroy ARCHIVES, verifiably, and
// warns that the case remains.
func TestDeleteCase_ArchivesAndWarns(t *testing.T) {
	fi := newFakeInternal()
	fi.cases["KA-4"] = client.CaseDetail{Case: client.Case{ID: 4, Number: "KA-4", Name: "Roof job"}}
	r := newCaseResource(fi)

	resp := &resource.DeleteResponse{State: caseResState(t, existingCase())}
	r.Delete(context.Background(),
		resource.DeleteRequest{State: caseResState(t, existingCase())}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("destroy must not fail: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.archivedCalls) != 1 || !fi.archivedCalls[0] {
		t.Fatalf("destroy must archive the case, got calls %v", fi.archivedCalls)
	}
	warnings := resp.Diagnostics.Warnings()
	if len(warnings) == 0 {
		t.Fatal("destroy must warn that the case remains upstream")
	}
	text := strings.ToLower(warnings[0].Summary() + " " + warnings[0].Detail())
	if !strings.Contains(text, "ka-4") {
		t.Errorf("the warning must name the case left behind: %s", text)
	}
}

// A failed archive is an ERROR, not a warning. Reporting a teardown that did
// not happen is the worst outcome -- the same rule employee deactivation follows.
func TestDeleteCase_FailedArchiveIsAnError(t *testing.T) {
	fi := newFakeInternal()
	fi.archiveErr = errContext("kala refused")
	r := newCaseResource(fi)

	resp := &resource.DeleteResponse{State: caseResState(t, existingCase())}
	r.Delete(context.Background(),
		resource.DeleteRequest{State: caseResState(t, existingCase())}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed archive must fail the destroy rather than warn")
	}
}

func TestReadCase_MissingIsDrift(t *testing.T) {
	fi := newFakeInternal()
	r := newCaseResource(fi)

	resp := &resource.ReadResponse{State: caseResState(t, existingCase())}
	r.Read(context.Background(), resource.ReadRequest{State: caseResState(t, existingCase())}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a missing case is drift, not an error: %s", diagsText(resp.Diagnostics))
	}
	if !resp.State.Raw.IsNull() {
		t.Error("state should have been removed for a case that no longer exists")
	}
}

// Import takes the STRING case number, because that is what writes key on.
func TestImportCase_ByCaseNumber(t *testing.T) {
	r := newCaseResource(newFakeInternal())
	typ := caseResSchema(t).Type().TerraformType(context.Background())
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: caseResSchema(t), Raw: tftypes.NewValue(typ, nil)},
	}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "KA-4"}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %s", diagsText(resp.Diagnostics))
	}
	var got caseResourceModel
	resp.State.Get(context.Background(), &got)
	if got.Number.ValueString() != "KA-4" {
		t.Errorf("imported number = %q, want KA-4", got.Number.ValueString())
	}
}

func errContext(msg string) error { return errors.New(msg) }

func TestProvider_RegistersCaseResource(t *testing.T) {
	var found bool
	for _, f := range New("test")().(*kalaProvider).Resources(context.Background()) {
		resp := &resource.MetadataResponse{}
		f().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "kala"}, resp)
		if resp.TypeName == "kala_case" {
			found = true
		}
	}
	if !found {
		t.Error("kala_case is not registered; an unregistered resource is unreachable")
	}
}

// Drift detection: Read must overwrite state with what Kala holds, or a change
// made in the UI is never noticed.
func TestReadCase_RefreshesFromUpstream(t *testing.T) {
	fi := newFakeInternal()
	fi.cases["KA-4"] = client.CaseDetail{
		Case: client.Case{
			ID: 4, Number: "KA-4", Name: "Renamed In The UI",
			Address: "New Street", Zip: "9999", CustomerCompany: "Bag End Ltd",
			Archived: true,
		},
		CustomerID: 7, IsFinished: true,
	}
	r := newCaseResource(fi)

	resp := &resource.ReadResponse{State: caseResState(t, existingCase())}
	r.Read(context.Background(), resource.ReadRequest{State: caseResState(t, existingCase())}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %s", diagsText(resp.Diagnostics))
	}
	var got caseResourceModel
	resp.State.Get(context.Background(), &got)
	if got.Name.ValueString() != "Renamed In The UI" {
		t.Errorf("name = %q; an upstream change must surface as drift", got.Name.ValueString())
	}
	if !got.Archived.ValueBool() {
		t.Error("archived was not refreshed")
	}
	if got.CustomerID.ValueInt64() != 7 || !got.IsFinished.ValueBool() {
		t.Errorf("computed attributes were not refreshed: %+v", got)
	}
}

// An internal project has no customer, and null is the honest representation --
// zero would read as "customer 0".
func TestReadCase_InternalProjectHasNullCustomerID(t *testing.T) {
	fi := newFakeInternal()
	fi.cases["KA-4"] = client.CaseDetail{
		Case:       client.Case{ID: 4, Number: "KA-4", InternalProject: true},
		CustomerID: 0,
	}
	r := newCaseResource(fi)

	resp := &resource.ReadResponse{State: caseResState(t, existingCase())}
	r.Read(context.Background(), resource.ReadRequest{State: caseResState(t, existingCase())}, resp)

	var got caseResourceModel
	resp.State.Get(context.Background(), &got)
	if !got.CustomerID.IsNull() {
		t.Errorf("customer_id = %v, want null on an internal project", got.CustomerID)
	}
}

func TestReadCase_UpstreamFailureIsAnErrorNotDrift(t *testing.T) {
	fi := newFakeInternal()
	fi.getCaseErr = errContext("upstream exploded")
	r := newCaseResource(fi)

	resp := &resource.ReadResponse{State: caseResState(t, existingCase())}
	r.Read(context.Background(), resource.ReadRequest{State: caseResState(t, existingCase())}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a read failure must be an error, not silent drift")
	}
	if resp.State.Raw.IsNull() {
		t.Error("state must not be removed on a failure that does not prove absence")
	}
}

// Archiving through the attribute rather than by destroying.
func TestUpdateCase_ArchivedToggleIsWritten(t *testing.T) {
	fi := newFakeInternal()
	fi.cases["KA-4"] = client.CaseDetail{Case: client.Case{ID: 4, Number: "KA-4"}}
	r := newCaseResource(fi)

	state := existingCase()
	plan := existingCase()
	plan.Archived = types.BoolValue(true)

	resp := &resource.UpdateResponse{State: caseResState(t, state)}
	r.Update(context.Background(),
		resource.UpdateRequest{Plan: caseResPlan(t, plan), State: caseResState(t, state)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.archivedCalls) != 1 || !fi.archivedCalls[0] {
		t.Errorf("archived toggle was not written: %v", fi.archivedCalls)
	}
}

// A compare-and-swap refusal must tell the operator what to DO about it.
// "kala: server error" would leave them guessing at a race condition.
func TestUpdateCase_ConflictExplainsWhatToDo(t *testing.T) {
	fi := newFakeInternal()
	fi.cases["KA-4"] = client.CaseDetail{Case: client.Case{ID: 4, Number: "KA-4"}}
	fi.caseFieldErr = client.ErrConflict
	r := newCaseResource(fi)

	state := existingCase()
	plan := existingCase()
	plan.Name = types.StringValue("Renamed by Terraform")

	resp := &resource.UpdateResponse{State: caseResState(t, state)}
	r.Update(context.Background(),
		resource.UpdateRequest{Plan: caseResPlan(t, plan), State: caseResState(t, state)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a conflict must fail the update")
	}
	text := strings.ToLower(diagsText(resp.Diagnostics))
	if !strings.Contains(text, "refresh") {
		t.Errorf("a conflict must tell the operator to refresh and re-apply: %s", text)
	}
	if !strings.Contains(text, "was not made") {
		t.Errorf("a conflict must say the change did NOT happen: %s", text)
	}
}

func TestUpdateCase_FailuresPropagate(t *testing.T) {
	for name, setup := range map[string]func(*fakeInternal){
		"field write": func(f *fakeInternal) { f.caseFieldErr = errContext("nope") },
		"customer":    func(f *fakeInternal) { f.setCustomerErr = errContext("nope") },
		"archive":     func(f *fakeInternal) { f.archiveErr = errContext("nope") },
		"read-back":   func(f *fakeInternal) { f.getCaseErr = errContext("nope") },
	} {
		t.Run(name, func(t *testing.T) {
			fi := newFakeInternal()
			fi.cases["KA-4"] = client.CaseDetail{Case: client.Case{ID: 4, Number: "KA-4"}}
			setup(fi)
			r := newCaseResource(fi)

			state := existingCase()
			plan := existingCase()
			plan.Name = types.StringValue("Changed")
			plan.InternalProject = types.BoolValue(true)
			plan.CustomerNumber = types.StringNull()
			plan.Archived = types.BoolValue(true)

			resp := &resource.UpdateResponse{State: caseResState(t, state)}
			r.Update(context.Background(),
				resource.UpdateRequest{Plan: caseResPlan(t, plan), State: caseResState(t, state)}, resp)

			if !resp.Diagnostics.HasError() {
				t.Errorf("a %s failure must propagate", name)
			}
		})
	}
}

// A case already gone is nothing to archive -- a warning, not a failure.
func TestDeleteCase_AlreadyGoneIsAWarning(t *testing.T) {
	fi := newFakeInternal()
	fi.archiveErr = client.ErrNotFound
	r := newCaseResource(fi)

	resp := &resource.DeleteResponse{State: caseResState(t, existingCase())}
	r.Delete(context.Background(), resource.DeleteRequest{State: caseResState(t, existingCase())}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a case that is already gone must not fail destroy: %s", diagsText(resp.Diagnostics))
	}
	if len(resp.Diagnostics.Warnings()) == 0 {
		t.Error("want a warning that there was nothing to archive")
	}
}

func TestImportCase_EmptyIDIsAnError(t *testing.T) {
	r := newCaseResource(newFakeInternal())
	typ := caseResSchema(t).Type().TerraformType(context.Background())
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: caseResSchema(t), Raw: tftypes.NewValue(typ, nil)},
	}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "  "}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("an empty import id must be rejected")
	}
}

func TestCaseResource_WithoutInternalCredentialsIsAClearError(t *testing.T) {
	r := &caseResource{clients: &providerClients{Web: &fakeClient{}}}
	m := existingCase()

	create := &resource.CreateResponse{State: emptyCaseResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: caseResPlan(t, m)}, create)
	read := &resource.ReadResponse{State: caseResState(t, m)}
	r.Read(context.Background(), resource.ReadRequest{State: caseResState(t, m)}, read)
	update := &resource.UpdateResponse{State: caseResState(t, m)}
	r.Update(context.Background(),
		resource.UpdateRequest{Plan: caseResPlan(t, m), State: caseResState(t, m)}, update)
	del := &resource.DeleteResponse{State: caseResState(t, m)}
	r.Delete(context.Background(), resource.DeleteRequest{State: caseResState(t, m)}, del)

	for name, d := range map[string]int{
		"Create": len(create.Diagnostics), "Read": len(read.Diagnostics),
		"Update": len(update.Diagnostics), "Delete": len(del.Diagnostics),
	} {
		if d == 0 {
			t.Errorf("%s without internal credentials must be a clear error", name)
		}
	}
}

func TestCaseResource_ConfigureAndValidators(t *testing.T) {
	r := &caseResource{}
	resp := &resource.ConfigureResponse{}
	r.Configure(context.Background(), resource.ConfigureRequest{}, resp)
	if r.clients != nil || resp.Diagnostics.HasError() {
		t.Error("nil ProviderData is the framework's first call, not an error")
	}

	want := &providerClients{Web: &fakeClient{}, Internal: newFakeInternal()}
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: want}, resp)
	if r.clients != want {
		t.Error("Configure did not store the provider clients")
	}

	if len(NewCaseResource().(*caseResource).ConfigValidators(context.Background())) != 1 {
		t.Error("the customer/internal validator is not registered, so nothing enforces it")
	}
	v := caseCustomerValidator{}
	if v.Description(context.Background()) == "" || v.MarkdownDescription(context.Background()) == "" {
		t.Error("validator descriptions must be non-empty")
	}
}

// A plan or state the framework cannot decode must stop the operation. An
// empty model reaching a write would send blanks to compare-and-swap
// endpoints, which either fails noisily or overwrites the wrong thing.
func TestCaseResource_UndecodablePlanOrStateStopsTheOperation(t *testing.T) {
	fi := newFakeInternal()
	fi.cases["KA-4"] = client.CaseDetail{Case: client.Case{ID: 4, Number: "KA-4"}}
	r := newCaseResource(fi)

	broken := tfsdk.Plan{Schema: caseResSchema(t), Raw: tftypes.Value{}}
	brokenState := tfsdk.State{Schema: caseResSchema(t), Raw: tftypes.Value{}}
	brokenConfig := tfsdk.Config{Schema: caseResSchema(t), Raw: tftypes.Value{}}

	create := &resource.CreateResponse{State: emptyCaseResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: broken}, create)
	read := &resource.ReadResponse{State: emptyCaseResState(t)}
	r.Read(context.Background(), resource.ReadRequest{State: brokenState}, read)
	update := &resource.UpdateResponse{State: emptyCaseResState(t)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: broken, State: brokenState}, update)
	del := &resource.DeleteResponse{State: emptyCaseResState(t)}
	r.Delete(context.Background(), resource.DeleteRequest{State: brokenState}, del)

	validate := &resource.ValidateConfigResponse{}
	caseCustomerValidator{}.ValidateResource(context.Background(),
		resource.ValidateConfigRequest{Config: brokenConfig}, validate)

	for name, n := range map[string]int{
		"Create": len(create.Diagnostics), "Read": len(read.Diagnostics),
		"Update": len(update.Diagnostics), "Delete": len(del.Diagnostics),
		"Validate": len(validate.Diagnostics),
	} {
		if n == 0 {
			t.Errorf("%s accepted an undecodable plan/state/config", name)
		}
	}
	if fi.createCaseCalled || len(fi.fieldWrites) > 0 {
		t.Error("an undecodable plan reached an upstream write")
	}
}

// contact_phone is Optional+Computed, so a configuration that omits it arrives
// UNKNOWN on create. Nothing unknown may reach state, and null is the honest
// substitute -- the next Read fills it in.
func TestCreateCase_UnknownContactPhoneBecomesNull(t *testing.T) {
	fi := newFakeInternal()
	r := newCaseResource(fi)

	plan := plannedCase()
	plan.ContactPhone = types.StringUnknown()

	resp := &resource.CreateResponse{State: emptyCaseResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: caseResPlan(t, plan)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	var got caseResourceModel
	resp.State.Get(context.Background(), &got)
	if got.ContactPhone.IsUnknown() {
		t.Error("an unknown value reached state; the framework treats that as a provider error")
	}
}

// worker_number is Optional so that an IMPORTED case -- for which Kala reports
// no creator -- does not appear to need replacing. Forcing replacement there
// would archive the real case and create a duplicate, which is the single most
// damaging thing this resource could do.
func TestCaseResource_WorkerNumberDoesNotForceReplacement(t *testing.T) {
	attr, ok := caseResSchema(t).Attributes["worker_number"]
	if !ok {
		t.Fatal("worker_number is missing from the schema")
	}
	if attr.IsRequired() {
		t.Error("worker_number must be Optional: an imported case has no value for it")
	}
	if !attr.IsComputed() {
		t.Error("worker_number must be Computed so a null in state does not read as a change")
	}
}

// Kala still requires it to CREATE, so the omission is caught before any write
// rather than surfacing as an upstream rejection.
func TestCreateCase_WithoutWorkerNumberIsRejectedBeforeWriting(t *testing.T) {
	fi := newFakeInternal()
	r := newCaseResource(fi)

	plan := plannedCase()
	plan.WorkerNumber = types.Int64Null()

	resp := &resource.CreateResponse{State: emptyCaseResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: caseResPlan(t, plan)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("creating without worker_number must be an error")
	}
	if fi.createCaseCalled {
		t.Error("the case was created despite having no creator")
	}
}
