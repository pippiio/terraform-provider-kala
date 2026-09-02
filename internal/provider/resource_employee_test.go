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

	// email write path
	emailSet    []emailSetCall
	setEmailErr error

	// welcome email
	welcomeSent []string
	welcomeErr  error

	// field writes
	fieldSets []fieldSetCall
	roleSets  []roleSetCall
	dateSet   string
	fieldErr  error

	// WorkerInfo enrichment
	info      client.WorkerInfo
	infoErr   error
	infoCalls int
	infoByNr  map[int64]client.WorkerInfo
}

type setValidatedCall struct {
	number int64
	value  bool
}

type emailSetCall struct {
	number int64
	email  string
}

type fieldSetCall struct {
	field client.WorkerField
	value string
}

type roleSetCall struct {
	role  client.WorkerRole
	value bool
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

func (f *fakeInternal) SetWorkerEmail(_ context.Context, nr int64, email string) error {
	f.emailSet = append(f.emailSet, emailSetCall{nr, email})
	if f.setEmailErr != nil {
		return f.setEmailErr
	}
	// Reflect the change so a subsequent enrichment read sees it.
	if f.info.WorkerNr != 0 {
		f.info.Email = email
	}
	if f.infoByNr != nil {
		if i, ok := f.infoByNr[nr]; ok {
			i.Email = email
			f.infoByNr[nr] = i
		}
	}
	return nil
}

func (f *fakeInternal) SendWelcomeEmail(_ context.Context, email string) error {
	f.welcomeSent = append(f.welcomeSent, email)
	return f.welcomeErr
}

func (f *fakeInternal) SetWorkerField(_ context.Context, _ int64, field client.WorkerField, v string) error {
	f.fieldSets = append(f.fieldSets, fieldSetCall{field, v})
	return f.fieldErr
}

func (f *fakeInternal) SetWorkerRole(_ context.Context, _ int64, role client.WorkerRole, v bool) error {
	f.roleSets = append(f.roleSets, roleSetCall{role, v})
	return f.fieldErr
}

func (f *fakeInternal) SetWorkerDateOfEmployment(_ context.Context, _ int64, date string) error {
	f.dateSet = date
	return f.fieldErr
}

func (f *fakeInternal) GetWorkerInfo(_ context.Context, nr int64) (client.WorkerInfo, error) {
	f.infoCalls++
	if f.infoErr != nil {
		return client.WorkerInfo{}, f.infoErr
	}
	if i, ok := f.infoByNr[nr]; ok {
		return i, nil
	}
	if f.info.WorkerNr != 0 {
		return f.info, nil
	}
	// Default: derive a minimal record from the worker so enrichment succeeds.
	w, ok := f.workers[nr]
	if !ok {
		return client.WorkerInfo{}, client.ErrNotFound
	}
	return client.WorkerInfo{
		WorkerNr: w.WorkerNr, WorkerID: w.WorkerNr, Name: w.Name,
		Title: w.Title, Phone: w.Phone, Department: w.Department,
		Initials: w.Initials, IsValidated: w.IsValidated,
	}, nil
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
	bl := func(v types.Bool) any {
		if v.IsNull() {
			return nil
		}
		return v.ValueBool()
	}
	i64 := func(v types.Int64) any {
		if v.IsNull() {
			return nil
		}
		return v.ValueInt64()
	}
	return tftypes.NewValue(typ.(tftypes.Object), map[string]tftypes.Value{
		"employee_number":       tftypes.NewValue(tftypes.Number, m.EmployeeNumber.ValueInt64()),
		"name":                  tftypes.NewValue(tftypes.String, m.Name.ValueString()),
		"email":                 tftypes.NewValue(tftypes.String, m.Email.ValueString()),
		"active":                tftypes.NewValue(tftypes.Bool, m.Active.ValueBool()),
		"send_welcome_email":    tftypes.NewValue(tftypes.Bool, bl(m.SendWelcomeEmail)),
		"title":                 tftypes.NewValue(tftypes.String, str(m.Title)),
		"phone":                 tftypes.NewValue(tftypes.String, str(m.Phone)),
		"private_phone":         tftypes.NewValue(tftypes.String, str(m.PrivatePhone)),
		"department":            tftypes.NewValue(tftypes.String, str(m.Department)),
		"initials":              tftypes.NewValue(tftypes.String, str(m.Initials)),
		"license_plate":         tftypes.NewValue(tftypes.String, str(m.LicensePlate)),
		"date_of_employment":    tftypes.NewValue(tftypes.String, str(m.DateOfEmployment)),
		"flex_start_date":       tftypes.NewValue(tftypes.String, str(m.FlexStartDate)),
		"norm_hours":            tftypes.NewValue(tftypes.String, str(m.NormHours)),
		"leader_note":           tftypes.NewValue(tftypes.String, str(m.LeaderNote)),
		"is_leader":             tftypes.NewValue(tftypes.Bool, bl(m.IsLeader)),
		"is_planner":            tftypes.NewValue(tftypes.Bool, bl(m.IsPlanner)),
		"is_super_user":         tftypes.NewValue(tftypes.Bool, bl(m.IsSuperUser)),
		"is_finance":            tftypes.NewValue(tftypes.Bool, bl(m.IsFinance)),
		"is_visible_in_planner": tftypes.NewValue(tftypes.Bool, bl(m.IsVisibleInPlanner)),
		"worker_id":             tftypes.NewValue(tftypes.Number, i64(m.WorkerID)),
		"adopted":               tftypes.NewValue(tftypes.Bool, m.Adopted.ValueBool()),
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
		EmployeeNumber:   types.Int64Value(number),
		Name:             types.StringValue(name),
		Email:            types.StringValue(email),
		Active:           types.BoolValue(active),
		SendWelcomeEmail: types.BoolValue(true),
		// Undeclared Optional+Computed attributes are null in a real plan, not
		// empty strings — the distinction is what stops the provider blanking
		// fields on an adopted employee.
		Title:              types.StringNull(),
		Phone:              types.StringNull(),
		PrivatePhone:       types.StringNull(),
		Department:         types.StringNull(),
		Initials:           types.StringNull(),
		LicensePlate:       types.StringNull(),
		DateOfEmployment:   types.StringNull(),
		FlexStartDate:      types.StringNull(),
		NormHours:          types.StringNull(),
		LeaderNote:         types.StringNull(),
		IsLeader:           types.BoolNull(),
		IsPlanner:          types.BoolNull(),
		IsSuperUser:        types.BoolNull(),
		IsFinance:          types.BoolNull(),
		IsVisibleInPlanner: types.BoolNull(),
		WorkerID:           types.Int64Null(),
		Adopted:            types.BoolValue(false),
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

	// Corrected twice on 2026-09-01: email is readable (WorkerInfo) AND
	// writable (SetEmailNew), so it is fully managed. Earlier descriptions
	// claimed write-only, then read-only-after-create; both were wrong.
	email := strings.ToLower(s.Attributes["email"].GetMarkdownDescription())
	for _, stale := range []string{"write-only", "no endpoint to change", "cannot be changed"} {
		if strings.Contains(email, stale) {
			t.Errorf("email description still carries the stale claim %q", stale)
		}
	}
	if !strings.Contains(email, "drift") {
		t.Error("email should document that it is drift-detected")
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
		WorkerNr: 3, Name: "X", Title: "Ringbearer", Phone: "+45",
		Department: "Ops", Initials: "XX", IsValidated: false,
	})
	fi.info = client.WorkerInfo{
		WorkerNr: 3, WorkerID: 3, Name: "X", Email: "upstream@example.com",
		Title: "Ringbearer", Phone: "+45", Department: "Ops", Initials: "XX",
		NormHours: "37", DateOfEmployment: "2020-01-01", IsLeader: true,
	}
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
	// Corrected 2026-09-01: email IS readable via WorkerInfo, so Read must
	// refresh it from upstream rather than carrying the configured value
	// forward. That is what gives the field real drift detection.
	if got.Email.ValueString() != "upstream@example.com" {
		t.Errorf("email = %q, want it refreshed from WorkerInfo", got.Email.ValueString())
	}

	// The rest of the WorkerInfo enrichment must land too.
	if got.NormHours.ValueString() != "37" {
		t.Errorf("norm_hours = %q, want 37", got.NormHours.ValueString())
	}
	if got.DateOfEmployment.ValueString() != "2020-01-01" {
		t.Errorf("date_of_employment = %q, want 2020-01-01", got.DateOfEmployment.ValueString())
	}
	if !got.IsLeader.ValueBool() {
		t.Error("is_leader should be refreshed from WorkerInfo")
	}
	if got.WorkerID.ValueInt64() != 3 {
		t.Errorf("worker_id = %d, want 3", got.WorkerID.ValueInt64())
	}
}

// ARCH1.5: WorkerInfo is a read-enrichment path, so a failure must degrade
// rather than fail — and must NOT null the prior values, which would
// manufacture an unresolvable diff on every plan.
func TestReadEmployee_EnrichmentFailureKeepsPriorValues(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	fi.infoErr = errors.New("WorkerInfo unavailable")
	r := newEmployeeResource(fi)

	prior := employeeModelFor(3, "X", "kept@example.com", true)
	prior.Department = types.StringValue("Ops")

	resp := &resource.ReadResponse{State: employeeState(t, prior)}
	r.Read(context.Background(), resource.ReadRequest{State: employeeState(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("enrichment failure must not fail the read: %s", diagsText(resp.Diagnostics))
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("a degraded read should warn")
	}

	var got employeeResourceModel
	resp.State.Get(context.Background(), &got)
	if got.Email.ValueString() != "kept@example.com" {
		t.Errorf("email = %q, want the prior value kept", got.Email.ValueString())
	}
	if got.Department.ValueString() != "Ops" {
		t.Errorf("department = %q, want the prior value kept", got.Department.ValueString())
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

// --- Delete ---------------------------------------------------------------

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

func TestUpdateEmployee_EmailChangeIsWritten(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "X", "old@example.com", true)
	plan := employeeModelFor(3, "X", "new@example.com", true)

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, plan), State: employeeState(t, state),
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.emailSet) != 1 {
		t.Fatalf("want one SetWorkerEmail call, got %+v", fi.emailSet)
	}
	if fi.emailSet[0].number != 3 || fi.emailSet[0].email != "new@example.com" {
		t.Errorf("sent %+v, want {3 new@example.com}", fi.emailSet[0])
	}
}

func TestUpdateEmployee_EmailUnchangedMakesNoWrite(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	r := newEmployeeResource(fi)

	same := employeeModelFor(3, "X", "same@example.com", true)
	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, same), State: employeeState(t, same),
	}, resp)

	if len(fi.emailSet) != 0 {
		t.Errorf("no email write expected, got %+v", fi.emailSet)
	}
}

