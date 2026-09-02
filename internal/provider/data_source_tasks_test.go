package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

type taskFake struct {
	client.InternalClient
	scan client.TaskScan
	err  error
	got  client.TaskQuery
}

func (f *taskFake) ListTasks(_ context.Context, q client.TaskQuery) (client.TaskScan, error) {
	f.got = q
	return f.scan, f.err
}

func i64ptr(v int64) *int64 { return &v }
func iptr(v int) *int       { return &v }

func sampleTasks() []client.Task {
	added := time.Date(2026, 9, 2, 7, 19, 3, 0, time.UTC)
	fin := time.Date(2026, 9, 2, 7, 19, 46, 0, time.UTC)
	return []client.Task{
		{
			ID: 5, Name: "Secret task", CaseID: 2, CaseNumber: "KA-2", ChecklistID: 11,
			StatusName: "Færdig", AssigneeWorkerNr: i64ptr(1), AssignedToMe: false,
			CreatedBy: "Frodo Baggins", FinishedBy: "Frodo Baggins",
			TimeAdded: &added, TimeFinished: &fin, IsFinished: true,
			InvoiceMode: "REG_HOURS&SPECIAL", PriceFixed: iptr(550),
			RegisteredHoursTotal: 4, BilledHours: 3,
			NoteRequired: false, ImageRequired: true, HasImage: false,
		},
		{
			ID: 6, Name: "Gutter", CaseID: 2, CaseNumber: "KA-2", ChecklistID: 11,
			StatusName: "To do", AssigneeWorkerNr: nil,
			CreatedBy: "Samwise Gamgee", IsFinished: false,
			InvoiceMode: "REG_HOURS",
		},
	}
}

func tasksSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp datasource.SchemaResponse
	NewTasksDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

func taskSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp datasource.SchemaResponse
	NewTaskDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

func readTasks(t *testing.T, f *taskFake, vals map[string]tftypes.Value) *datasource.ReadResponse {
	t.Helper()
	sch := tasksSchema(t)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch}}
	(&tasksDataSource{client: f}).Read(context.Background(),
		datasource.ReadRequest{Config: dsConfig(t, sch, vals)}, resp)
	return resp
}

func readTask(t *testing.T, f *taskFake, vals map[string]tftypes.Value) *datasource.ReadResponse {
	t.Helper()
	sch := taskSchema(t)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch}}
	(&taskDataSource{client: f}).Read(context.Background(),
		datasource.ReadRequest{Config: dsConfig(t, sch, vals)}, resp)
	return resp
}

func TestTasksDataSource_Metadata(t *testing.T) {
	for _, tc := range []struct {
		ds   datasource.DataSource
		want string
	}{{NewTasksDataSource(), "kala_tasks"}, {NewTaskDataSource(), "kala_task"}} {
		var resp datasource.MetadataResponse
		tc.ds.Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "kala"}, &resp)
		if resp.TypeName != tc.want {
			t.Errorf("TypeName = %q, want %q", resp.TypeName, tc.want)
		}
	}
}

// case_id addresses the endpoint; it is not an optional filter.
func TestTasksDataSource_CaseIDIsRequiredOnBoth(t *testing.T) {
	for name, sch := range map[string]schema.Schema{"kala_tasks": tasksSchema(t), "kala_task": taskSchema(t)} {
		attr, ok := sch.Attributes["case_id"]
		if !ok {
			t.Fatalf("%s: schema is missing case_id", name)
		}
		if !attr.IsRequired() {
			t.Errorf("%s: case_id must be Required", name)
		}
	}
}

func TestTasksDataSource_SchemaDocumentsWhyThereIsNoAccountWideRead(t *testing.T) {
	desc := strings.ToLower(tasksSchema(t).MarkdownDescription)
	for _, want := range []string{"required", "no account-wide", "for_each"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the schema description should mention %q; got: %s", want, desc)
		}
	}
}

