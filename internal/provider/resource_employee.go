package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

var (
	_ resource.Resource                = &employeeResource{}
	_ resource.ResourceWithConfigure   = &employeeResource{}
	_ resource.ResourceWithImportState = &employeeResource{}
)

// NewEmployeeResource returns the kala_employee resource.
func NewEmployeeResource() resource.Resource {
	return &employeeResource{}
}

type employeeResource struct {
	clients *providerClients
}

type employeeResourceModel struct {
	EmployeeNumber   types.Int64  `tfsdk:"employee_number"`
	Name             types.String `tfsdk:"name"`
	Email            types.String `tfsdk:"email"`
	Active           types.Bool   `tfsdk:"active"`
	SendWelcomeEmail types.Bool   `tfsdk:"send_welcome_email"`

	// Computed, read from the internal API's WorkerInfo endpoint.
	Title              types.String `tfsdk:"title"`
	Phone              types.String `tfsdk:"phone"`
	PrivatePhone       types.String `tfsdk:"private_phone"`
	Department         types.String `tfsdk:"department"`
	Initials           types.String `tfsdk:"initials"`
	LicensePlate       types.String `tfsdk:"license_plate"`
	DateOfEmployment   types.String `tfsdk:"date_of_employment"`
	FlexStartDate      types.String `tfsdk:"flex_start_date"`
	NormHours          types.String `tfsdk:"norm_hours"`
	LeaderNote         types.String `tfsdk:"leader_note"`
	IsLeader           types.Bool   `tfsdk:"is_leader"`
	IsPlanner          types.Bool   `tfsdk:"is_planner"`
	IsSuperUser        types.Bool   `tfsdk:"is_super_user"`
	IsFinance          types.Bool   `tfsdk:"is_finance"`
	IsVisibleInPlanner types.Bool   `tfsdk:"is_visible_in_planner"`
	WorkerID           types.Int64  `tfsdk:"worker_id"`
	Adopted            types.Bool   `tfsdk:"adopted"`
}

func (r *employeeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_employee"
}

