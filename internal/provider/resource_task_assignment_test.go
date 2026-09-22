package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	fwschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/pippiio/terraform-provider-kala/internal/client"
)

func newAssignmentResource(fi *fakeInternal) *taskAssignmentResource {
	return &taskAssignmentResource{clients: &providerClients{Web: &fakeClient{}, Internal: fi}}
}

func assignSchema(t *testing.T) fwschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewTaskAssignmentResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func assignValue(t *testing.T, m taskAssignmentResourceModel) tftypes.Value {
	t.Helper()
	typ := assignSchema(t).Type().TerraformType(context.Background())
	i64 := func(v types.Int64) tftypes.Value {
		switch {
		case v.IsUnknown():
			return tftypes.NewValue(tftypes.Number, tftypes.UnknownValue)
		case v.IsNull():
			return tftypes.NewValue(tftypes.Number, nil)
		}
		return tftypes.NewValue(tftypes.Number, v.ValueInt64())
	}
	set := tftypes.NewValue(tftypes.Set{ElementType: tftypes.Number}, nil)
	if !m.TaskIDs.IsNull() && !m.TaskIDs.IsUnknown() {
		var elems []tftypes.Value
		for _, e := range m.TaskIDs.Elements() {
			elems = append(elems, tftypes.NewValue(tftypes.Number, e.(types.Int64).ValueInt64()))
		}
		set = tftypes.NewValue(tftypes.Set{ElementType: tftypes.Number}, elems)
	}
	return tftypes.NewValue(typ.(tftypes.Object), map[string]tftypes.Value{
		"case_number":   tftypes.NewValue(tftypes.String, m.CaseNumber.ValueString()),
		"case_id":       i64(m.CaseID),
		"worker_number": i64(m.WorkerNumber),
		"task_ids":      set,
		"job_link_id":   i64(m.JobLinkID),
	})
}

func assignPlan(t *testing.T, m taskAssignmentResourceModel) tfsdk.Plan {
	return tfsdk.Plan{Schema: assignSchema(t), Raw: assignValue(t, m)}
}

func assignState(t *testing.T, m taskAssignmentResourceModel) tfsdk.State {
	return tfsdk.State{Schema: assignSchema(t), Raw: assignValue(t, m)}
}

func emptyAssignState(t *testing.T) tfsdk.State {
	return tfsdk.State{Schema: assignSchema(t), Raw: tftypes.Value{}}
}

func idSet(ids ...int64) types.Set {
	elems := make([]attr.Value, 0, len(ids))
	for _, id := range ids {
		elems = append(elems, types.Int64Value(id))
	}
	s, _ := types.SetValue(types.Int64Type, elems)
	return s
}

func plannedAssignment(ids ...int64) taskAssignmentResourceModel {
	return taskAssignmentResourceModel{
		CaseNumber: types.StringValue("KA-4"), CaseID: types.Int64Unknown(),
		WorkerNumber: types.Int64Value(1), TaskIDs: idSet(ids...),
		JobLinkID: types.Int64Unknown(),
	}
}

func existingAssignment(ids ...int64) taskAssignmentResourceModel {
	m := plannedAssignment(ids...)
	m.CaseID, m.JobLinkID = types.Int64Value(4), types.Int64Value(7)
	return m
}

// fakeWithTasks gives the case three items, so a set can be a proper subset.
func fakeWithTasks() *fakeInternal {
	fi := fakeWithCase()
	fi.linkWorker = 1
	for _, id := range []int64{1, 2, 3} {
		fi.tasks[id] = client.Task{ID: id, CaseID: 4, CaseNumber: "KA-4", Name: "Item"}
	}
	return fi
}

func TestTaskAssignment_Metadata(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewTaskAssignmentResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "kala"}, resp)
	if resp.TypeName != "kala_task_assignment" {
		t.Errorf("TypeName = %q, want kala_task_assignment", resp.TypeName)
	}
}