func TestBuildTasksState_MapsAlwaysVisibleFields(t *testing.T) {
	got := buildTasksState(sampleTasks(), false, false)
	if len(got) != 2 {
		t.Fatalf("got %d tasks, want 2", len(got))
	}
	k := got[0]
	if k.ID.ValueInt64() != 5 || k.Name.ValueString() != "Secret task" {
		t.Errorf("identity = %d/%q", k.ID.ValueInt64(), k.Name.ValueString())
	}
	if k.CaseID.ValueInt64() != 2 || k.CaseNumber.ValueString() != "KA-2" {
		t.Errorf("case link = %d/%q", k.CaseID.ValueInt64(), k.CaseNumber.ValueString())
	}
	if k.StatusName.ValueString() != "Færdig" {
		t.Errorf("status_name = %q -- upstream language, not translated", k.StatusName.ValueString())
	}
	if !k.IsFinished.ValueBool() {
		t.Error("is_finished = false, want true")
	}
	if k.TimeAdded.ValueString() != "2026-09-02T07:19:03Z" {
		t.Errorf("time_added = %q, want RFC3339", k.TimeAdded.ValueString())
	}
	if k.TimeFinished.ValueString() != "2026-09-02T07:19:46Z" {
		t.Errorf("time_finished = %q", k.TimeFinished.ValueString())
	}
	if k.InvoiceMode.ValueString() != "REG_HOURS&SPECIAL" {
		t.Errorf("invoice_mode = %q", k.InvoiceMode.ValueString())
	}
	if !k.ImageRequired.ValueBool() {
		t.Error("image_required = false, want true")
	}
	if !got[1].Deadline.IsNull() || !got[1].TimeFinished.IsNull() {
		t.Error("unset timestamps must stay null, not become year 1")
	}
}

// FR7: assignee and authorship identify people.
func TestBuildTasksState_WithholdsPeopleFieldsByDefault(t *testing.T) {
	k := buildTasksState(sampleTasks(), false, false)[0]
	for name, v := range map[string]interface{ IsNull() bool }{
		"assignee_worker_nr": k.AssigneeWorkerNr, "created_by": k.CreatedBy,
		"finished_by": k.FinishedBy, "assigned_to_me": k.AssignedToMe,
	} {
		if !v.IsNull() {
			t.Errorf("%s must be null without include_contact_details", name)
		}
	}
}

func TestBuildTasksState_WithholdsFinancialsByDefault(t *testing.T) {
	k := buildTasksState(sampleTasks(), false, false)[0]
	for name, v := range map[string]interface{ IsNull() bool }{
		"registered_hours_total": k.RegisteredHoursTotal,
		"billed_hours":           k.BilledHours,
		"price_fixed":            k.PriceFixed,
	} {
		if !v.IsNull() {
			t.Errorf("%s must be null without include_financials", name)
		}
	}
}

func TestBuildTasksState_ExposesGatedFieldsWhenRequested(t *testing.T) {
	k := buildTasksState(sampleTasks(), true, true)[0]
	if k.AssigneeWorkerNr.ValueInt64() != 1 {
		t.Errorf("assignee_worker_nr = %d, want 1", k.AssigneeWorkerNr.ValueInt64())
	}
	if k.CreatedBy.ValueString() != "Frodo Baggins" || k.FinishedBy.ValueString() != "Frodo Baggins" {
		t.Errorf("authorship = %q/%q", k.CreatedBy.ValueString(), k.FinishedBy.ValueString())
	}
	if k.RegisteredHoursTotal.ValueInt64() != 4 || k.BilledHours.ValueInt64() != 3 {
		t.Errorf("hours = %d/%d, want 4/3", k.RegisteredHoursTotal.ValueInt64(), k.BilledHours.ValueInt64())
	}
	if k.PriceFixed.ValueInt64() != 550 {
		t.Errorf("price_fixed = %d, want 550", k.PriceFixed.ValueInt64())
	}
}

// An unassigned task is null even with the opt-in: there is nobody to name.
func TestBuildTasksState_UnassignedIsNullEvenWithOptIn(t *testing.T) {
	got := buildTasksState(sampleTasks(), true, true)
	if !got[1].AssigneeWorkerNr.IsNull() {
		t.Error("an unassigned task must have a null assignee even with the opt-in")
	}
}

func TestBuildTasksState_EmptyIsAnEmptySliceNotNil(t *testing.T) {
	if got := buildTasksState(nil, false, false); got == nil {
		t.Fatal("nil renders as null in state and produces a spurious diff")
	}
}