func TestUpdateEmployee_EmailWriteFailurePropagates(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	fi.setEmailErr = errors.New("rejected by Kala")
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "X", "old@example.com", true)
	plan := employeeModelFor(3, "X", "new@example.com", true)

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, plan), State: employeeState(t, state),
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed email change must fail the apply, not warn")
	}
	if !strings.Contains(diagsText(resp.Diagnostics), "rejected by Kala") {
		t.Errorf("error should carry the cause: %s", diagsText(resp.Diagnostics))
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

func TestApplyWorker_OnlyTouchesActivationAndAdopted(t *testing.T) {
	m := employeeResourceModel{Department: types.StringValue("Ops")}
	applyWorker(&m, client.Worker{WorkerNr: 1, IsValidated: true, Title: "T", Department: ""})

	if m.Adopted.IsNull() || m.Adopted.ValueBool() {
		t.Errorf("adopted should default to false, got %v", m.Adopted)
	}
	if !m.Active.ValueBool() {
		t.Error("active should be applied from the list record")
	}
	// The list returns blanks for these even when WorkerInfo has values, so
	// applyWorker must leave them alone.
	if m.Department.ValueString() != "Ops" {
		t.Errorf("department = %q, want the enriched value untouched", m.Department.ValueString())
	}
	if !m.Title.IsNull() {
		t.Errorf("title should not be set from the list record, got %v", m.Title)
	}
}

func TestApplyWorkerInfo_PopulatesEverything(t *testing.T) {
	m := employeeResourceModel{}
	applyWorkerInfo(&m, client.WorkerInfo{
		WorkerNr: 3, WorkerID: 3, Email: "a@b.c", Title: "T", Phone: "p",
		PrivatePhone: "pp", Department: "D", Initials: "AB", LicensePlate: "XY12345",
		DateOfEmployment: "2020-01-01", FlexStartDate: "2021-01-01", NormHours: "37",
		LeaderNote: "note", IsLeader: true, IsPlanner: true, IsSuperUser: true,
		IsFinance: true, IsVisibleInPlanner: true,
	})

	checks := map[string]string{
		"email": m.Email.ValueString(), "title": m.Title.ValueString(),
		"private_phone": m.PrivatePhone.ValueString(), "department": m.Department.ValueString(),
		"initials": m.Initials.ValueString(), "license_plate": m.LicensePlate.ValueString(),
		"date_of_employment": m.DateOfEmployment.ValueString(),
		"flex_start_date":    m.FlexStartDate.ValueString(),
		"norm_hours":         m.NormHours.ValueString(), "leader_note": m.LeaderNote.ValueString(),
	}
	for name, v := range checks {
		if v == "" {
			t.Errorf("%s was not populated", name)
		}
	}
	for name, v := range map[string]bool{
		"is_leader": m.IsLeader.ValueBool(), "is_planner": m.IsPlanner.ValueBool(),
		"is_super_user": m.IsSuperUser.ValueBool(), "is_finance": m.IsFinance.ValueBool(),
		"is_visible_in_planner": m.IsVisibleInPlanner.ValueBool(),
	} {
		if !v {
			t.Errorf("%s was not populated", name)
		}
	}
	if m.WorkerID.ValueInt64() != 3 {
		t.Errorf("worker_id = %d, want 3", m.WorkerID.ValueInt64())
	}
}

func TestApplyWorker_PreservesExplicitAdopted(t *testing.T) {
	m := employeeResourceModel{Adopted: types.BoolValue(true)}
	applyWorker(&m, client.Worker{WorkerNr: 1})

	if !m.Adopted.ValueBool() {
		t.Error("an explicit adopted=true must not be reset")
	}
}

// --- field writes ---------------------------------------------------------

func TestUpdateEmployee_WritesOnlyChangedFields(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "X", "e@example.com", true)
	state.Title = types.StringValue("Old Title")
	state.Phone = types.StringValue("111")
	state.Department = types.StringValue("Ops")

	plan := state
	plan.Title = types.StringValue("New Title") // only this changes

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, plan), State: employeeState(t, state),
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.fieldSets) != 1 {
		t.Fatalf("want exactly one field write, got %+v", fi.fieldSets)
	}
	if fi.fieldSets[0].field != client.FieldTitle || fi.fieldSets[0].value != "New Title" {
		t.Errorf("wrote %+v, want title=New Title", fi.fieldSets[0])
	}
}

