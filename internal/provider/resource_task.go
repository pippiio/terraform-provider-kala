// Story: kala_task resource
//
// Input:  a plan describing a checklist item, or state identifying one.
// Process:
//   1. Resolve the case ONCE at create. Writes address a case by its string
//      number and reads by its integer id; storing both avoids a GetCase on
//      every later operation, which matters for a for_each over many tasks.
//   2. Create via CreateTask, which allocates and returns the item id, and
//      follows up with an update when a description is set (the create
//      endpoint does not accept one).
//   3. Update via UpdateTask, a FULL-RECORD REPLACE. The model carries every
//      writable field precisely so the write cannot blank what it omits.
//   4. Destroy writes NOTHING and warns. A checklist item has no
//      archive and no deactivation; unlike a case, there is no off switch.
//
// What this resource deliberately does NOT manage:
//   - is_finished. Completing work is a transactional event produced by a
//     person, and a product Non-Goal. Terraform managing it would fight the
//     worker who ticked the box.
//   - assignment. It is a many-to-many through a per-(worker, case) job link
//     and belongs in its own resource.
//
// Output: Terraform state carrying Kala's allocated identity.
//
// Dependencies: client.InternalClient (CreateTask, UpdateTask, GetCase, ListTasks).
// Side effects: CREATES AND MUTATES REAL CHECKLIST ITEMS. Kala has no delete.

package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/pippiio/terraform-provider-kala/internal/client"
)

func NewTaskResource() resource.Resource { return &taskResource{} }

type taskResource struct {
	clients *providerClients
}

type taskResourceModel struct {
	ID         types.Int64  `tfsdk:"id"`
	CaseNumber types.String `tfsdk:"case_number"`
	CaseID     types.Int64  `tfsdk:"case_id"`

	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Deadline    types.String `tfsdk:"deadline"`

	NoteRequired  types.Bool `tfsdk:"note_required"`
	ImageRequired types.Bool `tfsdk:"image_required"`

	InvoiceMode types.String `tfsdk:"invoice_mode"`
	PriceFixed  types.Int64  `tfsdk:"price_fixed"`

	IsFinished       types.Bool  `tfsdk:"is_finished"`
	AssigneeWorkerNr types.Int64 `tfsdk:"assignee_worker_number"`
}

func (r *taskResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_task"
}

func (r *taskResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a task — a checklist item on a Kala case.\n\n" +
			"**Creating a task is irreversible.** Kala has no delete endpoint for checklist " +
			"items and no archive, so `terraform destroy` removes this resource from state and " +
			"leaves the item in Kala permanently. Unlike `kala_case`, which archives, there is " +
			"no off switch at all.\n\n" +
			"This resource manages a task's **definition** — what the work is. It does not " +
			"manage whether the work is done: `is_finished` is read-only, because completing a " +
			"task is something a person does, and Terraform reverting it on the next apply " +
			"would be fighting them.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
				MarkdownDescription: "Kala's checklist item id, allocated on create.",
			},
			"case_number": schema.StringAttribute{
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "The case this task belongs to, e.g. `KA-1`. A task cannot " +
					"exist without a case and cannot be moved between them, so changing this " +
					"forces replacement — which creates a new item and strands the old one.",
			},
			"case_id": schema.Int64Attribute{
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
				MarkdownDescription: "The case's integer id, resolved once at create. Kala " +
					"addresses a case by string number on writes and by integer id on reads; " +
					"storing both avoids a lookup on every operation.",
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Task name.",
			},
			"description": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Longer description. Kala's create endpoint does not accept " +
					"one, so setting this makes creation a two-step operation.",
			},
			"deadline": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Deadline as RFC 3339, **at second precision**. Kala does " +
					"not round-trip sub-second values — the create response echoes what it was " +
					"given but the list read returns it a few milliseconds later — so anything " +
					"finer would produce a permanent diff. Sub-second input is truncated.",
			},
			"note_required": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Whether completing the task requires a note.",
			},
			"image_required": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Whether completing the task requires a photo.",
			},
			"invoice_mode": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "How the task is invoiced, e.g. `REG_HOURS&STANDARD`. " +
					"Treated as an opaque tenant value rather than a validated enum.",
			},
			"price_fixed": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Fixed price, when one applies. Null means no fixed price.",
			},
			"is_finished": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "Whether the task is complete. **Read-only.** Completion is " +
					"work performed, not configuration — a task ticked off in Kala produces no " +
					"diff here.",
			},
			"assignee_worker_number": schema.Int64Attribute{
				Computed: true,
				MarkdownDescription: "The responsible worker's employee number. **Read-only.** " +
					"Assignment is a many-to-many through a per-(worker, case) job link, which " +
					"does not fit a single field on a task.",
			},
		},
	}
}

func (r *taskResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	if c, ok := req.ProviderData.(*providerClients); ok {
		r.clients = c
	}
}