func TestTasksRead_ForwardsServerSideFilters(t *testing.T) {
	f := &taskFake{scan: client.TaskScan{Tasks: sampleTasks(), Total: 2, Fetched: 2, CaseTotal: 4, CaseFinished: 1}}
	resp := readTasks(t, f, map[string]tftypes.Value{
		"case_id":         tftypes.NewValue(tftypes.Number, 2),
		"search":          tftypes.NewValue(tftypes.String, "roof"),
		"name_contains":   tftypes.NewValue(tftypes.String, "gutter"),
		"only_unfinished": tftypes.NewValue(tftypes.Bool, true),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	if f.got.CaseID != 2 || f.got.Search != "roof" || f.got.NameContains != "gutter" || !f.got.OnlyUnfinished {
		t.Errorf("query = %+v", f.got)
	}
	var state tasksDataSourceModel
	resp.State.Get(context.Background(), &state)
	if state.CaseTotal.ValueInt64() != 4 || state.CaseFinished.ValueInt64() != 1 {
		t.Errorf("case counters = %d/%d, want 4/1", state.CaseTotal.ValueInt64(), state.CaseFinished.ValueInt64())
	}
}

func TestTasksRead_AssigneeFilterIsPassedToTheClient(t *testing.T) {
	f := &taskFake{scan: client.TaskScan{Tasks: sampleTasks()[:1], Total: 2, Fetched: 2}}
	readTasks(t, f, map[string]tftypes.Value{
		"case_id":            tftypes.NewValue(tftypes.Number, 2),
		"assignee_worker_nr": tftypes.NewValue(tftypes.Number, 1),
	})
	if f.got.AssigneeWorkerNr == nil || *f.got.AssigneeWorkerNr != 1 {
		t.Errorf("assignee not forwarded: %v", f.got.AssigneeWorkerNr)
	}
}

// A client-side assignee filter narrows the RESULT, not the READ.
func TestTasksRead_AssigneeFilterDoesNotMakeCompleteFalse(t *testing.T) {
	f := &taskFake{scan: client.TaskScan{Tasks: sampleTasks()[:1], Total: 2, Fetched: 2}}
	resp := readTasks(t, f, map[string]tftypes.Value{
		"case_id":            tftypes.NewValue(tftypes.Number, 2),
		"assignee_worker_nr": tftypes.NewValue(tftypes.Number, 1),
	})
	var state tasksDataSourceModel
	resp.State.Get(context.Background(), &state)
	if len(state.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(state.Tasks))
	}
	if !state.Complete.ValueBool() {
		t.Error("all records were received; a client-side filter must not report the read as partial")
	}
}

func TestTasksRead_IncompleteReadWarns(t *testing.T) {
	f := &taskFake{scan: client.TaskScan{Tasks: sampleTasks(), Total: 900, Fetched: 2}}
	resp := readTasks(t, f, map[string]tftypes.Value{"case_id": tftypes.NewValue(tftypes.Number, 2)})
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("a capped read must warn")
	}
}

func TestTasksRead_UnknownCaseSurfacesAsNotFound(t *testing.T) {
	f := &taskFake{err: client.ErrNotFound}
	resp := readTasks(t, f, map[string]tftypes.Value{"case_id": tftypes.NewValue(tftypes.Number, 999)})
	if !resp.Diagnostics.HasError() {
		t.Fatal("an unknown case must error")
	}
	if !strings.Contains(strings.ToLower(resp.Diagnostics.Errors()[0].Summary()), "not found") {
		t.Errorf("summary = %q", resp.Diagnostics.Errors()[0].Summary())
	}
}

func TestTasksRead_SurfacesClientError(t *testing.T) {
	f := &taskFake{err: errors.New("upstream exploded")}
	if resp := readTasks(t, f, map[string]tftypes.Value{
		"case_id": tftypes.NewValue(tftypes.Number, 2),
	}); !resp.Diagnostics.HasError() {
		t.Fatal("a client error must surface")
	}
}

// --- kala_task ------------------------------------------------------------