func TestUpdateEmployee_WritesRoleChanges(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "X", "e@example.com", true)
	plan := state
	plan.IsLeader = types.BoolValue(true)
	plan.IsFinance = types.BoolValue(true)

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, plan), State: employeeState(t, state),
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.roleSets) != 2 {
		t.Fatalf("want two role writes, got %+v", fi.roleSets)
	}
}

func TestUpdateEmployee_WritesDateOfEmployment(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "X", "e@example.com", true)
	plan := state
	plan.DateOfEmployment = types.StringValue("2026-03-15")

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, plan), State: employeeState(t, state),
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %s", diagsText(resp.Diagnostics))
	}
	if fi.dateSet != "2026-03-15" {
		t.Errorf("dateSet = %q, want 2026-03-15", fi.dateSet)
	}
}

func TestUpdateEmployee_NoFieldChangesMakesNoWrites(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	r := newEmployeeResource(fi)

	same := employeeModelFor(3, "X", "e@example.com", true)
	same.Title = types.StringValue("T")

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, same), State: employeeState(t, same),
	}, resp)

	if len(fi.fieldSets) != 0 || len(fi.roleSets) != 0 || fi.dateSet != "" {
		t.Errorf("no writes expected: fields=%+v roles=%+v date=%q", fi.fieldSets, fi.roleSets, fi.dateSet)
	}
}