func (r *employeeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an employee in Kala.\n\n" +
			"Creation uses the internal app API's `SignUp` endpoint, so `username` and `password` " +
			"must be configured on the provider.\n\n" +
			"> **Destroy deactivates rather than deletes.** Kala has no delete endpoint for " +
			"employees. `terraform destroy` sets the employee inactive and warns; their record, " +
			"settings, and history remain. See ADR-002.\n\n" +
			"> **An existing `employee_number` is adopted, not rejected.** If the number is already " +
			"in use, the resource takes ownership of that employee and reactivates them if they " +
			"were inactive.",
		Attributes: map[string]schema.Attribute{
			"employee_number": schema.Int64Attribute{
				Required: true,
				MarkdownDescription: "The employee's number. Kala does not allocate this — you choose it. " +
					"The same value is used across both Kala APIs (`medarbejderNr`, `workerNr`, and " +
					"webapiv2's `employeeNumber` are one value). Changing it forces replacement.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Full name. Sent when the employee is created. Kala exposes no endpoint " +
					"to rename an existing employee, so changing this on an adopted or existing employee " +
					"produces a warning rather than a rename.",
			},
			"email": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Email address. Set at creation, refreshed from `WorkerInfo` on every " +
					"read, and updated in place via `SetEmailNew` when changed — so this is a fully managed " +
					"attribute with real drift detection.",
			},
			"send_welcome_email": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				MarkdownDescription: "Send Kala's onboarding email when this resource **creates** a new " +
					"employee. Defaults to `true`.\n\n" +
					"It is never sent when an existing `employee_number` is adopted or reactivated — those " +
					"people have been onboarded already. Because sending mail reaches a real person and " +
					"cannot be undone, set this to `false` for migrations, imports, or test runs.\n\n" +
					"This only takes effect at creation; changing it afterwards does nothing.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"active": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				MarkdownDescription: "Whether the employee is active (`isValidated` in Kala). Setting this to " +
					"`false` deactivates them; `true` reactivates. This is the only employee field Kala " +
					"allows Terraform to both read and write, so it is the only one with real drift detection.",
			},

			"title":         optionalComputedString("Job title."),
			"phone":         optionalComputedString("Work phone number."),
			"department":    optionalComputedString("Department."),
			"initials":      optionalComputedString("Initials. Kala's other integrations derive email addresses from these."),
			"license_plate": optionalComputedString("Vehicle registration recorded against the employee."),
			"leader_note":   optionalComputedString("Free-text note visible to leaders."),
			"date_of_employment": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Employment start date as `YYYY-MM-DD`.\n\n" +
					"Kala's write endpoint takes a timestamp plus a GMT offset while its read returns a " +
					"plain date, and the offset can shift the stored date across midnight. The provider " +
					"sends midnight UTC at offset 0 so the value round-trips exactly.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},

			"private_phone": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Private phone number. Read-only — Kala exposes no endpoint to set it.",
			},
			"flex_start_date": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Flex-time start date. Read-only — Kala exposes no endpoint to set it.",
			},
			"norm_hours": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Contracted normal hours, **as an opaque string**. Kala returns JSON " +
					"embedded in a string here rather than a number — observed as `{\"normHours\": 37}`. " +
					"It is passed through verbatim rather than unwrapped, because the shape is undocumented " +
					"and may vary. Parse it with `jsondecode()` if you need the value.",
			},
			"is_leader":  optionalComputedBool("Whether the employee is a leader."),
			"is_planner": optionalComputedBool("Whether the employee has planner rights."),
			"is_finance": optionalComputedBool("Whether the employee has finance rights."),

			"is_super_user": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the employee is a super user. Read-only — Kala exposes no endpoint to set it.",
			},
			"is_visible_in_planner": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the employee appears in the planner. Read-only — Kala exposes no endpoint to set it.",
			},
			"worker_id": schema.Int64Attribute{
				Computed: true,
				MarkdownDescription: "Kala's internal worker ID. Observed to equal `employee_number`, but " +
					"exposed separately in case they ever diverge.",
				// Stable for the life of the resource, so hold the known value
				// rather than showing "(known after apply)" on every update.
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"adopted": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "True when this resource took over an employee that already existed in Kala " +
					"rather than creating one. Useful for spotting configurations that assume they provisioned " +
					"a person they actually inherited.",
				// Describes how the resource began; it never changes afterwards.
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *employeeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*providerClients)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			"The kala_employee resource expected configured Kala clients. This is a bug in the provider.",
		)
		return
	}
	r.clients = c
}

