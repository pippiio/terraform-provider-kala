package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	fwschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

func newTaskResource(fi *fakeInternal) *taskResource {
	return &taskResource{clients: &providerClients{Web: &fakeClient{}, Internal: fi}}
}

func taskResSchema(t *testing.T) fwschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewTaskResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func taskResValue(t *testing.T, m taskResourceModel) tftypes.Value {
	t.Helper()
	typ := taskResSchema(t).Type().TerraformType(context.Background())
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
		"id": i64(m.ID), "case_number": str(m.CaseNumber), "case_id": i64(m.CaseID),
		"name": str(m.Name), "description": str(m.Description), "deadline": str(m.Deadline),
		"note_required": bl(m.NoteRequired), "image_required": bl(m.ImageRequired),
		"invoice_mode": str(m.InvoiceMode), "price_fixed": i64(m.PriceFixed),
		"is_finished": bl(m.IsFinished), "assignee_worker_number": i64(m.AssigneeWorkerNr),
	})
}

func taskResPlan(t *testing.T, m taskResourceModel) tfsdk.Plan {
	return tfsdk.Plan{Schema: taskResSchema(t), Raw: taskResValue(t, m)}
}

func taskResState(t *testing.T, m taskResourceModel) tfsdk.State {
	return tfsdk.State{Schema: taskResSchema(t), Raw: taskResValue(t, m)}
}

func emptyTaskResState(t *testing.T) tfsdk.State {
	return tfsdk.State{Schema: taskResSchema(t), Raw: tftypes.Value{}}
}

func plannedTask() taskResourceModel {
	return taskResourceModel{
		ID: types.Int64Unknown(), CaseNumber: types.StringValue("KA-4"),
		CaseID: types.Int64Unknown(), Name: types.StringValue("Mount gutter"),
		Description: types.StringNull(), Deadline: types.StringValue("2026-09-30T15:11:32Z"),
		NoteRequired: types.BoolValue(true), ImageRequired: types.BoolValue(false),
		InvoiceMode: types.StringValue("REG_HOURS&STANDARD"), PriceFixed: types.Int64Value(500),
		IsFinished: types.BoolUnknown(), AssigneeWorkerNr: types.Int64Unknown(),
	}
}

func existingTask() taskResourceModel {
	m := plannedTask()
	m.ID, m.CaseID = types.Int64Value(9), types.Int64Value(4)
	m.Description = types.StringValue("")
	m.IsFinished, m.AssigneeWorkerNr = types.BoolValue(false), types.Int64Null()
	return m
}

func fakeWithCase() *fakeInternal {
	fi := newFakeInternal()
	fi.cases["KA-4"] = client.CaseDetail{Case: client.Case{ID: 4, Number: "KA-4"}}
	return fi
}

func TestTaskResource_Metadata(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewTaskResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "kala"}, resp)
	if resp.TypeName != "kala_task" {
		t.Errorf("TypeName = %q, want kala_task", resp.TypeName)
	}
}

// The line this resource draws: it manages what the work IS, not whether it is
// done. The schema must say so where a reader meets it.
func TestTaskResource_SchemaDocumentsTheDefinitionOnlyBoundary(t *testing.T) {
	d := strings.ToLower(taskResSchema(t).MarkdownDescription)
	for _, want := range []string{"irreversible", "read-only", "is_finished"} {
		if !strings.Contains(d, want) {
			t.Errorf("schema description does not mention %q", want)
		}
	}
}

func TestCreateTask_ResolvesTheCaseAndRecordsIdentity(t *testing.T) {
	fi := fakeWithCase()
	r := newTaskResource(fi)

	resp := &resource.CreateResponse{State: emptyTaskResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: taskResPlan(t, plannedTask())}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if !fi.createTaskCalled {
		t.Fatal("CreateTask was not called")
	}
	// Writes address the case by string number, reads by integer id. Both must
	// reach the client or the read-back cannot find the item.
	if fi.taskIn.CaseNumber != "KA-4" || fi.taskIn.CaseID != 4 {
		t.Errorf("client got caseNumber=%q caseID=%d, want KA-4 / 4",
			fi.taskIn.CaseNumber, fi.taskIn.CaseID)
	}

	var got taskResourceModel
	resp.State.Get(context.Background(), &got)
	if got.ID.ValueInt64() != 9 {
		t.Errorf("id = %d, want the allocated 9", got.ID.ValueInt64())
	}
	if got.CaseID.ValueInt64() != 4 {
		t.Errorf("case_id = %d; it is resolved once at create and stored", got.CaseID.ValueInt64())
	}
}

