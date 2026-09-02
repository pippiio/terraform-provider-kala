// Story: Task data sources (kala_tasks, kala_task)
//
// A Kala "task" is a CHECKLIST ITEM. It carries completion, hours, and billing
// data, which draft/product.md excludes from managed state; reading it here is
// a deliberate, recorded narrowing of that Non-Goal.
//
// Input:  Terraform config -- case_id is REQUIRED on both, plus filters on the
//         list and exactly one of id/name on the singular one.
// Process:
//   1. case_id is required because it ADDRESSES the endpoint rather than
//      filtering it. There is no account-wide task read: that would be one
//      request per case, an N+1 fan-out against undocumented rate limits.
//   2. Forward the filters upstream actually honours -- search, name_contains,
//      only_unfinished -- and let the client apply the assignee filter, which
//      has no upstream parameter.
//   3. Surface read coverage as `complete`, plus the case-level counters the
//      envelope carries, which are unaffected by any filter.
//   4. Withhold assignee and authorship behind include_contact_details, and
//      hours and money behind include_financials (FR7).
//   5. For kala_task, prefilter by name upstream where possible, then match
//      exactly and require a single result -- the same shape kala_customer uses.
//
// Output: Terraform state.
//
// Dependencies: client.InternalClient (ListTasks).
// Side effects: none. AddChecklistItem exists upstream, is not called, and is
//               not on ARCH1.3's permitted-write list.

package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

var (
	_ datasource.DataSource              = &tasksDataSource{}
	_ datasource.DataSourceWithConfigure = &tasksDataSource{}
	_ datasource.DataSource              = &taskDataSource{}
	_ datasource.DataSourceWithConfigure = &taskDataSource{}
)

