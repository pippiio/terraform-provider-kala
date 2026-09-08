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
//   4. Destroy writes NOTHING and warns (ADR-001). A checklist item has no
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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/techchapter/terraform-provider-kala/internal/client"
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

var (
	_ = errors.Is
	_ = fmt.Sprintf
	_ = strings.TrimSpace
	_ = time.Second
	_ = tflog.Debug
	_ client.InternalClient
	_ = path.Root
)

// --- stubs (RED) -----------------------------------------------------------

func (r *taskResource) Create(_ context.Context, _ resource.CreateRequest, _ *resource.CreateResponse) {
}
func (r *taskResource) Read(_ context.Context, _ resource.ReadRequest, _ *resource.ReadResponse) {}
func (r *taskResource) Update(_ context.Context, _ resource.UpdateRequest, _ *resource.UpdateResponse) {
}
func (r *taskResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}
func (r *taskResource) ImportState(_ context.Context, _ resource.ImportStateRequest, _ *resource.ImportStateResponse) {
}