// the item exists upstream and Kala cannot delete it.
func TestCreateTask_FailureAfterCreationStillRecordsState(t *testing.T) {
	fi := fakeWithCase()
	fi.createTaskErr = errContext("read-back failed")
	r := newTaskResource(fi)

	resp := &resource.CreateResponse{State: emptyTaskResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: taskResPlan(t, plannedTask())}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed create must report an error")
	}
	var got taskResourceModel
	resp.State.Get(context.Background(), &got)
	if got.ID.ValueInt64() != 9 {
		t.Fatalf("id = %d; the item exists and cannot be deleted, so it must be in state",
			got.ID.ValueInt64())
	}
}

// The reason is_finished is Computed. A worker ticking a task off in
// Kala must not produce a diff, or every apply would fight them.
func TestReadTask_CompletedUpstreamProducesNoDiff(t *testing.T) {
	fi := fakeWithCase()
	fi.tasks[9] = client.Task{
		ID: 9, CaseID: 4, CaseNumber: "KA-4", Name: "Mount gutter",
		NoteRequired: true, InvoiceMode: "REG_HOURS&STANDARD",
		IsFinished: true, // someone completed it
	}
	r := newTaskResource(fi)

	resp := &resource.ReadResponse{State: taskResState(t, existingTask())}
	r.Read(context.Background(), resource.ReadRequest{State: taskResState(t, existingTask())}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %s", diagsText(resp.Diagnostics))
	}
	var got taskResourceModel
	resp.State.Get(context.Background(), &got)
	if !got.IsFinished.ValueBool() {
		t.Error("is_finished must be refreshed from upstream")
	}
	if got.Name.ValueString() != "Mount gutter" {
		t.Errorf("the managed definition must be unchanged by completion: %q", got.Name.ValueString())
	}
}

func TestReadTask_MissingIsDrift(t *testing.T) {
	fi := fakeWithCase()
	r := newTaskResource(fi)

	resp := &resource.ReadResponse{State: taskResState(t, existingTask())}
	r.Read(context.Background(), resource.ReadRequest{State: taskResState(t, existingTask())}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a missing task is drift, not an error: %s", diagsText(resp.Diagnostics))
	}
	if !resp.State.Raw.IsNull() {
		t.Error("state should have been removed for a task that no longer exists")
	}
}