// taskDeadlineLayout is RFC 3339 at SECOND precision -- see the schema note.
const taskDeadlineLayout = "2006-01-02T15:04:05Z07:00"

// parseDeadline accepts RFC 3339 and truncates to the second.
//
// Kala does not round-trip finer than that: the create response echoes the
// millisecond value it was given, but the list read returns it a few
// milliseconds later. Truncating on the way in makes the written value and the
// read value the same, so the second plan is empty.
func parseDeadline(v types.String) (*time.Time, error) {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v.ValueString())
	if err != nil {
		return nil, err
	}
	t = t.UTC().Truncate(time.Second)
	return &t, nil
}

func (m taskResourceModel) toInput() (client.TaskInput, error) {
	deadline, err := parseDeadline(m.Deadline)
	if err != nil {
		return client.TaskInput{}, err
	}
	in := client.TaskInput{
		CaseNumber:    m.CaseNumber.ValueString(),
		CaseID:        m.CaseID.ValueInt64(),
		Name:          m.Name.ValueString(),
		Description:   m.Description.ValueString(),
		Deadline:      deadline,
		NoteRequired:  m.NoteRequired.ValueBool(),
		ImageRequired: m.ImageRequired.ValueBool(),
		InvoiceMode:   m.InvoiceMode.ValueString(),
	}
	if !m.PriceFixed.IsNull() && !m.PriceFixed.IsUnknown() {
		p := int(m.PriceFixed.ValueInt64())
		in.PriceFixed = &p
	}
	return in, nil
}

func applyTask(m *taskResourceModel, k client.Task) {
	m.ID = types.Int64Value(k.ID)
	m.CaseID = types.Int64Value(k.CaseID)
	m.CaseNumber = types.StringValue(k.CaseNumber)
	m.Name = types.StringValue(k.Name)
	m.Description = types.StringValue(k.Description)
	m.NoteRequired = types.BoolValue(k.NoteRequired)
	m.ImageRequired = types.BoolValue(k.ImageRequired)
	m.InvoiceMode = types.StringValue(k.InvoiceMode)
	m.IsFinished = types.BoolValue(k.IsFinished)

	if k.Deadline == nil {
		m.Deadline = types.StringNull()
	} else {
		m.Deadline = types.StringValue(k.Deadline.UTC().Truncate(time.Second).Format(taskDeadlineLayout))
	}
	if k.PriceFixed == nil {
		m.PriceFixed = types.Int64Null()
	} else {
		m.PriceFixed = types.Int64Value(int64(*k.PriceFixed))
	}
	if k.AssigneeWorkerNr == nil {
		m.AssigneeWorkerNr = types.Int64Null()
	} else {
		m.AssigneeWorkerNr = types.Int64Value(*k.AssigneeWorkerNr)
	}
}

// resolveCase turns the case NUMBER the operator wrote into the integer id the
// read path needs. Done once, at create; the result lives in state.
func (r *taskResource) resolveCase(
	ctx context.Context, c client.InternalClient, number string, diags *diag.Diagnostics,
) (int64, bool) {
	detail, err := c.GetCase(ctx, number)
	if err != nil {
		diags.AddError(
			"Could not find the case this task belongs to",
			fmt.Sprintf("Case %s could not be read, so the task was NOT created.\n\nError: %s",
				number, err.Error()),
		)
		return 0, false
	}
	return detail.ID, true
}