func TestUpdateEmployee_FieldWriteFailureIsAttributeScoped(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	fi.fieldErr = errors.New("kala refused")
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "X", "e@example.com", true)
	plan := state
	plan.Phone = types.StringValue("999")

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, plan), State: employeeState(t, state),
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed field write must fail the apply")
	}
	if !strings.Contains(diagsText(resp.Diagnostics), "phone") {
		t.Errorf("the diagnostic should name the attribute: %s", diagsText(resp.Diagnostics))
	}
}

// SignUp only accepts number/name/email, so everything else declared in the
// configuration has to be written separately at create time.
func TestCreateEmployee_AppliesDeclaredFields(t *testing.T) {
	fi := newFakeInternal()
	r := newEmployeeResource(fi)

	m := employeeModelFor(20, "New", "n@example.com", true)
	m.Title = types.StringValue("Ringbearer")
	m.Phone = types.StringValue("12345678")
	m.IsLeader = types.BoolValue(true)

	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if !fi.createCalled {
		t.Fatal("the employee was not created")
	}
	if len(fi.fieldSets) != 2 {
		t.Errorf("want title and phone written, got %+v", fi.fieldSets)
	}
	if len(fi.roleSets) != 1 {
		t.Errorf("want is_leader written, got %+v", fi.roleSets)
	}
}