// UpdateChecklistItem replaces the whole record, so every managed field must
// travel or the write blanks it.
func TestUpdateTask_SendsTheWholeRecord(t *testing.T) {
	fi := fakeWithCase()
	fi.tasks[9] = client.Task{ID: 9, CaseID: 4, CaseNumber: "KA-4", Name: "Old"}
	r := newTaskResource(fi)

	state := existingTask()
	plan := existingTask()
	plan.Name = types.StringValue("Mount gutter, north side")

	resp := &resource.UpdateResponse{State: taskResState(t, state)}
	r.Update(context.Background(),
		resource.UpdateRequest{Plan: taskResPlan(t, plan), State: taskResState(t, state)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %s", diagsText(resp.Diagnostics))
	}
	if !fi.updateTaskCalled {
		t.Fatal("UpdateTask was not called")
	}
	in := fi.taskIn
	if in.Name != "Mount gutter, north side" || in.InvoiceMode == "" || !in.NoteRequired {
		t.Errorf("update sent a partial record, which blanks fields upstream: %+v", in)
	}
	if in.CaseNumber != "KA-4" || in.CaseID != 4 {
		t.Errorf("both identifiers must travel: %+v", in)
	}
}

// Deadlines are second-precision because Kala does not round-trip finer.
func TestTaskDeadline_SubSecondInputIsTruncated(t *testing.T) {
	fi := fakeWithCase()
	r := newTaskResource(fi)

	plan := plannedTask()
	plan.Deadline = types.StringValue("2026-09-30T15:11:32.375Z")

	resp := &resource.CreateResponse{State: emptyTaskResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: taskResPlan(t, plan)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if fi.taskIn.Deadline == nil {
		t.Fatal("no deadline reached the client")
	}
	want := time.Date(2026, 9, 30, 15, 11, 32, 0, time.UTC)
	if !fi.taskIn.Deadline.UTC().Equal(want) {
		t.Errorf("deadline = %s, want %s -- sub-second precision cannot round-trip",
			fi.taskIn.Deadline.UTC(), want)
	}
}

func TestCreateTask_InvalidDeadlineIsAClearError(t *testing.T) {
	fi := fakeWithCase()
	r := newTaskResource(fi)

	plan := plannedTask()
	plan.Deadline = types.StringValue("next Tuesday")

	resp := &resource.CreateResponse{State: emptyTaskResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: taskResPlan(t, plan)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an unparseable deadline must be rejected before any write")
	}
	if fi.createTaskCalled {
		t.Error("a bad deadline reached an upstream write")
	}
}

// A case that does not exist must fail before creating anything.
func TestCreateTask_UnknownCaseFailsBeforeWriting(t *testing.T) {
	fi := newFakeInternal() // no cases
	r := newTaskResource(fi)

	resp := &resource.CreateResponse{State: emptyTaskResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: taskResPlan(t, plannedTask())}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a task on an unknown case must fail")
	}
	if fi.createTaskCalled {
		t.Error("the item was created despite the case being unresolvable")
	}
}

// A checklist item has no archive and no deactivation. Destroy writes
// nothing and says so.
func TestDeleteTask_WritesNothingUpstreamAndWarns(t *testing.T) {
	fi := fakeWithCase()
	fi.tasks[9] = client.Task{ID: 9, CaseID: 4, CaseNumber: "KA-4", Name: "Mount gutter"}
	r := newTaskResource(fi)

	resp := &resource.DeleteResponse{State: taskResState(t, existingTask())}
	r.Delete(context.Background(), resource.DeleteRequest{State: taskResState(t, existingTask())}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("destroy must not fail: %s", diagsText(resp.Diagnostics))
	}
	if fi.createTaskCalled || fi.updateTaskCalled {
		t.Error("destroy issued an upstream write; a checklist item has no off switch")
	}
	warnings := resp.Diagnostics.Warnings()
	if len(warnings) == 0 {
		t.Fatal("destroy must warn that the item remains upstream")
	}
	text := strings.ToLower(warnings[0].Summary() + " " + warnings[0].Detail())
	if !strings.Contains(text, "mount gutter") && !strings.Contains(text, "ka-4") {
		t.Errorf("the warning must name the item left behind: %s", text)
	}
}

// Import needs BOTH identifiers: the item id, and the case it belongs to.
func TestImportTask_TakesCaseAndItemID(t *testing.T) {
	r := newTaskResource(fakeWithCase())
	typ := taskResSchema(t).Type().TerraformType(context.Background())
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: taskResSchema(t), Raw: tftypes.NewValue(typ, nil)},
	}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "KA-4:9"}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %s", diagsText(resp.Diagnostics))
	}
	var got taskResourceModel
	resp.State.Get(context.Background(), &got)
	if got.CaseNumber.ValueString() != "KA-4" || got.ID.ValueInt64() != 9 {
		t.Errorf("imported case=%q id=%d, want KA-4 / 9",
			got.CaseNumber.ValueString(), got.ID.ValueInt64())
	}
}

func TestImportTask_MalformedIDIsAClearError(t *testing.T) {
	for _, id := range []string{"9", "KA-4", "KA-4:", ":9", "KA-4:nine", ""} {
		r := newTaskResource(fakeWithCase())
		typ := taskResSchema(t).Type().TerraformType(context.Background())
		resp := &resource.ImportStateResponse{
			State: tfsdk.State{Schema: taskResSchema(t), Raw: tftypes.NewValue(typ, nil)},
		}
		r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("import id %q must be rejected; a task needs both its case and its id", id)
		}
	}
}

