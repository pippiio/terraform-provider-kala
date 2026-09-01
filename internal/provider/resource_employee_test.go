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

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

// fakeInternal doubles the internal app API and records every call.
type fakeInternal struct {
	workers map[int64]client.Worker

	createCalled  bool
	created       client.NewWorker
	createErr     error
	setValidated  []setValidatedCall
	setValidErr   error
	getWorkerErr  error
	listWorkerErr error
}

type setValidatedCall struct {
	number int64
	value  bool
}

func newFakeInternal(workers ...client.Worker) *fakeInternal {
	m := map[int64]client.Worker{}
	for _, w := range workers {
		m[w.WorkerNr] = w
	}
	return &fakeInternal{workers: m}
}

func (f *fakeInternal) ListWorkers(context.Context) ([]client.Worker, error) {
	if f.listWorkerErr != nil {
		return nil, f.listWorkerErr
	}
	out := make([]client.Worker, 0, len(f.workers))
	for _, w := range f.workers {
		out = append(out, w)
	}
	return out, nil
}

func (f *fakeInternal) GetWorker(_ context.Context, nr int64) (client.Worker, error) {
	if f.getWorkerErr != nil {
		return client.Worker{}, f.getWorkerErr
	}
	w, ok := f.workers[nr]
	if !ok {
		return client.Worker{}, client.ErrNotFound
	}
	return w, nil
}

func (f *fakeInternal) SetWorkerValidated(_ context.Context, nr int64, v bool) error {
	f.setValidated = append(f.setValidated, setValidatedCall{nr, v})
	if f.setValidErr != nil {
		return f.setValidErr
	}
	if w, ok := f.workers[nr]; ok {
		w.IsValidated = v
		f.workers[nr] = w
	}
	return nil
}

func (f *fakeInternal) CreateWorker(_ context.Context, in client.NewWorker) (client.Worker, error) {
	f.createCalled, f.created = true, in
	if f.createErr != nil {
		return client.Worker{}, f.createErr
	}
	w := client.Worker{WorkerNr: in.Number, Name: in.Name, IsValidated: true}
	f.workers[in.Number] = w
	return w, nil
}

var _ client.InternalClient = (*fakeInternal)(nil)

func newEmployeeResource(fi *fakeInternal) *employeeResource {
	return &employeeResource{clients: &providerClients{Web: &fakeClient{}, Internal: fi}}
}

// --- plan/state plumbing --------------------------------------------------

func employeeSchema(t *testing.T) fwschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewEmployeeResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func employeeValue(t *testing.T, m employeeResourceModel) tftypes.Value {
	t.Helper()
	typ := employeeSchema(t).Type().TerraformType(context.Background())
	str := func(v types.String) any {
		if v.IsNull() {
			return nil
		}
		return v.ValueString()
	}
	return tftypes.NewValue(typ.(tftypes.Object), map[string]tftypes.Value{
		"employee_number": tftypes.NewValue(tftypes.Number, m.EmployeeNumber.ValueInt64()),
		"name":            tftypes.NewValue(tftypes.String, m.Name.ValueString()),
		"email":           tftypes.NewValue(tftypes.String, m.Email.ValueString()),
		"active":          tftypes.NewValue(tftypes.Bool, m.Active.ValueBool()),
		"title":           tftypes.NewValue(tftypes.String, str(m.Title)),
		"phone":           tftypes.NewValue(tftypes.String, str(m.Phone)),
		"department":      tftypes.NewValue(tftypes.String, str(m.Department)),
		"initials":        tftypes.NewValue(tftypes.String, str(m.Initials)),
		"adopted":         tftypes.NewValue(tftypes.Bool, m.Adopted.ValueBool()),
	})
}

func employeePlan(t *testing.T, m employeeResourceModel) tfsdk.Plan {
	t.Helper()
	return tfsdk.Plan{Schema: employeeSchema(t), Raw: employeeValue(t, m)}
}

func employeeState(t *testing.T, m employeeResourceModel) tfsdk.State {
	t.Helper()
	return tfsdk.State{Schema: employeeSchema(t), Raw: employeeValue(t, m)}
}

func emptyEmployeeState(t *testing.T) tfsdk.State {
	t.Helper()
	return tfsdk.State{Schema: employeeSchema(t), Raw: tftypes.Value{}}
}