func TestEmployeeSchema_SettableFieldsAreOptionalAndComputed(t *testing.T) {
	s := employeeSchema(t)

	// Settable: declaring them manages them, omitting them adopts Kala's value.
	for _, name := range []string{
		"title", "phone", "department", "initials", "license_plate",
		"leader_note", "date_of_employment", "is_leader", "is_planner", "is_finance",
	} {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Errorf("missing attribute %q", name)
			continue
		}
		if !attr.IsOptional() || !attr.IsComputed() {
			t.Errorf("%q should be Optional+Computed, got optional=%t computed=%t",
				name, attr.IsOptional(), attr.IsComputed())
		}
	}

	// Read-only: Kala exposes no endpoint to set these, so offering them as
	// writable would be a promise the provider cannot keep.
	for _, name := range []string{"private_phone", "flex_start_date", "norm_hours", "is_super_user", "is_visible_in_planner", "worker_id"} {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Errorf("missing attribute %q", name)
			continue
		}
		if attr.IsOptional() {
			t.Errorf("%q must NOT be settable — Kala has no endpoint for it", name)
		}
	}
}

// The sharpest edge of adoption: adopting an employee must not blank the
// fields the configuration does not mention. Kala cannot undo a wipe.
func TestCreateEmployee_AdoptionDoesNotBlankUndeclaredFields(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "Existing", IsValidated: true})
	fi.info = client.WorkerInfo{
		WorkerNr: 3, WorkerID: 3, Name: "Existing", Email: "e@example.com",
		Title: "Ringbearer", Department: "Ops", Initials: "EX", Phone: "111",
		IsLeader: true, IsValidated: true,
	}
	r := newEmployeeResource(fi)

	// Configuration declares only the required attributes.
	m := employeeModelFor(3, "Existing", "e@example.com", true)

	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("adopt failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.fieldSets) != 0 {
		t.Errorf("adoption wrote fields it was never told to: %+v", fi.fieldSets)
	}
	if len(fi.roleSets) != 0 {
		t.Errorf("adoption changed roles it was never told to: %+v", fi.roleSets)
	}
	if fi.dateSet != "" {
		t.Errorf("adoption wrote date_of_employment: %q", fi.dateSet)
	}

	// And the employee's real values must survive into state.
	var got employeeResourceModel
	resp.State.Get(context.Background(), &got)
	if got.Department.ValueString() != "Ops" || got.Title.ValueString() != "Ringbearer" {
		t.Errorf("adopted values lost: department=%q title=%q",
			got.Department.ValueString(), got.Title.ValueString())
	}
}

// An explicitly declared empty string on UPDATE is a real request to clear the
// field, and must be honoured — unlike the create path.
func TestUpdateEmployee_ExplicitEmptyStringClearsField(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "X", "e@example.com", true)
	state.LeaderNote = types.StringValue("something")

	plan := state
	plan.LeaderNote = types.StringValue("")

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, plan), State: employeeState(t, state),
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.fieldSets) != 1 || fi.fieldSets[0].value != "" {
		t.Errorf("want leader_note cleared, got %+v", fi.fieldSets)
	}
}

func TestUpdateEmployee_RoleWriteFailureIsAttributeScoped(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	fi.fieldErr = errors.New("refused")
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "X", "e@example.com", true)
	state.IsPlanner = types.BoolValue(false)
	plan := state
	plan.IsPlanner = types.BoolValue(true)

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, plan), State: employeeState(t, state),
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want the role write failure to surface")
	}
	if !strings.Contains(diagsText(resp.Diagnostics), "is_planner") {
		t.Errorf("diagnostic should name the attribute: %s", diagsText(resp.Diagnostics))
	}
}

func TestUpdateEmployee_DateWriteFailureIsAttributeScoped(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	fi.fieldErr = errors.New("bad date")
	r := newEmployeeResource(fi)

	state := employeeModelFor(3, "X", "e@example.com", true)
	plan := state
	plan.DateOfEmployment = types.StringValue("2026-01-01")

	resp := &resource.UpdateResponse{State: emptyEmployeeState(t)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan: employeePlan(t, plan), State: employeeState(t, state),
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want the date write failure to surface")
	}
	if !strings.Contains(diagsText(resp.Diagnostics), "date_of_employment") {
		t.Errorf("diagnostic should name the attribute: %s", diagsText(resp.Diagnostics))
	}
}

// An explicitly declared false IS a request to revoke, even on adopt. Skipping
// it makes the applied result contradict the plan, which Terraform rejects with
// "Provider produced inconsistent result after apply".
func TestCreateEmployee_ExplicitFalseRoleIsWritten(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "X", IsValidated: true})
	fi.info = client.WorkerInfo{WorkerNr: 3, WorkerID: 3, IsLeader: true, IsValidated: true}
	r := newEmployeeResource(fi)

	m := employeeModelFor(3, "X", "e@example.com", true)
	m.IsLeader = types.BoolValue(false) // explicitly false: revoke it

	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if len(fi.roleSets) != 1 || fi.roleSets[0].value {
		t.Errorf("an explicit false must be written as a revoke, got %+v", fi.roleSets)
	}
}