func TestProvider_RegistersTaskResource(t *testing.T) {
	var found bool
	for _, f := range New("test")().(*kalaProvider).Resources(context.Background()) {
		resp := &resource.MetadataResponse{}
		f().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "kala"}, resp)
		if resp.TypeName == "kala_task" {
			found = true
		}
	}
	if !found {
		t.Error("kala_task is not registered; an unregistered resource is unreachable")
	}
}

// Absent from an INCOMPLETE read proves nothing. Treating it as drift would
// drop a live item from state and create a duplicate on the next apply --
// which Kala could then never delete.
func TestReadTask_UnprovenAbsenceKeepsStateAndErrors(t *testing.T) {
	fi := fakeWithCase()
	// One item on the case, but the scan reports more exist than it returned.
	fi.tasks[1] = client.Task{ID: 1, CaseID: 4, CaseNumber: "KA-4", Name: "Another"}
	fi.truncateTasks = true
	r := newTaskResource(fi)

	resp := &resource.ReadResponse{State: taskResState(t, existingTask())}
	r.Read(context.Background(), resource.ReadRequest{State: taskResState(t, existingTask())}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an unprovable absence must be an error, not silent drift")
	}
	if resp.State.Raw.IsNull() {
		t.Error("state was removed on an UNPROVEN absence; that duplicates a live item")
	}
}

func TestReadTask_ListFailurePropagates(t *testing.T) {
	fi := fakeWithCase()
	fi.listTasksErr = errContext("upstream exploded")
	r := newTaskResource(fi)

	resp := &resource.ReadResponse{State: taskResState(t, existingTask())}
	r.Read(context.Background(), resource.ReadRequest{State: taskResState(t, existingTask())}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a read failure must be an error, not drift")
	}
}

func TestUpdateTask_FailurePropagates(t *testing.T) {
	fi := fakeWithCase()
	fi.tasks[9] = client.Task{ID: 9, CaseID: 4, CaseNumber: "KA-4"}
	fi.updateTaskErr = errContext("kala refused")
	r := newTaskResource(fi)

	state := existingTask()
	plan := existingTask()
	plan.Name = types.StringValue("Changed")

	resp := &resource.UpdateResponse{State: taskResState(t, state)}
	r.Update(context.Background(),
		resource.UpdateRequest{Plan: taskResPlan(t, plan), State: taskResState(t, state)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed update must report an error")
	}
}

func TestUpdateTask_InvalidDeadlineIsRejectedBeforeWriting(t *testing.T) {
	fi := fakeWithCase()
	fi.tasks[9] = client.Task{ID: 9, CaseID: 4, CaseNumber: "KA-4"}
	r := newTaskResource(fi)

	state := existingTask()
	plan := existingTask()
	plan.Deadline = types.StringValue("whenever")

	resp := &resource.UpdateResponse{State: taskResState(t, state)}
	r.Update(context.Background(),
		resource.UpdateRequest{Plan: taskResPlan(t, plan), State: taskResState(t, state)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an unparseable deadline must be rejected")
	}
	if fi.updateTaskCalled {
		t.Error("a bad deadline reached an upstream write")
	}
}

// A task whose case has an assignee reads it back; the attribute is Computed,
// so it must be populated rather than left null.
func TestReadTask_AssigneeIsSurfacedReadOnly(t *testing.T) {
	fi := fakeWithCase()
	nr := int64(3)
	fi.tasks[9] = client.Task{
		ID: 9, CaseID: 4, CaseNumber: "KA-4", Name: "Mount gutter",
		AssigneeWorkerNr: &nr,
	}
	r := newTaskResource(fi)

	resp := &resource.ReadResponse{State: taskResState(t, existingTask())}
	r.Read(context.Background(), resource.ReadRequest{State: taskResState(t, existingTask())}, resp)

	var got taskResourceModel
	resp.State.Get(context.Background(), &got)
	if got.AssigneeWorkerNr.ValueInt64() != 3 {
		t.Errorf("assignee = %v, want 3 surfaced read-only", got.AssigneeWorkerNr)
	}
}

func TestTaskResource_WithoutInternalCredentialsIsAClearError(t *testing.T) {
	r := &taskResource{clients: &providerClients{Web: &fakeClient{}}}
	m := existingTask()

	create := &resource.CreateResponse{State: emptyTaskResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: taskResPlan(t, m)}, create)
	read := &resource.ReadResponse{State: taskResState(t, m)}
	r.Read(context.Background(), resource.ReadRequest{State: taskResState(t, m)}, read)
	update := &resource.UpdateResponse{State: taskResState(t, m)}
	r.Update(context.Background(),
		resource.UpdateRequest{Plan: taskResPlan(t, m), State: taskResState(t, m)}, update)

	for name, d := range map[string]int{
		"Create": len(create.Diagnostics), "Read": len(read.Diagnostics),
		"Update": len(update.Diagnostics),
	} {
		if d == 0 {
			t.Errorf("%s without internal credentials must be a clear error", name)
		}
	}
}

func TestTaskResource_UndecodablePlanOrStateStopsTheOperation(t *testing.T) {
	fi := fakeWithCase()
	r := newTaskResource(fi)
	broken := tfsdk.Plan{Schema: taskResSchema(t), Raw: tftypes.Value{}}
	brokenState := tfsdk.State{Schema: taskResSchema(t), Raw: tftypes.Value{}}

	create := &resource.CreateResponse{State: emptyTaskResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: broken}, create)
	read := &resource.ReadResponse{State: emptyTaskResState(t)}
	r.Read(context.Background(), resource.ReadRequest{State: brokenState}, read)
	update := &resource.UpdateResponse{State: emptyTaskResState(t)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: broken, State: brokenState}, update)
	del := &resource.DeleteResponse{State: emptyTaskResState(t)}
	r.Delete(context.Background(), resource.DeleteRequest{State: brokenState}, del)

	for name, n := range map[string]int{
		"Create": len(create.Diagnostics), "Read": len(read.Diagnostics),
		"Update": len(update.Diagnostics), "Delete": len(del.Diagnostics),
	} {
		if n == 0 {
			t.Errorf("%s accepted an undecodable plan/state", name)
		}
	}
	if fi.createTaskCalled || fi.updateTaskCalled {
		t.Error("an undecodable plan reached an upstream write")
	}
}