func employeeModelFor(number int64, name, email string, active bool) employeeResourceModel {
	return employeeResourceModel{
		EmployeeNumber: types.Int64Value(number),
		Name:           types.StringValue(name),
		Email:          types.StringValue(email),
		Active:         types.BoolValue(active),
		Title:          types.StringValue(""),
		Phone:          types.StringValue(""),
		Department:     types.StringValue(""),
		Initials:       types.StringValue(""),
		Adopted:        types.BoolValue(false),
	}
}

// --- schema ---------------------------------------------------------------

func TestEmployeeResource_Metadata(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewEmployeeResource().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "kala"}, resp)
	if resp.TypeName != "kala_employee" {
		t.Errorf("TypeName = %q, want kala_employee", resp.TypeName)
	}
}

func TestEmployeeResource_SchemaDocumentsIrreversibleDestroy(t *testing.T) {
	s := employeeSchema(t)

	desc := strings.ToLower(s.GetMarkdownDescription())
	if !strings.Contains(desc, "deactivat") {
		t.Error("the schema must say destroy deactivates rather than deletes")
	}
	// The adoption behaviour is surprising; users must be told before they hit it.
	if !strings.Contains(desc, "adopt") {
		t.Error("the schema must document that an existing employee_number is adopted")
	}

	email := s.Attributes["email"]
	if !strings.Contains(strings.ToLower(email.GetMarkdownDescription()), "write-only") {
		t.Error("email must be documented as write-only")
	}
}

// --- Create: new employee -------------------------------------------------

func TestCreateEmployee_ProvisionsWhenNumberIsFree(t *testing.T) {
	fi := newFakeInternal()
	r := newEmployeeResource(fi)

	m := employeeModelFor(10, "New Person", "new@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if !fi.createCalled {
		t.Fatal("CreateWorker was not called")
	}
	if fi.created.Number != 10 || fi.created.Email != "new@example.com" || fi.created.Name != "New Person" {
		t.Errorf("created with %+v", fi.created)
	}

	var got employeeResourceModel
	resp.State.Get(context.Background(), &got)
	if got.Adopted.ValueBool() {
		t.Error("adopted should be false for a genuinely new employee")
	}
}

func TestCreateEmployee_NewButRequestedInactiveIsDeactivated(t *testing.T) {
	fi := newFakeInternal()
	r := newEmployeeResource(fi)

	m := employeeModelFor(11, "Dormant", "d@example.com", false)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	// Kala creates employees active, so active=false needs an explicit write.
	if len(fi.setValidated) != 1 || fi.setValidated[0].value {
		t.Errorf("want one SetValidated(false) call, got %+v", fi.setValidated)
	}
}

// --- Create: adoption (the requested behaviour) ---------------------------

func TestCreateEmployee_ReactivatesExistingInactiveEmployee(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "Returning Person", IsValidated: false})
	r := newEmployeeResource(fi)

	m := employeeModelFor(3, "Returning Person", "r@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if fi.createCalled {
		t.Error("must not call CreateWorker for a number already in use")
	}
	if len(fi.setValidated) != 1 || !fi.setValidated[0].value || fi.setValidated[0].number != 3 {
		t.Errorf("want SetValidated(3, true), got %+v", fi.setValidated)
	}

	var got employeeResourceModel
	resp.State.Get(context.Background(), &got)
	if !got.Adopted.ValueBool() {
		t.Error("adopted should be true")
	}
	if !got.Active.ValueBool() {
		t.Error("the employee should be active after reactivation")
	}

	// The user must know they inherited a person rather than creating one.
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("reactivation must warn")
	}
	if !strings.Contains(diagsText(resp.Diagnostics), "Reactivated") {
		t.Errorf("warning should say the employee was reactivated: %s", diagsText(resp.Diagnostics))
	}
}

func TestCreateEmployee_AdoptsAlreadyActiveEmployeeWithoutWriting(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "Existing", IsValidated: true})
	r := newEmployeeResource(fi)

	m := employeeModelFor(3, "Existing", "e@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if fi.createCalled {
		t.Error("must not create")
	}
	// Already in the desired state — no write should occur at all.
	if len(fi.setValidated) != 0 {
		t.Errorf("no activation write expected, got %+v", fi.setValidated)
	}
}

func TestCreateEmployee_AdoptedNameMismatchWarns(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "Actual Name", IsValidated: true})
	r := newEmployeeResource(fi)

	m := employeeModelFor(3, "Configured Name", "e@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	text := diagsText(resp.Diagnostics)
	if !strings.Contains(text, "Actual Name") || !strings.Contains(text, "Configured Name") {
		t.Errorf("warning should show both names, got: %s", text)
	}
	if !strings.Contains(text, "rename") {
		t.Errorf("warning should explain that renaming is impossible, got: %s", text)
	}
}