// The job link is SHARED, and a reader has to know that or they will declare
// two resources for one pair and watch them fight.
func TestTaskAssignment_SchemaWarnsTheLinkIsShared(t *testing.T) {
	d := strings.ToLower(assignSchema(t).MarkdownDescription)
	for _, want := range []string{"shared", "one per", "destroy"} {
		if !strings.Contains(d, want) {
			t.Errorf("schema description does not mention %q", want)
		}
	}
}

func TestCreateAssignment_LinksAndSetsTheWholeSet(t *testing.T) {
	fi := fakeWithTasks()
	r := newAssignmentResource(fi)

	resp := &resource.CreateResponse{State: emptyAssignState(t)}
	r.Create(context.Background(),
		resource.CreateRequest{Plan: assignPlan(t, plannedAssignment(1, 3))}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.ensureLinkCalls) != 1 || fi.ensureLinkCalls[0] != [2]int64{4, 1} {
		t.Errorf("EnsureJobLink calls = %v, want one for case 4 / worker 1", fi.ensureLinkCalls)
	}
	if len(fi.setChecklistCalls) != 1 || len(fi.setChecklistCalls[0]) != 2 {
		t.Errorf("the whole set must be sent once: %v", fi.setChecklistCalls)
	}

	var got taskAssignmentResourceModel
	resp.State.Get(context.Background(), &got)
	if got.JobLinkID.ValueInt64() != 7 {
		t.Errorf("job_link_id = %d, want 7 recorded from the link", got.JobLinkID.ValueInt64())
	}
	if len(got.TaskIDs.Elements()) != 2 {
		t.Errorf("task_ids = %v, want two", got.TaskIDs)
	}
}

// Whether EnsureJobLink returns an existing link or duplicates one is
// unverified upstream, so the outcome is checked rather than trusted.
func TestCreateAssignment_UnappliedWriteIsAnError(t *testing.T) {
	fi := fakeWithTasks()
	fi.setChecklistErr = nil
	// Reflect nothing: the write reports success and changes nothing.
	fi.tasks[1] = client.Task{ID: 1, CaseID: 4, CaseNumber: "KA-4"}
	fi.linkWorker = 99 // the fake will assign a DIFFERENT worker

	r := newAssignmentResource(fi)
	resp := &resource.CreateResponse{State: emptyAssignState(t)}
	r.Create(context.Background(),
		resource.CreateRequest{Plan: assignPlan(t, plannedAssignment(1))}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an assignment that did not take effect must fail read-back verification")
	}
	if !strings.Contains(strings.ToLower(diagsText(resp.Diagnostics)), "did not apply") {
		t.Errorf("the diagnostic must say the write did not apply: %s", diagsText(resp.Diagnostics))
	}
}