// Create either provisions a new employee or adopts an existing one.
//
// Adoption is deliberate, not a fallback for an error. Employee numbers are
// chosen by the operator rather than allocated by Kala, and Kala cannot delete
// employees — so a number being "already used" is the normal state of affairs
// for anyone who has ever offboarded someone. Failing there would make the
// resource unusable for re-onboarding, which is exactly when it is most useful.
func (r *employeeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan employeeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	number := plan.EmployeeNumber.ValueInt64()
	wantActive := plan.Active.ValueBool()

	// Prior state for field convergence. Create has no real prior state, so
	// each branch seeds only the fields Kala already holds — everything left
	// null is written, and everything seeded is written only if it differs.
	prior := employeeResourceModel{}

	existing, err := internal.GetWorker(ctx, number)
	switch {
	case err == nil:
		// --- adopt ---
		plan.Adopted = types.BoolValue(true)

		if existing.Name != "" && existing.Name != plan.Name.ValueString() {
			resp.Diagnostics.AddWarning(
				"Adopted an existing employee whose name differs from the configuration",
				fmt.Sprintf(
					"Employee %d already exists in Kala as %q, but the configuration says %q.\n\n"+
						"Kala provides no endpoint to rename an employee, so the name in Kala is "+
						"unchanged and the configured value is recorded in state only. Update your "+
						"configuration to match, or rename the employee in the Kala interface.",
					number, existing.Name, plan.Name.ValueString(),
				),
			)
		}

		if !existing.IsValidated && wantActive {
			if err := internal.SetWorkerValidated(ctx, number, true); err != nil {
				resp.Diagnostics.AddError("Could not reactivate the existing Kala employee", err.Error())
				return
			}
			resp.Diagnostics.AddWarning(
				"Reactivated an existing employee",
				fmt.Sprintf(
					"Employee %d already existed in Kala and was inactive. They have been reactivated "+
						"rather than created, so their previous settings, hours, and history are intact.",
					number,
				),
			)
			tflog.Debug(ctx, "reactivated existing employee", map[string]any{"employee_number": number})
		} else if existing.IsValidated && !wantActive {
			if err := internal.SetWorkerValidated(ctx, number, false); err != nil {
				resp.Diagnostics.AddError("Could not deactivate the existing Kala employee", err.Error())
				return
			}
		}

		// Seed the prior email from upstream so convergence issues a write only
		// on a real difference. If this read fails we leave it null, which
		// converges unconditionally — the safe direction, since an unconverged
		// email would make the applied state contradict the plan.
		if info, infoErr := internal.GetWorkerInfo(ctx, number); infoErr == nil {
			prior.Email = types.StringValue(info.Email)
		}

	case errors.Is(err, client.ErrNotFound):
		// --- create ---
		plan.Adopted = types.BoolValue(false)

		// SignUp carries the email, so seeding it here stops convergence from
		// issuing a redundant second write.
		prior.Email = plan.Email

		if _, err := internal.CreateWorker(ctx, client.NewWorker{
			Number: number,
			Email:  plan.Email.ValueString(),
			Name:   plan.Name.ValueString(),
		}); err != nil {
			resp.Diagnostics.AddError("Could not create the Kala employee", err.Error())
			return
		}

		// New employees start active; only act if inactive was requested.
		if !wantActive {
			if err := internal.SetWorkerValidated(ctx, number, false); err != nil {
				resp.Diagnostics.AddError("Employee was created but could not be deactivated", err.Error())
				return
			}
		}

		// Welcome email — genuine creation only. An adopted employee has been
		// onboarded already, and mailing them again would be confusing at best.
		if plan.SendWelcomeEmail.ValueBool() {
			if err := internal.SendWelcomeEmail(ctx, plan.Email.ValueString()); err != nil {
				// The employee exists and is configured; only the email failed.
				// Failing the apply here would abandon state for a record that
				// was created successfully, so this warns instead — but says
				// plainly that it must be sent by hand.
				resp.Diagnostics.AddWarning(
					"Employee created, but the welcome email could not be sent",
					fmt.Sprintf(
						"Employee %d was created successfully and Terraform will record it, but Kala "+
							"rejected the welcome email to %q.\n\nSend it from the Kala interface, or "+
							"set send_welcome_email = false to stop Terraform attempting it.\n\nError: %s",
						number, plan.Email.ValueString(), err.Error()),
				)
			} else {
				tflog.Debug(ctx, "sent welcome email for new employee",
					map[string]any{"employee_number": number})
			}
		}

	default:
		resp.Diagnostics.AddError("Could not look up the Kala employee", err.Error())
		return
	}

	// Apply any declared field values. SignUp accepts only number, name, and
	// email, so everything else needs its own write — and on an adopted
	// employee this is what brings Kala in line with the configuration.
	// The empty prior state means "write whatever was declared".
	if !r.applyFieldChanges(ctx, internal, number, plan, prior, &resp.Diagnostics) {
		return
	}

	if !r.refresh(ctx, &plan, internal, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *employeeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state employeeResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	worker, err := internal.GetWorker(ctx, state.EmployeeNumber.ValueInt64())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			// Kala cannot delete employees, so this is unusual — but if the
			// record is genuinely gone, it is drift, not a failure (TF1.2).
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Could not read the Kala employee", err.Error())
		return
	}

	applyWorker(&state, worker)
	r.enrich(ctx, &state, internal, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *employeeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state employeeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	number := plan.EmployeeNumber.ValueInt64()

	// Activation is the only field Kala lets us change.
	if plan.Active.ValueBool() != state.Active.ValueBool() {
		if err := internal.SetWorkerValidated(ctx, number, plan.Active.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Could not change the employee's activation state", err.Error())
			return
		}
	}

	if plan.Name.ValueString() != state.Name.ValueString() {
		resp.Diagnostics.AddWarning(
			"Employee name cannot be changed through Kala's API",
			fmt.Sprintf(
				"The configuration changes employee %d's name from %q to %q, but Kala exposes no "+
					"endpoint to rename an employee. The new value is recorded in Terraform state "+
					"only — the name in Kala is unchanged. Rename them in the Kala interface to "+
					"make this real.",
				number, state.Name.ValueString(), plan.Name.ValueString(),
			),
		)
	}

	if !r.applyFieldChanges(ctx, internal, number, plan, state, &resp.Diagnostics) {
		return
	}

	// Carry adoption status forward — it describes how the resource began.
	plan.Adopted = state.Adopted

	if !r.refresh(ctx, &plan, internal, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deactivates the employee (ADR-002).
//
// This is the one resource whose Delete writes upstream. ARCH1.5 explicitly
// excludes this path from graceful degradation: a failed deactivation must fail
// the apply, because silently "succeeding" would leave a departed employee
// active while reporting otherwise.
func (r *employeeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state employeeResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	number := state.EmployeeNumber.ValueInt64()

	if err := internal.SetWorkerValidated(ctx, number, false); err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.Diagnostics.AddWarning(
				"Employee no longer exists in Kala",
				fmt.Sprintf("Employee %d could not be found, so there was nothing to deactivate.", number),
			)
			return
		}
		resp.Diagnostics.AddError(
			"Could not deactivate the Kala employee",
			fmt.Sprintf("Employee %d is still active in Kala. Destroy failed rather than reporting "+
				"a cleanup that did not happen.\n\nError: %s", number, err.Error()),
		)
		return
	}

	resp.Diagnostics.AddWarning(
		"Employee deactivated, not deleted",
		fmt.Sprintf(
			"Employee %d (%s) has been deactivated in Kala and removed from Terraform state.\n\n"+
				"Kala provides no way to delete an employee. Their record, settings, registered "+
				"hours, and history all remain, and the employee number stays in use. Re-adding "+
				"this resource with the same employee_number will reactivate them rather than "+
				"create someone new.",
			number, state.Name.ValueString(),
		),
	)

	tflog.Debug(ctx, "deactivated employee on destroy", map[string]any{"employee_number": number})
}

// ImportState accepts the employee number.
func (r *employeeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	number, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil || number == 0 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Import ID %q must be the employee number, for example \"3\".", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("employee_number"), number)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("adopted"), true)...)

	resp.Diagnostics.AddWarning(
		"email could not be imported",
		"Kala returns no email address from any read endpoint, so it cannot be recovered on import. "+
			"Set it in your configuration; it is only ever sent when an employee is created, so the "+
			"value will not be written back to Kala.",
	)
}