type taskModel struct {
	ID          types.Int64  `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	CaseID      types.Int64  `tfsdk:"case_id"`
	CaseNumber  types.String `tfsdk:"case_number"`
	ChecklistID types.Int64  `tfsdk:"checklist_id"`
	StatusName  types.String `tfsdk:"status_name"`

	Deadline     types.String `tfsdk:"deadline"`
	TimeAdded    types.String `tfsdk:"time_added"`
	IsFinished   types.Bool   `tfsdk:"is_finished"`
	TimeFinished types.String `tfsdk:"time_finished"`

	InvoiceMode   types.String `tfsdk:"invoice_mode"`
	NoteRequired  types.Bool   `tfsdk:"note_required"`
	ImageRequired types.Bool   `tfsdk:"image_required"`
	HasImage      types.Bool   `tfsdk:"has_image"`

	AssigneeWorkerNr types.Int64  `tfsdk:"assignee_worker_nr"`
	AssignedToMe     types.Bool   `tfsdk:"assigned_to_me"`
	CreatedBy        types.String `tfsdk:"created_by"`
	FinishedBy       types.String `tfsdk:"finished_by"`

	RegisteredHoursTotal types.Int64 `tfsdk:"registered_hours_total"`
	BilledHours          types.Int64 `tfsdk:"billed_hours"`
	PriceFixed           types.Int64 `tfsdk:"price_fixed"`
}

func taskAttributes() map[string]schema.Attribute {
	a := map[string]schema.Attribute{
		"id":           schema.Int64Attribute{Computed: true, MarkdownDescription: "Checklist item id."},
		"name":         schema.StringAttribute{Computed: true, MarkdownDescription: "Task name."},
		"description":  schema.StringAttribute{Computed: true, MarkdownDescription: "Task description."},
		"case_id":      schema.Int64Attribute{Computed: true, MarkdownDescription: "The case this task belongs to."},
		"case_number":  schema.StringAttribute{Computed: true, MarkdownDescription: "The case number, e.g. `KA-1`."},
		"checklist_id": schema.Int64Attribute{Computed: true, MarkdownDescription: "The checklist this item belongs to."},
		"status_name": schema.StringAttribute{
			Computed: true,
			MarkdownDescription: "Status label. **Not translated** -- these are the tenant's own values " +
				"from the company-wide `kanban_options` setting, so they appear in whatever language " +
				"that setting uses.",
		},
		"deadline":       schema.StringAttribute{Computed: true, MarkdownDescription: "Deadline as RFC 3339. Null when unset."},
		"time_added":     schema.StringAttribute{Computed: true, MarkdownDescription: "When the task was created, as RFC 3339."},
		"is_finished":    schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the task is complete."},
		"time_finished":  schema.StringAttribute{Computed: true, MarkdownDescription: "When it was completed, as RFC 3339. Null when unfinished."},
		"invoice_mode":   schema.StringAttribute{Computed: true, MarkdownDescription: "How the task is invoiced, e.g. `REG_HOURS&SPECIAL`."},
		"note_required":  schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether completing the task requires a note."},
		"image_required": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether completing the task requires a photo."},
		"has_image":      schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether a photo has been attached."},
	}
	for name, desc := range map[string]string{
		"assignee_worker_nr": "Employee number of the responsible worker",
		"assigned_to_me":     "Whether the task is assigned to the authenticating user",
		"created_by":         "Who created the task",
		"finished_by":        "Who completed the task",
	} {
		d := desc + ". **Null unless `include_contact_details` is set** -- this identifies a person."
		if name == "assignee_worker_nr" {
			a[name] = schema.Int64Attribute{Computed: true, MarkdownDescription: d + " Null also when unassigned."}
		} else if name == "assigned_to_me" {
			a[name] = schema.BoolAttribute{Computed: true, MarkdownDescription: d}
		} else {
			a[name] = schema.StringAttribute{Computed: true, MarkdownDescription: d}
		}
	}
	for name, desc := range map[string]string{
		"registered_hours_total": "Hours registered against the task",
		"billed_hours":           "Hours billed",
		"price_fixed":            "Fixed price, when one is set",
	} {
		a[name] = schema.Int64Attribute{
			Computed:            true,
			MarkdownDescription: desc + ". **Null unless `include_financials` is set.**",
		}
	}
	return a
}

// --- kala_tasks -----------------------------------------------------------

// NewTasksDataSource returns the kala_tasks data source.
func NewTasksDataSource() datasource.DataSource { return &tasksDataSource{} }

type tasksDataSource struct {
	client client.InternalClient
}

type tasksDataSourceModel struct {
	CaseID           types.Int64  `tfsdk:"case_id"`
	Search           types.String `tfsdk:"search"`
	NameContains     types.String `tfsdk:"name_contains"`
	OnlyUnfinished   types.Bool   `tfsdk:"only_unfinished"`
	AssigneeWorkerNr types.Int64  `tfsdk:"assignee_worker_nr"`

	IncludeContactDetails types.Bool `tfsdk:"include_contact_details"`
	IncludeFinancials     types.Bool `tfsdk:"include_financials"`

	Complete     types.Bool  `tfsdk:"complete"`
	Total        types.Int64 `tfsdk:"total"`
	CaseTotal    types.Int64 `tfsdk:"case_total"`
	CaseFinished types.Int64 `tfsdk:"case_finished"`
	Tasks        []taskModel `tfsdk:"tasks"`
}

func (d *tasksDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tasks"
}

func (d *tasksDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the tasks (checklist items) of one case.\n\n" +
			"`case_id` is **required**: Kala addresses checklist items by case, and there is no " +
			"account-wide task endpoint. Reading every task would mean one request per case.\n\n" +
			"To read tasks across several cases, combine with `kala_cases` and `for_each` rather " +
			"than expecting this data source to fan out.",
		Attributes: map[string]schema.Attribute{
			"case_id": schema.Int64Attribute{
				Required: true,
				MarkdownDescription: "The case whose tasks to read. This is the integer `id` from " +
					"`kala_cases`, **not** the string case number.",
			},
			"search":        schema.StringAttribute{Optional: true, MarkdownDescription: "Free-text filter applied upstream."},
			"name_contains": schema.StringAttribute{Optional: true, MarkdownDescription: "Name substring filter applied upstream."},
			"only_unfinished": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Return only unfinished tasks. Applied upstream. Defaults to `false`, which returns all.",
			},
			"assignee_worker_nr": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "Return only tasks whose responsible worker matches. **Applied " +
					"client-side** -- Kala has no assignee parameter -- so it narrows the result " +
					"without reducing what was read. `complete` still reflects the underlying read.",
			},
			"include_contact_details": schema.BoolAttribute{Optional: true, MarkdownDescription: "Expose assignee and authorship fields. Off by default."},
			"include_financials":      schema.BoolAttribute{Optional: true, MarkdownDescription: "Expose hours and price fields. Off by default."},

			"complete": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "Whether the read covered every task on the case. Reflects records " +
					"**received**, so a client-side `assignee_worker_nr` filter does not make it false.",
			},
			"total":         schema.Int64Attribute{Computed: true, MarkdownDescription: "How many tasks the case holds, as reported upstream."},
			"case_total":    schema.Int64Attribute{Computed: true, MarkdownDescription: "Case-level task count, unaffected by filters."},
			"case_finished": schema.Int64Attribute{Computed: true, MarkdownDescription: "Case-level finished-task count, unaffected by filters."},
			"tasks": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The tasks returned.",
				NestedObject:        schema.NestedAttributeObject{Attributes: taskAttributes()},
			},
		},
	}
}

func (d *tasksDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureInternal(req, resp, "kala_tasks")
}

func (d *tasksDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	_ = ctx
	_ = req
	_ = resp
}

// --- kala_task ------------------------------------------------------------

// NewTaskDataSource returns the kala_task data source.
func NewTaskDataSource() datasource.DataSource { return &taskDataSource{} }

type taskDataSource struct {
	client client.InternalClient
}

type taskDataSourceModel struct {
	CaseIDFilter          types.Int64  `tfsdk:"case_id_filter"`
	NameSelector          types.String `tfsdk:"name_selector"`
	IncludeContactDetails types.Bool   `tfsdk:"include_contact_details"`
	IncludeFinancials     types.Bool   `tfsdk:"include_financials"`
	taskModel
}

func (d *taskDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_task"
}

func (d *taskDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "placeholder"}
}

func (d *taskDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureInternal(req, resp, "kala_task")
}

func (d *taskDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	_ = ctx
	_ = req
	_ = resp
}

// buildTasksState converts domain tasks into state models.
func buildTasksState(tasks []client.Task, includeContacts, includeFinancials bool) []taskModel {
	return nil // stub: RED
}

var (
	_ = errors.Is
	_ = fmt.Sprintf
	_ = path.Root
	_ = tflog.Debug
)