func TestTaskResource_Configure(t *testing.T) {
	r := &taskResource{}
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
}

// A task need not have a deadline. Null must travel as "no deadline", not as
// a zero time, which would write 0001-01-01 upstream.
func TestCreateTask_NoDeadlineIsNull(t *testing.T) {
	fi := fakeWithCase()
	r := newTaskResource(fi)

	plan := plannedTask()
	plan.Deadline = types.StringNull()

	resp := &resource.CreateResponse{State: emptyTaskResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: taskResPlan(t, plan)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if fi.taskIn.Deadline != nil {
		t.Errorf("deadline = %v, want nil when unset", fi.taskIn.Deadline)
	}
	var got taskResourceModel
	resp.State.Get(context.Background(), &got)
	if !got.Deadline.IsNull() {
		t.Errorf("deadline = %v, want null in state", got.Deadline)
	}
}

// The partial-create path must leave nothing unknown in state: the framework
// treats an unknown value after apply as a provider error, and the next Read
// is what fills these in honestly.
func TestCreateTask_PartialCreateResolvesUnknownsToNull(t *testing.T) {
	fi := fakeWithCase()
	fi.createTaskErr = errContext("read-back failed")
	r := newTaskResource(fi)

	plan := plannedTask()
	plan.Description = types.StringUnknown()
	plan.InvoiceMode = types.StringUnknown()
	plan.Deadline = types.StringUnknown()

	resp := &resource.CreateResponse{State: emptyTaskResState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: taskResPlan(t, plan)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed create must report an error")
	}
	var got taskResourceModel
	resp.State.Get(context.Background(), &got)
	for name, v := range map[string]bool{
		"description":  got.Description.IsUnknown(),
		"invoice_mode": got.InvoiceMode.IsUnknown(),
		"deadline":     got.Deadline.IsUnknown(),
		"is_finished":  got.IsFinished.IsUnknown(),
	} {
		if v {
			t.Errorf("%s reached state unknown; the framework treats that as a provider error", name)
		}
	}
	if got.ID.ValueInt64() != 9 {
		t.Error("the allocated id must still be recorded")
	}
}