func TestUpdateAssignment_ReplacesTheSet(t *testing.T) {
	fi := fakeWithTasks()
	fi.tasks[1] = client.Task{ID: 1, CaseID: 4, CaseNumber: "KA-4", AssignedWorkerNrs: []int64{1}}
	r := newAssignmentResource(fi)

	state := existingAssignment(1)
	plan := existingAssignment(2, 3)

	resp := &resource.UpdateResponse{State: assignState(t, state)}
	r.Update(context.Background(),
		resource.UpdateRequest{Plan: assignPlan(t, plan), State: assignState(t, state)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %s", diagsText(resp.Diagnostics))
	}
	last := fi.setChecklistCalls[len(fi.setChecklistCalls)-1]
	if len(last) != 2 {
		t.Errorf("the whole replacement set must be sent: %v", last)
	}
}

// Read is the task list: Kala exposes no way to read a job link.
func TestReadAssignment_ReadsThroughTheTaskList(t *testing.T) {
	fi := fakeWithTasks()
	fi.tasks[2] = client.Task{ID: 2, CaseID: 4, CaseNumber: "KA-4", AssignedWorkerNrs: []int64{1}}
	fi.tasks[3] = client.Task{ID: 3, CaseID: 4, CaseNumber: "KA-4", AssignedWorkerNrs: []int64{1, 5}}
	r := newAssignmentResource(fi)

	resp := &resource.ReadResponse{State: assignState(t, existingAssignment(2))}
	r.Read(context.Background(),
		resource.ReadRequest{State: assignState(t, existingAssignment(2))}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %s", diagsText(resp.Diagnostics))
	}
	var got taskAssignmentResourceModel
	resp.State.Get(context.Background(), &got)
	if len(got.TaskIDs.Elements()) != 2 {
		t.Errorf("task_ids = %v, want items 2 and 3 refreshed from upstream", got.TaskIDs)
	}
}

// An assignment covering nothing has nothing left to describe.
func TestReadAssignment_NoTasksIsDrift(t *testing.T) {
	fi := fakeWithTasks() // nobody assigned
	r := newAssignmentResource(fi)

	resp := &resource.ReadResponse{State: assignState(t, existingAssignment(1))}
	r.Read(context.Background(),
		resource.ReadRequest{State: assignState(t, existingAssignment(1))}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %s", diagsText(resp.Diagnostics))
	}
	if !resp.State.Raw.IsNull() {
		t.Error("an assignment covering no tasks must be removed from state")
	}
}

// Detaching needs no job link id, which is what makes destroy work for an
// imported resource.
func TestDeleteAssignment_DetachesEachTaskAndWarns(t *testing.T) {
	fi := fakeWithTasks()
	fi.tasks[1] = client.Task{ID: 1, CaseID: 4, CaseNumber: "KA-4", AssignedWorkerNrs: []int64{1}}
	fi.tasks[3] = client.Task{ID: 3, CaseID: 4, CaseNumber: "KA-4", AssignedWorkerNrs: []int64{1}}
	r := newAssignmentResource(fi)

	resp := &resource.DeleteResponse{State: assignState(t, existingAssignment(1, 3))}
	r.Delete(context.Background(),
		resource.DeleteRequest{State: assignState(t, existingAssignment(1, 3))}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("destroy failed: %s", diagsText(resp.Diagnostics))
	}
	if len(fi.removedItems) != 2 {
		t.Errorf("each covered task must be detached: %v", fi.removedItems)
	}
	warnings := resp.Diagnostics.Warnings()
	if len(warnings) == 0 {
		t.Fatal("destroy must warn that the link itself remains")
	}
	if !strings.Contains(strings.ToLower(warnings[0].Detail()), "link remains") {
		t.Errorf("the warning must say the job link remains: %s", warnings[0].Detail())
	}
}

// A failed detach is an error, not a warning: reporting an unassignment that
// did not happen is the worst outcome.
func TestDeleteAssignment_FailedDetachIsAnError(t *testing.T) {
	fi := fakeWithTasks()
	fi.removeItemErr = errContext("kala refused")
	r := newAssignmentResource(fi)

	resp := &resource.DeleteResponse{State: assignState(t, existingAssignment(1))}
	r.Delete(context.Background(),
		resource.DeleteRequest{State: assignState(t, existingAssignment(1))}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed detach must fail the destroy")
	}
}

func TestImportAssignment_TakesCaseAndWorker(t *testing.T) {
	r := newAssignmentResource(fakeWithTasks())
	typ := assignSchema(t).Type().TerraformType(context.Background())
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: assignSchema(t), Raw: tftypes.NewValue(typ, nil)},
	}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "KA-4:1"}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %s", diagsText(resp.Diagnostics))
	}
	var got taskAssignmentResourceModel
	resp.State.Get(context.Background(), &got)
	if got.CaseNumber.ValueString() != "KA-4" || got.WorkerNumber.ValueInt64() != 1 {
		t.Errorf("imported %+v", got)
	}
}