func TestCreateEmployee_AdoptActiveButWantInactiveDeactivates(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	r := newEmployeeResource(fi)

	m := employeeModelFor(3, "X", "e@example.com", false)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.setValidated) != 1 || fi.setValidated[0].value {
		t.Errorf("want SetValidated(false), got %+v", fi.setValidated)
	}
}

func TestCreateEmployee_ReactivationFailurePropagates(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: false})
	fi.setValidErr = errors.New("upstream refused")
	r := newEmployeeResource(fi)

	m := employeeModelFor(3, "X", "e@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed reactivation must surface")
	}
}

func TestCreateEmployee_RequiresInternalCredentials(t *testing.T) {
	r := &employeeResource{clients: &providerClients{Web: &fakeClient{}}}

	m := employeeModelFor(1, "X", "e@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error without internal credentials")
	}
	text := diagsText(resp.Diagnostics)
	for _, want := range []string{"KALA_USERNAME", "KALA_PASSWORD"} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostic should name %s, got: %s", want, text)
		}
	}
}

// --- Read -----------------------------------------------------------------

func TestReadEmployee_RefreshesActivationAndComputedFields(t *testing.T) {
	fi := newFakeInternal(client.Worker{
		WorkerNr: 3, Name: "X", Title: "Montør", Phone: "+45",
		Department: "Ops", Initials: "XX", IsValidated: false,
	})
	r := newEmployeeResource(fi)

	prior := employeeModelFor(3, "X", "e@example.com", true)
	resp := &resource.ReadResponse{State: employeeState(t, prior)}
	r.Read(context.Background(), resource.ReadRequest{State: employeeState(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %s", diagsText(resp.Diagnostics))
	}

	var got employeeResourceModel
	resp.State.Get(context.Background(), &got)

	if got.Active.ValueBool() {
		t.Error("active should reflect upstream isValidated=false — this is the drift signal")
	}
	if got.Department.ValueString() != "Ops" || got.Initials.ValueString() != "XX" {
		t.Errorf("computed fields not refreshed: %+v", got)
	}
	// email is write-only; it must survive from prior state, not be nulled.
	if got.Email.ValueString() != "e@example.com" {
		t.Errorf("email = %q, want it preserved — refreshing it would cause a perpetual diff", got.Email.ValueString())
	}
}

func TestReadEmployee_MissingIsDrift(t *testing.T) {
	fi := newFakeInternal()
	r := newEmployeeResource(fi)

	prior := employeeModelFor(99, "Gone", "g@example.com", true)
	resp := &resource.ReadResponse{State: employeeState(t, prior)}
	r.Read(context.Background(), resource.ReadRequest{State: employeeState(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a missing employee is drift, not an error: %s", diagsText(resp.Diagnostics))
	}
	if !resp.State.Raw.IsNull() {
		t.Error("state should be removed")
	}
}

// --- Update ---------------------------------------------------------------

func TestUpdateEmployee_ActivationChangeIsWritten(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "X", "e@example.com", true)
	plan := employeeModelFor(3, "X", "e@example.com", false)

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, plan), State: employeeState(t, state),
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.setValidated) != 1 || fi.setValidated[0].value {
		t.Errorf("want SetValidated(false), got %+v", fi.setValidated)
	}
}

func TestUpdateEmployee_NameChangeWarnsRatherThanSilentlyFailing(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "Old", IsValidated: true})
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "Old", "e@example.com", true)
	plan := employeeModelFor(3, "New", "e@example.com", true)

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, plan), State: employeeState(t, state),
	}, resp)

	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("a name change must warn — Kala cannot rename employees")
	}
	if !strings.Contains(diagsText(resp.Diagnostics), "no endpoint to rename") {
		t.Errorf("warning should explain why, got: %s", diagsText(resp.Diagnostics))
	}
}

func TestUpdateEmployee_NoActivationChangeMakesNoWrite(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	r := newEmployeeResource(fi)

	same := employeeModelFor(3, "X", "e@example.com", true)
	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, same), State: employeeState(t, same),
	}, resp)

	if len(fi.setValidated) != 0 {
		t.Errorf("no write expected when nothing changed, got %+v", fi.setValidated)
	}
}

// --- Delete (ADR-002) -----------------------------------------------------