// --- helpers -------------------------------------------------------------

// refresh reads the worker back and fills the computed attributes.
func (r *employeeResource) refresh(ctx context.Context, m *employeeResourceModel, c client.InternalClient, diags diagnosticSink) bool {
	worker, err := c.GetWorker(ctx, m.EmployeeNumber.ValueInt64())
	if err != nil {
		diags.AddError("Could not read the Kala employee back", err.Error())
		return false
	}
	applyWorker(m, worker)
	r.enrich(ctx, m, c, diags)
	return true
}

// applyFieldChanges writes every settable field whose planned value differs
// from state, one endpoint per field.
//
// Kala has no bulk update: each field is its own endpoint, each verified by
// read-back. Only changed fields are written, so an apply that touches one
// attribute does not rewrite the rest.
//
// The null/unknown check is what protects an ADOPTED employee: an attribute the
// configuration does not mention arrives null, never as a zero value, so
// omitting `department` cannot blank a department Kala already holds. An
// explicit "" or false, by contrast, IS a request to clear or revoke — and must
// be honoured, or Terraform rejects the apply for producing a result that
// contradicts the plan.
func (r *employeeResource) applyFieldChanges(
	ctx context.Context, c client.InternalClient, number int64,
	plan, state employeeResourceModel, diags *diag.Diagnostics,
) bool {
	stringFields := []struct {
		field     client.WorkerField
		planned   types.String
		prior     types.String
		attribute string
	}{
		{client.FieldPhone, plan.Phone, state.Phone, "phone"},
		{client.FieldTitle, plan.Title, state.Title, "title"},
		{client.FieldInitials, plan.Initials, state.Initials, "initials"},
		{client.FieldLicensePlate, plan.LicensePlate, state.LicensePlate, "license_plate"},
		{client.FieldDepartment, plan.Department, state.Department, "department"},
		{client.FieldLeaderNote, plan.LeaderNote, state.LeaderNote, "leader_note"},
	}
	for _, f := range stringFields {
		// Unknown or null means the user left it unset and Terraform will fill
		// it from the refresh — not a change to write.
		if f.planned.IsUnknown() || f.planned.IsNull() || f.planned.Equal(f.prior) {
			continue
		}

		if err := c.SetWorkerField(ctx, number, f.field, f.planned.ValueString()); err != nil {
			diags.AddAttributeError(path.Root(f.attribute),
				"Could not update "+f.attribute, err.Error())
			return false
		}
	}

	roles := []struct {
		role      client.WorkerRole
		planned   types.Bool
		prior     types.Bool
		attribute string
	}{
		{client.RoleLeader, plan.IsLeader, state.IsLeader, "is_leader"},
		{client.RoleFinance, plan.IsFinance, state.IsFinance, "is_finance"},
		{client.RolePlanner, plan.IsPlanner, state.IsPlanner, "is_planner"},
	}
	for _, rl := range roles {
		if rl.planned.IsUnknown() || rl.planned.IsNull() || rl.planned.Equal(rl.prior) {
			continue
		}

		if err := c.SetWorkerRole(ctx, number, rl.role, rl.planned.ValueBool()); err != nil {
			diags.AddAttributeError(path.Root(rl.attribute),
				"Could not update "+rl.attribute, err.Error())
			return false
		}
	}

	// Email has its own endpoint, like date_of_employment below. It belongs in
	// this convergence path and not only in Update: the adoption path in Create
	// never wrote it, while refresh overwrote it from WorkerInfo, so the applied
	// state contradicted the plan whenever the two differed.
	if !plan.Email.IsUnknown() && !plan.Email.IsNull() && !plan.Email.Equal(state.Email) {
		if err := c.SetWorkerEmail(ctx, number, plan.Email.ValueString()); err != nil {
			diags.AddAttributeError(path.Root("email"), "Could not update email", err.Error())
			return false
		}
	}

	dateDeclared := !plan.DateOfEmployment.IsUnknown() && !plan.DateOfEmployment.IsNull()
	dateChanged := !plan.DateOfEmployment.Equal(state.DateOfEmployment)

	if dateDeclared && dateChanged {
		if err := c.SetWorkerDateOfEmployment(ctx, number, plan.DateOfEmployment.ValueString()); err != nil {
			diags.AddAttributeError(path.Root("date_of_employment"),
				"Could not update date_of_employment", err.Error())
			return false
		}
	}

	return true
}