// --- welcome email --------------------------------------------------------

func TestCreateEmployee_SendsWelcomeEmailOnGenuineCreation(t *testing.T) {
	fi := newFakeInternal()
	r := newEmployeeResource(fi)

	m := employeeModelFor(30, "New Hire", "hire@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.welcomeSent) != 1 || fi.welcomeSent[0] != "hire@example.com" {
		t.Errorf("want one welcome email to hire@example.com, got %v", fi.welcomeSent)
	}
}

// The important half: adopting an existing person must NOT mail them. They
// were onboarded long ago, and the email cannot be recalled.
func TestCreateEmployee_NoWelcomeEmailWhenAdopting(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "Existing", IsValidated: true})
	r := newEmployeeResource(fi)

	m := employeeModelFor(3, "Existing", "existing@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("adopt failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.welcomeSent) != 0 {
		t.Errorf("adoption must not email an already-onboarded person, got %v", fi.welcomeSent)
	}
}

func TestCreateEmployee_NoWelcomeEmailWhenReactivating(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "Returning", IsValidated: false})
	r := newEmployeeResource(fi)

	m := employeeModelFor(3, "Returning", "returning@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if len(fi.welcomeSent) != 0 {
		t.Errorf("reactivation must not send a welcome email, got %v", fi.welcomeSent)
	}
}

func TestCreateEmployee_WelcomeEmailCanBeDisabled(t *testing.T) {
	fi := newFakeInternal()
	r := newEmployeeResource(fi)

	m := employeeModelFor(31, "Silent", "silent@example.com", true)
	m.SendWelcomeEmail = types.BoolValue(false)

	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if !fi.createCalled {
		t.Error("the employee should still be created")
	}
	if len(fi.welcomeSent) != 0 {
		t.Errorf("send_welcome_email = false must suppress the email, got %v", fi.welcomeSent)
	}
}

// The employee exists and is configured; only the mail failed. Failing the
// apply would abandon state for a record that was created successfully.
func TestCreateEmployee_WelcomeEmailFailureWarnsButSucceeds(t *testing.T) {
	fi := newFakeInternal()
	fi.welcomeErr = errors.New("smtp rejected")
	r := newEmployeeResource(fi)

	m := employeeModelFor(32, "New", "new@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a failed email must not fail the apply: %s", diagsText(resp.Diagnostics))
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("a failed email must warn")
	}

	text := diagsText(resp.Diagnostics)
	if !strings.Contains(text, "new@example.com") {
		t.Errorf("the warning should name the address so it can be sent by hand: %s", text)
	}
	if !strings.Contains(text, "send_welcome_email = false") {
		t.Errorf("the warning should mention the opt-out: %s", text)
	}
}

func TestEmployeeSchema_WelcomeEmailIsDocumentedAsCreateOnly(t *testing.T) {
	s := employeeSchema(t)

	attr, ok := s.Attributes["send_welcome_email"]
	if !ok {
		t.Fatal("send_welcome_email attribute missing")
	}
	desc := strings.ToLower(attr.GetMarkdownDescription())
	if !strings.Contains(desc, "adopt") {
		t.Error("must document that adoption does not send the email")
	}
	if !strings.Contains(desc, "cannot be undone") {
		t.Error("must warn that sending mail is irreversible")
	}
}

// --- F1 regression: email convergence on adoption -------------------------

// Adopting an employee whose upstream email differs from the configuration must
// CONVERGE it, not silently adopt the upstream value. Returning state that
// contradicts the plan makes Terraform reject the apply with "Provider produced
// inconsistent result after apply" — the same failure class as the is_planner
// bug fixed in 8911ae7, in a path that had no test.
func TestCreateEmployee_AdoptConvergesDifferingEmail(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "Existing", IsValidated: true})
	fi.info = client.WorkerInfo{
		WorkerNr: 3, WorkerID: 3, Name: "Existing",
		Email:       "upstream@example.com",
		IsValidated: true,
	}
	r := newEmployeeResource(fi)

	m := employeeModelFor(3, "Existing", "configured@example.com", true)

	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("adopt failed: %s", diagsText(resp.Diagnostics))
	}

	if len(fi.emailSet) != 1 || fi.emailSet[0].email != "configured@example.com" {
		t.Errorf("adoption must write the configured email upstream, got %+v", fi.emailSet)
	}

	var got employeeResourceModel
	resp.State.Get(context.Background(), &got)
	if got.Email.ValueString() != "configured@example.com" {
		t.Errorf("state.email = %q but plan said %q — Terraform will reject this apply",
			got.Email.ValueString(), "configured@example.com")
	}
}