func TestDeleteEmployee_DeactivatesAndWarns(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "Departing", IsValidated: true})
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "Departing", "d@example.com", true)
	resp := &resource.DeleteResponse{State: employeeState(t, state)}
	r.Delete(context.Background(), resource.DeleteRequest{State: employeeState(t, state)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.setValidated) != 1 || fi.setValidated[0].value {
		t.Fatalf("want SetValidated(3,false), got %+v", fi.setValidated)
	}
	if fi.workers[3].IsValidated {
		t.Error("the employee should be deactivated")
	}

	text := diagsText(resp.Diagnostics)
	for _, want := range []string{"deactivated", "Departing", "reactivate"} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Errorf("warning must mention %q, got: %s", want, text)
		}
	}
}

// ARCH1.5 excludes this path from graceful degradation: a failed deactivation
// must fail the apply rather than report a cleanup that did not happen.
func TestDeleteEmployee_FailureIsAnErrorNotAWarning(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	fi.setValidErr = errors.New("upstream down")
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "X", "d@example.com", true)
	resp := &resource.DeleteResponse{State: employeeState(t, state)}
	r.Delete(context.Background(), resource.DeleteRequest{State: employeeState(t, state)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed deactivation must fail the apply")
	}
	if !strings.Contains(diagsText(resp.Diagnostics), "still active") {
		t.Errorf("error should state the employee remains active, got: %s", diagsText(resp.Diagnostics))
	}
}

func TestDeleteEmployee_AlreadyGoneIsAWarningNotAnError(t *testing.T) {
	fi := newFakeInternal()
	fi.setValidErr = client.ErrNotFound
	r := newEmployeeResource(fi)

	state := employeeModelFor(99, "X", "d@example.com", true)
	resp := &resource.DeleteResponse{State: employeeState(t, state)}
	r.Delete(context.Background(), resource.DeleteRequest{State: employeeState(t, state)}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("nothing to deactivate should not fail: %s", diagsText(resp.Diagnostics))
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("want a warning")
	}
}

// --- Import ---------------------------------------------------------------

func TestImportEmployee_ByNumber(t *testing.T) {
	r := newEmployeeResource(newFakeInternal())

	typ := employeeSchema(t).Type().TerraformType(context.Background())
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: employeeSchema(t), Raw: tftypes.NewValue(typ, nil)},
	}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "3"}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %s", diagsText(resp.Diagnostics))
	}
	if !strings.Contains(diagsText(resp.Diagnostics), "email") {
		t.Error("import must warn that email cannot be recovered")
	}
}

func TestImportEmployee_MalformedID(t *testing.T) {
	r := newEmployeeResource(newFakeInternal())

	typ := employeeSchema(t).Type().TerraformType(context.Background())
	for _, id := range []string{"", "abc", "0"} {
		resp := &resource.ImportStateResponse{
			State: tfsdk.State{Schema: employeeSchema(t), Raw: tftypes.NewValue(typ, nil)},
		}
		r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("import ID %q should fail", id)
		}
	}
}

func TestEmployeeResource_Configure(t *testing.T) {
	r := NewEmployeeResource().(*employeeResource)

	resp := &resource.ConfigureResponse{}
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Error("nil ProviderData must not error")
	}

	resp = &resource.ConfigureResponse{}
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "wrong"}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("want a diagnostic for the wrong type")
	}

	resp = &resource.ConfigureResponse{}
	r.Configure(context.Background(), resource.ConfigureRequest{
		ProviderData: &providerClients{Web: &fakeClient{}, Internal: newFakeInternal()},
	}, resp)
	if resp.Diagnostics.HasError() || r.clients == nil {
		t.Error("clients were not stored")
	}
}

// --- remaining branches ---------------------------------------------------

func TestCreateEmployee_LookupFailureIsAnError(t *testing.T) {
	fi := newFakeInternal()
	fi.getWorkerErr = errors.New("upstream unreachable")
	r := newEmployeeResource(fi)

	m := employeeModelFor(3, "X", "e@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a lookup failure must not be mistaken for not-found")
	}
	if fi.createCalled {
		t.Error("must not create when the lookup itself failed — that could duplicate a person")
	}
}

func TestCreateEmployee_CreateFailurePropagates(t *testing.T) {
	fi := newFakeInternal()
	fi.createErr = errors.New("signup rejected")
	r := newEmployeeResource(fi)

	m := employeeModelFor(3, "X", "e@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want the create error to surface")
	}
}

func TestCreateEmployee_AdoptDeactivationFailurePropagates(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	fi.setValidErr = errors.New("refused")
	r := newEmployeeResource(fi)

	m := employeeModelFor(3, "X", "e@example.com", false)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want the deactivation failure to surface")
	}
}