// optionalComputedString builds a settable string attribute that falls back to
// whatever Kala already holds when the user does not declare it.
func optionalComputedString(description string) schema.StringAttribute {
	return schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: description + " Leave unset to adopt Kala's current value.",
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

func optionalComputedBool(description string) schema.BoolAttribute {
	return schema.BoolAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: description + " Leave unset to adopt Kala's current value.",
		PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
	}
}

// enrich fills the WorkerInfo-sourced attributes.
//
// Best-effort per ARCH1.5: WorkerInfo is a read-enrichment path, so a failure
// degrades rather than failing the operation. Prior values are LEFT IN PLACE
// rather than nulled — nulling them would manufacture a diff on every plan the
// user could not resolve.
func (r *employeeResource) enrich(ctx context.Context, m *employeeResourceModel, c client.InternalClient, diags diagnosticSink) {
	info, err := c.GetWorkerInfo(ctx, m.EmployeeNumber.ValueInt64())
	if err != nil {
		if w, ok := diags.(interface{ AddWarning(string, string) }); ok {
			w.AddWarning(
				"Could not read detailed employee information",
				fmt.Sprintf("Employee %d's detail attributes could not be refreshed from Kala and are "+
					"carried forward from the previous state.\n\nError: %s",
					m.EmployeeNumber.ValueInt64(), err.Error()),
			)
		}
		tflog.Debug(ctx, "WorkerInfo enrichment failed; keeping prior values", map[string]any{
			"employee_number": m.EmployeeNumber.ValueInt64(),
		})
		return
	}
	applyWorkerInfo(m, info)
}