// The matching non-change case: adopting someone whose email already matches
// must not issue a pointless write.
func TestCreateEmployee_AdoptMatchingEmailWritesNothing(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 3, Name: "Existing", IsValidated: true})
	fi.info = client.WorkerInfo{
		WorkerNr: 3, WorkerID: 3, Email: "same@example.com", IsValidated: true,
	}
	r := newEmployeeResource(fi)

	m := employeeModelFor(3, "Existing", "same@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("adopt failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.emailSet) != 0 {
		t.Errorf("no email write expected when it already matches, got %+v", fi.emailSet)
	}
}

// Genuine creation carries the email through SignUp, so convergence must not
// issue a second, redundant SetEmailNew.
func TestCreateEmployee_GenuineCreateDoesNotRewriteEmail(t *testing.T) {
	fi := newFakeInternal()
	r := newEmployeeResource(fi)

	m := employeeModelFor(60, "New Hire", "new@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if !fi.createCalled {
		t.Fatal("CreateWorker was not called")
	}
	if fi.created.Email != "new@example.com" {
		t.Errorf("SignUp should carry the email, got %q", fi.created.Email)
	}
	if len(fi.emailSet) != 0 {
		t.Errorf("SignUp already set the email; a second write is redundant, got %+v", fi.emailSet)
	}
}

// --- F3: a partial create must not strand a permanent record --------------
//
// Kala has no delete. If Create errors after SignUp succeeded and Terraform
// holds no state, the employee is stranded: Terraform will not manage it, will
// not destroy it, and the next apply silently adopts it instead of creating it.
// State must be written on every failure path that runs after the record
// exists, and the diagnostics must name the number that now exists.

// stateWritten reports whether Create left a usable state object behind.
func stateWritten(t *testing.T, s tfsdk.State) (employeeResourceModel, bool) {
	t.Helper()
	if s.Raw.IsNull() || !s.Raw.IsKnown() {
		return employeeResourceModel{}, false
	}
	var m employeeResourceModel
	if diags := s.Get(context.Background(), &m); diags.HasError() {
		return employeeResourceModel{}, false
	}
	return m, true
}

func TestCreateEmployee_FieldWriteFailureStillRecordsTheEmployee(t *testing.T) {
	fi := newFakeInternal()
	fi.fieldErr = errors.New("kala rejected the title")
	r := newEmployeeResource(fi)

	m := employeeModelFor(50, "Radagast the Brown", "radagast@example.com", true)
	m.Title = types.StringValue("Wizard")
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error when a field write fails")
	}
	if !fi.createCalled {
		t.Fatal("precondition: the employee should have been created")
	}

	got, ok := stateWritten(t, resp.State)
	if !ok {
		t.Fatal("employee was created in Kala but no state was written — the record is stranded")
	}
	if got.EmployeeNumber.ValueInt64() != 50 {
		t.Errorf("state records employee %d, want 50", got.EmployeeNumber.ValueInt64())
	}
}

func TestCreateEmployee_DeactivationFailureStillRecordsTheEmployee(t *testing.T) {
	fi := newFakeInternal()
	fi.setValidErr = errors.New("kala refused")
	r := newEmployeeResource(fi)

	m := employeeModelFor(51, "Radagast the Brown", "radagast@example.com", false)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error when deactivation fails")
	}
	if _, ok := stateWritten(t, resp.State); !ok {
		t.Fatal("employee was created in Kala but no state was written — the record is stranded")
	}
}

func TestCreateEmployee_ReadBackFailureStillRecordsTheEmployee(t *testing.T) {
	// Not-found on the pre-flight lookup, then a hard failure on every read
	// after creation. The record exists in Kala either way.
	fi := &failAfterCreate{fakeInternal: newFakeInternal()}
	r := &employeeResource{clients: &providerClients{Web: &fakeClient{}, Internal: fi}}

	m := employeeModelFor(52, "Radagast the Brown", "radagast@example.com", true)
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error when the read-back fails")
	}
	if _, ok := stateWritten(t, resp.State); !ok {
		t.Fatal("employee was created in Kala but no state was written — the record is stranded")
	}
}