func TestReadEmployee_UpstreamErrorIsNotTreatedAsDrift(t *testing.T) {
	fi := newFakeInternal()
	fi.getWorkerErr = client.ErrUnauthorized
	r := newEmployeeResource(fi)

	prior := employeeModelFor(3, "X", "e@example.com", true)
	resp := &resource.ReadResponse{State: employeeState(t, prior)}
	r.Read(context.Background(), resource.ReadRequest{State: employeeState(t, prior)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("an auth failure must error rather than silently drop the resource")
	}
}

func TestReadEmployee_RequiresInternalCredentials(t *testing.T) {
	r := &employeeResource{clients: &providerClients{Web: &fakeClient{}}}

	prior := employeeModelFor(3, "X", "e@example.com", true)
	resp := &resource.ReadResponse{State: employeeState(t, prior)}
	r.Read(context.Background(), resource.ReadRequest{State: employeeState(t, prior)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("want a credentials diagnostic")
	}
}

func TestUpdateEmployee_RequiresInternalCredentials(t *testing.T) {
	r := &employeeResource{clients: &providerClients{Web: &fakeClient{}}}

	m := employeeModelFor(3, "X", "e@example.com", true)
	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, m), State: employeeState(t, m),
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("want a credentials diagnostic")
	}
}

func TestDeleteEmployee_RequiresInternalCredentials(t *testing.T) {
	r := &employeeResource{clients: &providerClients{Web: &fakeClient{}}}

	m := employeeModelFor(3, "X", "e@example.com", true)
	resp := &resource.DeleteResponse{State: employeeState(t, m)}
	r.Delete(context.Background(), resource.DeleteRequest{State: employeeState(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("want a credentials diagnostic")
	}
}

func TestUpdateEmployee_ActivationWriteFailurePropagates(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	fi.setValidErr = errors.New("refused")
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "X", "e@example.com", true)
	plan := employeeModelFor(3, "X", "e@example.com", false)

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, plan), State: employeeState(t, state),
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("want the write failure to surface")
	}
}

func TestUpdateEmployee_EmailChangeWarns(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "X", "old@example.com", true)
	plan := employeeModelFor(3, "X", "new@example.com", true)

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, plan), State: employeeState(t, state),
	}, resp)

	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("an email change must warn — it is only sent at creation")
	}
	if !strings.Contains(diagsText(resp.Diagnostics), "state only") {
		t.Errorf("warning should say the value is state-only, got: %s", diagsText(resp.Diagnostics))
	}
}

// refresh runs after every write; if the read-back fails the operation must
// fail rather than persisting state we could not confirm.
func TestCreateEmployee_ReadBackFailureIsAnError(t *testing.T) {
	fi := &failAfterCreate{fakeInternal: newFakeInternal()}
	r := &employeeResource{clients: &providerClients{Web: &fakeClient{}, Internal: fi}}

	m := employeeModelFor(5, "X", "e@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an unverifiable create must fail rather than persist unconfirmed state")
	}
}

// failAfterCreate succeeds at creation but fails every subsequent read.
type failAfterCreate struct {
	*fakeInternal
	created bool
}

func (f *failAfterCreate) CreateWorker(ctx context.Context, in client.NewWorker) (client.Worker, error) {
	f.created = true
	return f.fakeInternal.CreateWorker(ctx, in)
}

func (f *failAfterCreate) GetWorker(ctx context.Context, nr int64) (client.Worker, error) {
	if f.created {
		return client.Worker{}, errors.New("read-back failed")
	}
	return f.fakeInternal.GetWorker(ctx, nr)
}

func TestApplyWorker_DefaultsAdoptedWhenUnset(t *testing.T) {
	m := employeeResourceModel{}
	applyWorker(&m, client.Worker{WorkerNr: 1, IsValidated: true, Title: "T"})

	if m.Adopted.IsNull() || m.Adopted.ValueBool() {
		t.Errorf("adopted should default to false, got %v", m.Adopted)
	}
	if !m.Active.ValueBool() || m.Title.ValueString() != "T" {
		t.Errorf("fields not applied: %+v", m)
	}
}

func TestApplyWorker_PreservesExplicitAdopted(t *testing.T) {
	m := employeeResourceModel{Adopted: types.BoolValue(true)}
	applyWorker(&m, client.Worker{WorkerNr: 1})

	if !m.Adopted.ValueBool() {
		t.Error("an explicit adopted=true must not be reset")
	}
}