func (r *taskResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan taskResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	caseID, ok := r.resolveCase(ctx, internal, plan.CaseNumber.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}
	plan.CaseID = types.Int64Value(caseID)

	in, err := plan.toInput()
	if err != nil {
		// Rejected before any write: an unparseable deadline is a
		// configuration mistake, and creating the item first would leave an
		// undeletable record behind a failed apply.
		resp.Diagnostics.AddAttributeError(
			path.Root("deadline"),
			"Invalid deadline",
			fmt.Sprintf("Expected an RFC 3339 timestamp such as 2026-09-30T15:11:32Z.\n\n"+
				"Error: %s", err.Error()),
		)
		return
	}

	created, err := internal.CreateTask(ctx, in)
	if err != nil {
		if created.ID != 0 {
			r.recordPartialTask(ctx, plan, created.ID, resp)
		}
		resp.Diagnostics.AddError(
			"Could not create the Kala task",
			fmt.Sprintf("Task %q on case %s.\n\nError: %s",
				plan.Name.ValueString(), plan.CaseNumber.ValueString(), err.Error()),
		)
		return
	}

	applyTask(&plan, created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	tflog.Debug(ctx, "created task", map[string]any{"id": created.ID, "case": created.CaseNumber})
}

func (r *taskResource) recordPartialTask(
	ctx context.Context, plan taskResourceModel, id int64, resp *resource.CreateResponse,
) {
	plan.ID = types.Int64Value(id)
	if plan.Description.IsUnknown() {
		plan.Description = types.StringNull()
	}
	if plan.InvoiceMode.IsUnknown() {
		plan.InvoiceMode = types.StringNull()
	}
	if plan.Deadline.IsUnknown() {
		plan.Deadline = types.StringNull()
	}
	if plan.IsFinished.IsUnknown() {
		plan.IsFinished = types.BoolValue(false)
	}
	if plan.AssigneeWorkerNr.IsUnknown() {
		plan.AssigneeWorkerNr = types.Int64Null()
	}

	resp.Diagnostics.AddWarning(
		"Task was created, but the apply did not finish",
		fmt.Sprintf(
			"Checklist item %d was created on case %s and a later step failed (see the error "+
				"below). Kala has no delete endpoint for checklist items and no archive, so it "+
				"exists permanently.\n\n"+
				"Terraform has recorded it in state rather than abandoning it. Apply again to "+
				"finish converging it; without this the next apply would create a SECOND item.",
			id, plan.CaseNumber.ValueString()),
	)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *taskResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state taskResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	scan, err := internal.ListTasks(ctx, client.TaskQuery{CaseID: state.CaseID.ValueInt64()})
	if err != nil {
		resp.Diagnostics.AddError(
			"Could not read the Kala task",
			fmt.Sprintf("Case %s.\n\nError: %s", state.CaseNumber.ValueString(), err.Error()),
		)
		return
	}

	want := state.ID.ValueInt64()
	for _, k := range scan.Tasks {
		if k.ID == want {
			applyTask(&state, k)
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}

	// Absent from an INCOMPLETE read proves nothing -- reporting it as drift
	// would drop a live item from state and duplicate it on the next apply.
	if !scan.Complete() {
		resp.Diagnostics.AddError(
			"Could not confirm whether the task still exists",
			fmt.Sprintf("Task %d was not in a read covering %d of %d items on case %s, so its "+
				"absence is unproven. Terraform has left state unchanged.",
				want, scan.Fetched, scan.Total, state.CaseNumber.ValueString()),
		)
		return
	}

	tflog.Debug(ctx, "task is gone upstream; removing from state", map[string]any{"id": want})
	resp.State.RemoveResource(ctx)
}

// Update replaces the whole record. The model carries every writable field
// precisely so the write cannot blank what it omits.
func (r *taskResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state taskResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	plan.CaseID = state.CaseID
	in, err := plan.toInput()
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			path.Root("deadline"), "Invalid deadline",
			fmt.Sprintf("Expected an RFC 3339 timestamp.\n\nError: %s", err.Error()),
		)
		return
	}

	updated, err := internal.UpdateTask(ctx, state.ID.ValueInt64(), in)
	if err != nil {
		resp.Diagnostics.AddError(
			"Could not update the Kala task",
			fmt.Sprintf("Task %d on case %s.\n\nError: %s",
				state.ID.ValueInt64(), state.CaseNumber.ValueString(), err.Error()),
		)
		return
	}

	applyTask(&plan, updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete performs NO upstream write.
//
// A checklist item has neither a delete nor an archive nor a deactivation
// flag -- it is the only entity here with no off switch whatsoever.
func (r *taskResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state taskResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddWarning(
		"Task removed from state, not deleted",
		fmt.Sprintf(
			"Checklist item %d (%q) on case %s has been removed from Terraform state and "+
				"REMAINS in Kala.\n\n"+
				"Kala provides no way to delete a checklist item, no archive, and no "+
				"deactivation flag, so nothing was written upstream. Re-adding this resource "+
				"would create a SECOND item rather than adopting this one — use "+
				"`terraform import %s:%d` instead.",
			state.ID.ValueInt64(), state.Name.ValueString(), state.CaseNumber.ValueString(),
			state.CaseNumber.ValueString(), state.ID.ValueInt64()),
	)
	tflog.Debug(ctx, "task removed from state; no upstream write",
		map[string]any{"id": state.ID.ValueInt64()})
}

// ImportState accepts "<case number>:<item id>", e.g. "KA-4:9".
//
// Both halves are required and neither is derivable from the other: the id
// addresses the item, and the case number is what its writes key on.
func (r *taskResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	number, rawID, found := strings.Cut(strings.TrimSpace(req.ID), ":")
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)

	if !found || strings.TrimSpace(number) == "" || err != nil || id <= 0 {
		resp.Diagnostics.AddError(
			"Invalid import ID for kala_task",
			fmt.Sprintf(
				"Import expects the case number and the checklist item id, separated by a "+
					"colon:\n\n    terraform import kala_task.gutter KA-4:9\n\n"+
					"Got %q. Both halves are required — the id identifies the item, and the "+
					"case number is what Kala keys its writes on.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("case_number"), number)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.AddWarning(
		"Imported task is now managed by Terraform",
		"Run `terraform plan` and reconcile the configuration with what Kala holds before "+
			"applying. Updates replace the whole record, so an attribute the configuration "+
			"omits will be blanked upstream on the next apply.",
	)
}
