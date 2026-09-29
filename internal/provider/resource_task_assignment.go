// Story: kala_task_assignment resource
//
// Input:  a case, a worker, and the set of tasks they should be assigned to.
// Process:
//   1. Ensure the (worker, case) job link exists, and keep its id.
//   2. Replace the covered set via SetJobLinkChecklist, which takes the whole
//      array -- a declarative set replacement, which is what Terraform wants.
//   3. VERIFY by read-back through the task list. Whether EnsureJobLink
//      returns an existing link or creates a duplicate is unverified upstream,
//      so the outcome is checked rather than trusted.
//   4. Destroy detaches each covered item individually, which needs no job
//      link id -- so it works even for a resource brought in by import.
//
// Why this is a resource and not an attribute on kala_task: the job link is
// SHARED. A worker on two items of one case has ONE link covering both, so two
// kala_task resources carrying assignment would overwrite each other's
// membership on every apply. Modelling the link makes the sharing visible in
// configuration instead of a race inside the provider.
//
// Output: Terraform state carrying the link and the set it covers.
//
// Dependencies: client.InternalClient (GetCase, EnsureJobLink,
//               SetJobLinkChecklist, RemoveJobLinkChecklistItem, AssignedTaskIDs).
// Side effects: creates and mutates real assignments.

package provider

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/pippiio/terraform-provider-kala/internal/client"
)

func NewTaskAssignmentResource() resource.Resource { return &taskAssignmentResource{} }

type taskAssignmentResource struct {
	clients *providerClients
}

type taskAssignmentResourceModel struct {
	CaseNumber   types.String `tfsdk:"case_number"`
	CaseID       types.Int64  `tfsdk:"case_id"`
	WorkerNumber types.Int64  `tfsdk:"worker_number"`
	TaskIDs      types.Set    `tfsdk:"task_ids"`
	JobLinkID    types.Int64  `tfsdk:"job_link_id"`
}

func (r *taskAssignmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_task_assignment"
}

func (r *taskAssignmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Assigns one employee to a set of tasks on one case.\n\n" +
			"Kala models assignment as a **job link** between a worker and a case, which then " +
			"covers a set of checklist items. That link is **shared**: an employee assigned to " +
			"two tasks on the same case has one link covering both. This resource represents " +
			"the link, so declare **one per (case, employee) pair** — two resources for the " +
			"same pair would overwrite each other on every apply.\n\n" +
			"`terraform destroy` detaches the employee from each task it covers. Kala exposes " +
			"no way to remove the underlying link itself, so the link may remain with nothing " +
			"assigned to it.\n\n" +
			"**Assignment is not access.** On a restricted case the employee must also be on the " +
			"case's access list, or they could not see the tasks. Create and update therefore fail " +
			"— before anything is written — when the case is restricted and the employee has no " +
			"access; refresh warns if access is revoked later. Grant access in Kala or with " +
			"`kala_case_access`, and when that resource is in the same configuration, reference it " +
			"with `depends_on` so the access exists before the assignment is made.",
		Attributes: map[string]schema.Attribute{
			"case_number": schema.StringAttribute{
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "The case, e.g. `KA-1`. A job link belongs to one case, so " +
					"changing this replaces the resource.",
			},
			"case_id": schema.Int64Attribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
				MarkdownDescription: "The case's integer id, resolved once. Reads key on it.",
			},
			"worker_number": schema.Int64Attribute{
				Required:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
				MarkdownDescription: "The employee number being assigned. Changing it replaces " +
					"the resource — a link belongs to one employee.",
			},
			"task_ids": schema.SetAttribute{
				Required:    true,
				ElementType: types.Int64Type,
				MarkdownDescription: "The checklist item ids this employee is assigned to. This " +
					"is the COMPLETE set for this (case, employee) pair: ids removed from it " +
					"are detached on the next apply.",
			},
			"job_link_id": schema.Int64Attribute{
				Computed: true,
				MarkdownDescription: "Kala's job link id. Read-only, and recorded because the " +
					"endpoint that sets the covered items requires it while the one that " +
					"detaches an item does not.",
			},
		},
	}
}

func (r *taskAssignmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	if c, ok := req.ProviderData.(*providerClients); ok {
		r.clients = c
	}
}