func TestTaskRead_ResolvesByID(t *testing.T) {
	f := &taskFake{scan: client.TaskScan{Tasks: sampleTasks(), Total: 2, Fetched: 2}}
	resp := readTask(t, f, map[string]tftypes.Value{
		"case_id": tftypes.NewValue(tftypes.Number, 2),
		"id":      tftypes.NewValue(tftypes.Number, 6),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	var state taskDataSourceModel
	resp.State.Get(context.Background(), &state)
	if state.Name.ValueString() != "Gutter" {
		t.Errorf("name = %q, want Gutter", state.Name.ValueString())
	}
}

// A name selector prefilters upstream via name_contains, then matches exactly.
func TestTaskRead_NameSelectorPrefiltersThenMatchesExactly(t *testing.T) {
	f := &taskFake{scan: client.TaskScan{Tasks: sampleTasks(), Total: 2, Fetched: 2}}
	resp := readTask(t, f, map[string]tftypes.Value{
		"case_id": tftypes.NewValue(tftypes.Number, 2),
		"name":    tftypes.NewValue(tftypes.String, "Gutter"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	if f.got.NameContains != "Gutter" {
		t.Errorf("name_contains = %q, want Gutter -- narrow upstream first", f.got.NameContains)
	}
	var state taskDataSourceModel
	resp.State.Get(context.Background(), &state)
	if state.ID.ValueInt64() != 6 {
		t.Errorf("resolved id = %d, want 6", state.ID.ValueInt64())
	}
}

func TestTaskRead_NoSelectorIsAnError(t *testing.T) {
	f := &taskFake{scan: client.TaskScan{Tasks: sampleTasks(), Total: 2, Fetched: 2}}
	resp := readTask(t, f, map[string]tftypes.Value{"case_id": tftypes.NewValue(tftypes.Number, 2)})
	if !resp.Diagnostics.HasError() {
		t.Fatal("a lookup with no selector must error")
	}
}

func TestTaskRead_BothSelectorsIsAnError(t *testing.T) {
	f := &taskFake{scan: client.TaskScan{Tasks: sampleTasks(), Total: 2, Fetched: 2}}
	resp := readTask(t, f, map[string]tftypes.Value{
		"case_id": tftypes.NewValue(tftypes.Number, 2),
		"id":      tftypes.NewValue(tftypes.Number, 5),
		"name":    tftypes.NewValue(tftypes.String, "Gutter"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("two selectors must error rather than silently preferring one")
	}
}

func TestTaskRead_AmbiguousNameIsAnError(t *testing.T) {
	dupes := sampleTasks()
	dupes[1].Name = dupes[0].Name
	f := &taskFake{scan: client.TaskScan{Tasks: dupes, Total: 2, Fetched: 2}}
	resp := readTask(t, f, map[string]tftypes.Value{
		"case_id": tftypes.NewValue(tftypes.Number, 2),
		"name":    tftypes.NewValue(tftypes.String, "Secret task"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("two tasks with the same name must error")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "2") {
		t.Errorf("the diagnostic should say how many matched, got: %s", resp.Diagnostics.Errors()[0].Detail())
	}
}

func TestTaskRead_AbsentFromPartialReadSaysIncomplete(t *testing.T) {
	f := &taskFake{scan: client.TaskScan{Tasks: sampleTasks(), Total: 900, Fetched: 2}}
	resp := readTask(t, f, map[string]tftypes.Value{
		"case_id": tftypes.NewValue(tftypes.Number, 2),
		"id":      tftypes.NewValue(tftypes.Number, 999),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("an inconclusive lookup must error")
	}
	if strings.Contains(strings.ToLower(resp.Diagnostics.Errors()[0].Summary()), "not found") {
		t.Error("a partial read must not report not-found")
	}
}

func TestProvider_RegistersTaskDataSources(t *testing.T) {
	want := map[string]bool{"kala_tasks": false, "kala_task": false}
	for _, mk := range (&kalaProvider{}).DataSources(context.Background()) {
		var resp datasource.MetadataResponse
		mk().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "kala"}, &resp)
		if _, tracked := want[resp.TypeName]; tracked {
			want[resp.TypeName] = true
		}
	}
	for name, ok := range want {
		if !ok {
			t.Errorf("%s is not registered on the provider", name)
		}
	}
}
