package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"

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
	EmployeeNumber types.Int64  `tfsdk:"employee_number"`
	Name           types.String `tfsdk:"name"`
	Email          types.String `tfsdk:"email"`
	Active         types.Bool   `tfsdk:"active"`

	// Computed, read from the internal API.
	Title      types.String `tfsdk:"title"`
	Phone      types.String `tfsdk:"phone"`
	Department types.String `tfsdk:"department"`
	Initials   types.String `tfsdk:"initials"`
	Adopted    types.Bool   `tfsdk:"adopted"`
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
				MarkdownDescription: "Email address, used at creation. **Write-only:** no Kala read endpoint " +
					"returns it, so Terraform cannot detect drift on this field.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"active": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				MarkdownDescription: "Whether the employee is active (`isValidated` in Kala). Setting this to " +
					"`false` deactivates them; `true` reactivates. This is the only employee field Kala " +
					"allows Terraform to both read and write, so it is the only one with real drift detection.",
			},

			"title":      schema.StringAttribute{Computed: true, MarkdownDescription: "Job title, as recorded in Kala."},
			"phone":      schema.StringAttribute{Computed: true, MarkdownDescription: "Phone number, as recorded in Kala."},
			"department": schema.StringAttribute{Computed: true, MarkdownDescription: "Department, as recorded in Kala."},
			"initials":   schema.StringAttribute{Computed: true, MarkdownDescription: "Initials, as recorded in Kala."},
			"adopted": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "True when this resource took over an employee that already existed in Kala " +
					"rather than creating one. Useful for spotting configurations that assume they provisioned " +
					"a person they actually inherited.",
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

	case errors.Is(err, client.ErrNotFound):
		// --- create ---
		plan.Adopted = types.BoolValue(false)

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

	default:
		resp.Diagnostics.AddError("Could not look up the Kala employee", err.Error())
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

	// email is never refreshed: no Kala endpoint returns it, so anything we
	// wrote here would be invented and would produce a perpetual diff.

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

	if plan.Email.ValueString() != state.Email.ValueString() {
		resp.Diagnostics.AddWarning(
			"Employee email cannot be changed through Kala's API",
			fmt.Sprintf(
				"Employee %d's email is only sent when the employee is first created, and no Kala "+
					"read endpoint returns it. The new value is recorded in state only.", number),
		)
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
func (r *employeeResource) refresh(ctx context.Context, m *employeeResourceModel, c client.InternalClient, diags interface{ AddError(string, string) }) bool {
	worker, err := c.GetWorker(ctx, m.EmployeeNumber.ValueInt64())
	if err != nil {
		diags.AddError("Could not read the Kala employee back", err.Error())
		return false
	}
	applyWorker(m, worker)
	return true
}

// applyWorker copies readable fields from Kala onto the model.
//
// name is refreshed because Kala does return it — but since it cannot be
// written, a mismatch surfaces as a diff the user resolves by editing their
// configuration, not by applying.
func applyWorker(m *employeeResourceModel, w client.Worker) {
	m.Active = types.BoolValue(w.IsValidated)
	m.Title = types.StringValue(w.Title)
	m.Phone = types.StringValue(w.Phone)
	m.Department = types.StringValue(w.Department)
	m.Initials = types.StringValue(w.Initials)
	if m.Adopted.IsNull() || m.Adopted.IsUnknown() {
		m.Adopted = types.BoolValue(false)
	}
}