func TestImportAssignment_MalformedIDIsAnError(t *testing.T) {
	for _, id := range []string{"KA-4", "1", "KA-4:", ":1", "KA-4:one", ""} {
		r := newAssignmentResource(fakeWithTasks())
		typ := assignSchema(t).Type().TerraformType(context.Background())
		resp := &resource.ImportStateResponse{
			State: tfsdk.State{Schema: assignSchema(t), Raw: tftypes.NewValue(typ, nil)},
		}
		r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("import id %q must be rejected", id)
		}
	}
}

func TestProvider_RegistersTaskAssignmentResource(t *testing.T) {
	var found bool
	for _, f := range New("test")().(*kalaProvider).Resources(context.Background()) {
		resp := &resource.MetadataResponse{}
		f().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "kala"}, resp)
		if resp.TypeName == "kala_task_assignment" {
			found = true
		}
	}
	if !found {
		t.Error("kala_task_assignment is not registered")
	}
}

// An imported resource knows only the case NUMBER, but reads key on the
// integer id. Read must resolve it, or import produces a resource that can
// never refresh.
func TestReadAssignment_ImportedResourceResolvesTheCaseID(t *testing.T) {
	fi := fakeWithTasks()
	fi.tasks[2] = client.Task{ID: 2, CaseID: 4, CaseNumber: "KA-4", AssignedWorkerNrs: []int64{1}}
	r := newAssignmentResource(fi)

	// As ImportState leaves it: no case_id, no job_link_id, no task_ids.
	imported := taskAssignmentResourceModel{
		CaseNumber: types.StringValue("KA-4"), CaseID: types.Int64Null(),
		WorkerNumber: types.Int64Value(1), TaskIDs: types.SetNull(types.Int64Type),
		JobLinkID: types.Int64Null(),
	}

	resp := &resource.ReadResponse{State: assignState(t, imported)}
	r.Read(context.Background(), resource.ReadRequest{State: assignState(t, imported)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %s", diagsText(resp.Diagnostics))
	}
	var got taskAssignmentResourceModel
	resp.State.Get(context.Background(), &got)
	if got.CaseID.ValueInt64() != 4 {
		t.Errorf("case_id = %d; an imported assignment must resolve it", got.CaseID.ValueInt64())
	}
	if len(got.TaskIDs.Elements()) != 1 {
		t.Errorf("task_ids = %v, want the assignment discovered upstream", got.TaskIDs)
	}
}

func TestAssignment_FailuresPropagate(t *testing.T) {
	for name, setup := range map[string]func(*fakeInternal){
		"case lookup":   func(f *fakeInternal) { f.getCaseErr = errContext("nope") },
		"link":          func(f *fakeInternal) { f.ensureLinkErr = errContext("nope") },
		"set checklist": func(f *fakeInternal) { f.setChecklistErr = errContext("nope") },
		"read back":     func(f *fakeInternal) { f.assignedErr = errContext("nope") },
	} {
		t.Run(name, func(t *testing.T) {
			fi := fakeWithTasks()
			setup(fi)
			r := newAssignmentResource(fi)

			resp := &resource.CreateResponse{State: emptyAssignState(t)}
			r.Create(context.Background(),
				resource.CreateRequest{Plan: assignPlan(t, plannedAssignment(1))}, resp)

			if !resp.Diagnostics.HasError() {
				t.Errorf("a %s failure must propagate", name)
			}
		})
	}
}

func TestReadAssignment_FailuresPropagate(t *testing.T) {
	for name, setup := range map[string]func(*fakeInternal){
		"case lookup": func(f *fakeInternal) { f.getCaseErr = errContext("nope") },
		"assignment":  func(f *fakeInternal) { f.assignedErr = errContext("nope") },
	} {
		t.Run(name, func(t *testing.T) {
			fi := fakeWithTasks()
			setup(fi)
			r := newAssignmentResource(fi)

			m := existingAssignment(1)
			if name == "case lookup" {
				m.CaseID = types.Int64Null() // force the resolve path
			}
			resp := &resource.ReadResponse{State: assignState(t, m)}
			r.Read(context.Background(), resource.ReadRequest{State: assignState(t, m)}, resp)

			if !resp.Diagnostics.HasError() {
				t.Errorf("a %s failure must propagate", name)
			}
		})
	}
}

func TestUpdateAssignment_FailurePropagates(t *testing.T) {
	fi := fakeWithTasks()
	fi.ensureLinkErr = errContext("nope")
	r := newAssignmentResource(fi)

	state := existingAssignment(1)
	plan := existingAssignment(2)

	resp := &resource.UpdateResponse{State: assignState(t, state)}
	r.Update(context.Background(),
		resource.UpdateRequest{Plan: assignPlan(t, plan), State: assignState(t, state)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed update must report an error")
	}
}

func TestAssignment_WithoutInternalCredentialsIsAClearError(t *testing.T) {
	r := &taskAssignmentResource{clients: &providerClients{Web: &fakeClient{}}}
	m := existingAssignment(1)

	create := &resource.CreateResponse{State: emptyAssignState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: assignPlan(t, m)}, create)
	read := &resource.ReadResponse{State: assignState(t, m)}
	r.Read(context.Background(), resource.ReadRequest{State: assignState(t, m)}, read)
	update := &resource.UpdateResponse{State: assignState(t, m)}
	r.Update(context.Background(),
		resource.UpdateRequest{Plan: assignPlan(t, m), State: assignState(t, m)}, update)
	del := &resource.DeleteResponse{State: assignState(t, m)}
	r.Delete(context.Background(), resource.DeleteRequest{State: assignState(t, m)}, del)

	for name, n := range map[string]int{
		"Create": len(create.Diagnostics), "Read": len(read.Diagnostics),
		"Update": len(update.Diagnostics), "Delete": len(del.Diagnostics),
	} {
		if n == 0 {
			t.Errorf("%s without internal credentials must be a clear error", name)
		}
	}
}

func TestAssignment_UndecodablePlanOrStateStopsTheOperation(t *testing.T) {
	fi := fakeWithTasks()
	r := newAssignmentResource(fi)
	broken := tfsdk.Plan{Schema: assignSchema(t), Raw: tftypes.Value{}}
	brokenState := tfsdk.State{Schema: assignSchema(t), Raw: tftypes.Value{}}

	create := &resource.CreateResponse{State: emptyAssignState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: broken}, create)
	read := &resource.ReadResponse{State: emptyAssignState(t)}
	r.Read(context.Background(), resource.ReadRequest{State: brokenState}, read)
	update := &resource.UpdateResponse{State: emptyAssignState(t)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: broken, State: brokenState}, update)
	del := &resource.DeleteResponse{State: emptyAssignState(t)}
	r.Delete(context.Background(), resource.DeleteRequest{State: brokenState}, del)

	for name, n := range map[string]int{
		"Create": len(create.Diagnostics), "Read": len(read.Diagnostics),
		"Update": len(update.Diagnostics), "Delete": len(del.Diagnostics),
	} {
		if n == 0 {
			t.Errorf("%s accepted an undecodable plan/state", name)
		}
	}
	if len(fi.ensureLinkCalls) > 0 {
		t.Error("an undecodable plan reached an upstream write")
	}
}

func TestAssignment_Configure(t *testing.T) {
	r := &taskAssignmentResource{}
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

// A null set is not an empty set: it is the absence of a value, and reading it
// as "assign nobody to nothing" would be a different instruction.
func TestTaskIDsOf_NullAndUnknownAreNil(t *testing.T) {
	var diags diag.Diagnostics
	if ids := taskIDsOf(context.Background(), types.SetNull(types.Int64Type), &diags); ids != nil {
		t.Errorf("null set = %v, want nil", ids)
	}
	if ids := taskIDsOf(context.Background(), types.SetUnknown(types.Int64Type), &diags); ids != nil {
		t.Errorf("unknown set = %v, want nil", ids)
	}
	if diags.HasError() {
		t.Errorf("neither is an error: %v", diags)
	}
}