// taskIDsOf reads the set attribute into a sorted slice.
//
// Sorted so that comparisons and diagnostics are stable; a Terraform set has
// no order, and neither does the upstream array.
func taskIDsOf(ctx context.Context, s types.Set, diags *diag.Diagnostics) []int64 {
	if s.IsNull() || s.IsUnknown() {
		return nil
	}
	var ids []int64
	diags.Append(s.ElementsAs(ctx, &ids, false)...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// taskIDSet renders ids as a Terraform set, sorted for stable diagnostics.
func taskIDSet(ids []int64) types.Set {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	elems := make([]attr.Value, 0, len(ids))
	for _, id := range ids {
		elems = append(elems, types.Int64Value(id))
	}
	// The element type is fixed by the schema, so this cannot fail; the
	// diagnostics are discarded rather than plumbed through every caller.
	set, _ := types.SetValue(types.Int64Type, elems)
	return set
}

func (r *taskAssignmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan taskAssignmentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	access, err := internal.GetCaseAccess(ctx, plan.CaseNumber.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Could not find the case for this assignment",
			fmt.Sprintf("Case %s could not be read, so nothing was assigned.\n\nError: %s",
				plan.CaseNumber.ValueString(), err.Error()),
		)
		return
	}
	if !requireCaseAccess(access, plan.WorkerNumber.ValueInt64(), plan.CaseNumber.ValueString(), &resp.Diagnostics) {
		return
	}
	plan.CaseID = types.Int64Value(access.CaseID)

	wanted := taskIDsOf(ctx, plan.TaskIDs, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if !r.converge(ctx, internal, &plan, wanted, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	tflog.Debug(ctx, "assigned worker to tasks", map[string]any{
		"worker": plan.WorkerNumber.ValueInt64(), "case": plan.CaseNumber.ValueString(),
	})
}

// converge ensures the link exists, sets the covered set, and VERIFIES it.
//
// The verification is not ceremony. Whether EnsureJobLink returns an existing
// link or creates a duplicate is unverified upstream, so a wrong assumption
// must fail loudly here rather than silently leave the wrong worker assigned.
func (r *taskAssignmentResource) converge(
	ctx context.Context, c client.InternalClient,
	m *taskAssignmentResourceModel, wanted []int64, diags *diag.Diagnostics,
) bool {
	caseID := m.CaseID.ValueInt64()
	worker := m.WorkerNumber.ValueInt64()

	link, err := c.EnsureJobLink(ctx, caseID, worker)
	if err != nil {
		diags.AddError(
			"Could not link the employee to the case",
			fmt.Sprintf("Employee %d on case %s.\n\nError: %s",
				worker, m.CaseNumber.ValueString(), err.Error()),
		)
		return false
	}
	m.JobLinkID = types.Int64Value(link.ID)

	if err := c.SetJobLinkChecklist(ctx, link.ID, wanted); err != nil {
		diags.AddError(
			"Could not set the tasks this employee is assigned to",
			fmt.Sprintf("Employee %d on case %s.\n\nError: %s",
				worker, m.CaseNumber.ValueString(), err.Error()),
		)
		return false
	}

	got, err := c.AssignedTaskIDs(ctx, caseID, worker)
	if err != nil {
		diags.AddError(
			"The assignment could not be read back",
			fmt.Sprintf("Employee %d on case %s. The write may or may not have taken effect.\n\n"+
				"Error: %s", worker, m.CaseNumber.ValueString(), err.Error()),
		)
		return false
	}
	if !sameIDs(wanted, got) {
		diags.AddError(
			"The assignment did not take effect",
			fmt.Sprintf(
				"Employee %d on case %s was expected to cover tasks %v, but Kala reports %v.\n\n"+
					"The write reported success and did not apply.",
				worker, m.CaseNumber.ValueString(), wanted, got),
		)
		return false
	}

	m.TaskIDs = taskIDSet(got)
	return true
}

func sameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (r *taskAssignmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state taskAssignmentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	// One read serves two purposes with different failure modes. An imported
	// resource knows only the case number, so the id must be resolved here --
	// and without it nothing else can be read, so that failure is an ERROR. The
	// access check is only a warning, so its failure is only a warning.
	access, accessErr := internal.GetCaseAccess(ctx, state.CaseNumber.ValueString())
	if state.CaseID.IsNull() || state.CaseID.ValueInt64() == 0 {
		if accessErr != nil {
			resp.Diagnostics.AddError(
				"Could not find the case for this assignment",
				fmt.Sprintf("Case %s.\n\nError: %s", state.CaseNumber.ValueString(), accessErr.Error()),
			)
			return
		}
		state.CaseID = types.Int64Value(access.CaseID)
	}

	ids, err := internal.AssignedTaskIDs(
		ctx, state.CaseID.ValueInt64(), state.WorkerNumber.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError(
			"Could not read the assignment",
			fmt.Sprintf("Employee %d on case %s.\n\nError: %s",
				state.WorkerNumber.ValueInt64(), state.CaseNumber.ValueString(), err.Error()),
		)
		return
	}

	// An assignment covering nothing is not a resource. Kala keeps the link
	// but there is nothing left for this resource to describe, so it is drift.
	if len(ids) == 0 {
		tflog.Debug(ctx, "assignment covers no tasks; removing from state", map[string]any{
			"worker": state.WorkerNumber.ValueInt64(),
		})
		resp.State.RemoveResource(ctx)
		return
	}

	state.TaskIDs = taskIDSet(ids)
	warnAboutCaseAccess(access, accessErr, state.WorkerNumber.ValueInt64(), state.CaseNumber.ValueString(), &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// requireCaseAccess enforces the rule on a WRITE: on a restricted case the
// employee must be on the access list, or they could not see the tasks this
// would assign them to. It reads Kala's list, not Terraform state, so access
// granted in the Kala UI -- or by a kala_case_access earlier in the same apply
// -- counts.
func requireCaseAccess(a client.CaseAccess, worker int64, caseNumber string, diags *diag.Diagnostics) bool {
	if !a.Restricted || slices.Contains(a.Granted, worker) {
		return true
	}
	diags.AddAttributeError(path.Root("worker_number"),
		fmt.Sprintf("Employee %d has no access to restricted case %s", worker, caseNumber),
		fmt.Sprintf("Case %s is restricted and employee %d is not on its access list, so they could not "+
			"see the tasks this would assign them to. Nothing was assigned.\n\n"+
			"Grant access first — in Kala, or with a kala_case_access resource. If a kala_case_access in "+
			"this configuration grants it, reference it from this resource, for example "+
			"`depends_on = [kala_case_access.<name>]`, so Terraform grants before it assigns.",
			caseNumber, worker))
	return false
}

// warnAboutCaseAccess is the READ side of the rule: access revoked after the
// assignment was made. Only a warning -- the assignment is intact, and
// replacing it would not restore access; a kala_case_access resource does. If
// access could not be checked at all, it says so rather than failing the
// refresh.
func warnAboutCaseAccess(a client.CaseAccess, err error, worker int64, caseNumber string, diags *diag.Diagnostics) {
	if err != nil {
		diags.AddAttributeWarning(path.Root("worker_number"), "Could not check case access",
			fmt.Sprintf("The access list of case %s could not be read, so it is unknown whether employee %d "+
				"can see the tasks they are assigned to.\n\nError: %s", caseNumber, worker, err))
		return
	}
	if a.Restricted && !slices.Contains(a.Granted, worker) {
		diags.AddAttributeWarning(path.Root("worker_number"), "Assigned without access",
			fmt.Sprintf("Employee %d is assigned to tasks on restricted case %s but has no access to it, so "+
				"they cannot see those tasks. Restore the access in Kala, or manage it with a "+
				"kala_case_access resource.", worker, caseNumber))
	}
}

func (r *taskAssignmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state taskAssignmentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	access, err := internal.GetCaseAccess(ctx, plan.CaseNumber.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Could not read the case for this assignment",
			fmt.Sprintf("Case %s could not be read, so the assignment was not changed.\n\nError: %s",
				plan.CaseNumber.ValueString(), err.Error()),
		)
		return
	}
	if !requireCaseAccess(access, plan.WorkerNumber.ValueInt64(), plan.CaseNumber.ValueString(), &resp.Diagnostics) {
		return
	}

	plan.CaseID = state.CaseID
	wanted := taskIDsOf(ctx, plan.TaskIDs, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if !r.converge(ctx, internal, &plan, wanted, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete detaches the employee from each covered task.
//
// Item detachment needs no job link id, so this works for a resource brought
// in by import — which is why destroy is expressed this way rather than by
// emptying the set through the link.
func (r *taskAssignmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state taskAssignmentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	number := state.CaseNumber.ValueString()
	worker := state.WorkerNumber.ValueInt64()

	for _, id := range taskIDsOf(ctx, state.TaskIDs, &resp.Diagnostics) {
		if err := internal.RemoveJobLinkChecklistItem(ctx, number, id, worker); err != nil {
			resp.Diagnostics.AddError(
				"Could not unassign the employee",
				fmt.Sprintf("Employee %d is still assigned to task %d on case %s. Destroy "+
					"failed rather than reporting an unassignment that did not happen.\n\n"+
					"Error: %s", worker, id, number, err.Error()),
			)
			return
		}
	}

	resp.Diagnostics.AddWarning(
		"Employee unassigned; the job link itself remains",
		fmt.Sprintf(
			"Employee %d has been detached from every task they covered on case %s.\n\n"+
				"Kala exposes no way to remove the underlying job link, so the link remains "+
				"with nothing assigned to it. It has no visible effect, and re-adding this "+
				"resource reuses it rather than creating another.",
			worker, number),
	)
}

// ImportState accepts "<case number>:<employee number>", e.g. "KA-4:1".
//
// The pair IS the identity — a job link is one worker on one case — and
// neither half is derivable from the other.
func (r *taskAssignmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	number, rawWorker, found := strings.Cut(strings.TrimSpace(req.ID), ":")
	worker, err := strconv.ParseInt(strings.TrimSpace(rawWorker), 10, 64)

	if !found || strings.TrimSpace(number) == "" || err != nil || worker <= 0 {
		resp.Diagnostics.AddError(
			"Invalid import ID for kala_task_assignment",
			fmt.Sprintf(
				"Import expects the case number and the employee number, separated by a "+
					"colon:\n\n    terraform import kala_task_assignment.gimli KA-4:1\n\n"+
					"Got %q. The pair is the identity: a job link is one employee on one case.",
				req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("case_number"), number)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("worker_number"), worker)...)
}