func TestCreateEmployee_PartialCreateWarningNamesTheEmployeeAndSaysItIsPermanent(t *testing.T) {
	fi := newFakeInternal()
	fi.fieldErr = errors.New("kala rejected the title")
	r := newEmployeeResource(fi)

	m := employeeModelFor(53, "Radagast the Brown", "radagast@example.com", true)
	m.Title = types.StringValue("Wizard")
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	text := diagsText(resp.Diagnostics)
	for _, want := range []string{"53", "cannot be deleted", "apply again"} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostics do not mention %q:\n%s", want, text)
		}
	}
}

// Adoption is the milder case — the employee pre-existed, so nothing new is
// stranded — but state must still be written, or the reactivation this resource
// performed goes unrecorded and the next plan is computed against nothing.
func TestCreateEmployee_AdoptionFailureStillRecordsTheEmployee(t *testing.T) {
	fi := newFakeInternal(client.Worker{WorkerNr: 54, Name: "Radagast the Brown", IsValidated: true})
	fi.fieldErr = errors.New("kala rejected the title")
	r := newEmployeeResource(fi)

	m := employeeModelFor(54, "Radagast the Brown", "radagast@example.com", true)
	m.Title = types.StringValue("Wizard")
	resp := &resource.CreateResponse{State: emptyEmployeeState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: employeePlan(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error when a field write fails")
	}
	got, ok := stateWritten(t, resp.State)
	if !ok {
		t.Fatal("no state written for an adopted employee")
	}
	if !got.Adopted.ValueBool() {
		t.Error("state should record that the employee was adopted")
	}
}

// A model bound for state must carry no unknown values: the framework rejects
// a Computed attribute left unknown after apply. When the post-failure refresh
// cannot reach Kala, null is the honest substitute — it says Terraform does not
// know, and the next Read fills it in.
func TestResolveUnknowns_ReplacesEveryUnknownWithNull(t *testing.T) {
	m := employeeResourceModel{
		EmployeeNumber:     types.Int64Value(55),
		Name:               types.StringValue("Radagast the Brown"),
		Email:              types.StringValue("radagast@example.com"),
		Active:             types.BoolUnknown(),
		SendWelcomeEmail:   types.BoolUnknown(),
		Title:              types.StringUnknown(),
		Phone:              types.StringUnknown(),
		PrivatePhone:       types.StringUnknown(),
		Department:         types.StringUnknown(),
		Initials:           types.StringUnknown(),
		LicensePlate:       types.StringUnknown(),
		DateOfEmployment:   types.StringUnknown(),
		FlexStartDate:      types.StringUnknown(),
		NormHours:          types.StringUnknown(),
		LeaderNote:         types.StringUnknown(),
		IsLeader:           types.BoolUnknown(),
		IsPlanner:          types.BoolUnknown(),
		IsSuperUser:        types.BoolUnknown(),
		IsFinance:          types.BoolUnknown(),
		IsVisibleInPlanner: types.BoolUnknown(),
		WorkerID:           types.Int64Unknown(),
		Adopted:            types.BoolUnknown(),
	}

	resolveUnknowns(&m)

	for name, unknown := range map[string]bool{
		"active": m.Active.IsUnknown(), "send_welcome_email": m.SendWelcomeEmail.IsUnknown(),
		"title": m.Title.IsUnknown(), "phone": m.Phone.IsUnknown(),
		"private_phone": m.PrivatePhone.IsUnknown(), "department": m.Department.IsUnknown(),
		"initials": m.Initials.IsUnknown(), "license_plate": m.LicensePlate.IsUnknown(),
		"date_of_employment": m.DateOfEmployment.IsUnknown(), "flex_start_date": m.FlexStartDate.IsUnknown(),
		"norm_hours": m.NormHours.IsUnknown(), "leader_note": m.LeaderNote.IsUnknown(),
		"is_leader": m.IsLeader.IsUnknown(), "is_planner": m.IsPlanner.IsUnknown(),
		"is_super_user": m.IsSuperUser.IsUnknown(), "is_finance": m.IsFinance.IsUnknown(),
		"is_visible_in_planner": m.IsVisibleInPlanner.IsUnknown(),
		"worker_id":             m.WorkerID.IsUnknown(), "adopted": m.Adopted.IsUnknown(),
	} {
		if unknown {
			t.Errorf("%s is still unknown", name)
		}
	}

	// Known values must survive untouched.
	if m.EmployeeNumber.ValueInt64() != 55 || m.Name.ValueString() != "Radagast the Brown" {
		t.Error("resolveUnknowns altered a known value")
	}
}