// applyWorkerInfo copies the detailed record onto the model.
func applyWorkerInfo(m *employeeResourceModel, i client.WorkerInfo) {
	// email is readable after all — via WorkerInfo, not the endpoints that
	// return the settings list. Refreshing it gives real drift detection.
	m.Email = types.StringValue(i.Email)

	m.Title = types.StringValue(i.Title)
	m.Phone = types.StringValue(i.Phone)
	m.PrivatePhone = types.StringValue(i.PrivatePhone)
	m.Department = types.StringValue(i.Department)
	m.Initials = types.StringValue(i.Initials)
	m.LicensePlate = types.StringValue(i.LicensePlate)
	m.DateOfEmployment = types.StringValue(i.DateOfEmployment)
	m.FlexStartDate = types.StringValue(i.FlexStartDate)
	m.NormHours = types.StringValue(i.NormHours)
	m.LeaderNote = types.StringValue(i.LeaderNote)
	m.IsLeader = types.BoolValue(i.IsLeader)
	m.IsPlanner = types.BoolValue(i.IsPlanner)
	m.IsSuperUser = types.BoolValue(i.IsSuperUser)
	m.IsFinance = types.BoolValue(i.IsFinance)
	m.IsVisibleInPlanner = types.BoolValue(i.IsVisibleInPlanner)
	m.WorkerID = types.Int64Value(i.WorkerID)
}

// diagnosticSink is the subset of diag.Diagnostics these helpers need.
type diagnosticSink interface{ AddError(string, string) }

// applyWorker copies the fields the WORKER LIST reliably provides.
//
// Deliberately only activation state. /api/Workers returns title, phone,
// department, and initials as empty strings even when WorkerInfo has values for
// them (observed 2026-09-01), so copying them here would overwrite good
// enrichment data with blanks — and would null those attributes entirely
// whenever enrichment failed. Everything beyond activation comes from
// applyWorkerInfo.
func applyWorker(m *employeeResourceModel, w client.Worker) {
	m.Active = types.BoolValue(w.IsValidated)
	if m.Adopted.IsNull() || m.Adopted.IsUnknown() {
		m.Adopted = types.BoolValue(false)
	}
}

// resolveUnknowns replaces unknown values with null. Not yet implemented.
func resolveUnknowns(_ *employeeResourceModel) {}
