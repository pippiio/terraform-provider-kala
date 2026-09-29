// Story: kala_case_access resource
//
// Input:  a case number and an employee number.
// Process:
//   1. Create: read the case's access (GetCaseAccess). If the employee is
//      already granted, ADOPT without writing -- GrantAccess sends role:[] and
//      calling it on an existing grant risks clearing roles given in the UI.
//      Otherwise grant, verified by read-back in the client.
//   2. Read: an employee no longer on Kala's access list is DRIFT. Remove the
//      resource from state, so the next plan proposes creating it again -- which
//      restores the access. A case that no longer exists is drift too.
//   3. Delete: REVOKE, verified by read-back. Nothing remains upstream, so --
//      unlike the records Kala cannot delete -- there is nothing to warn about.
//   4. Every attribute that identifies the grant forces replacement; a grant has
//      nothing the provider can change in place.
// Output: state keyed "<case_number>/<employee_number>".
//
// Dependencies: client.InternalClient (GetCaseAccess, SetCaseAccess).
// Side effects: CHANGES WHO MAY ACCESS A REAL CASE.

package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

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

func NewCaseAccessResource() resource.Resource { return &caseAccessResource{} }

type caseAccessResource struct {
	clients *providerClients
}

type caseAccessResourceModel struct {
	ID             types.String `tfsdk:"id"`
	CaseNumber     types.String `tfsdk:"case_number"`
	EmployeeNumber types.Int64  `tfsdk:"employee_number"`
	CaseID         types.Int64  `tfsdk:"case_id"`
}

func (r *caseAccessResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_case_access"
}

func (r *caseAccessResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Grants one employee access to one Kala case — an entry on the case's access " +
			"list, which governs who may access a **restricted** case and register time on it.\n\n" +
			"Destroying this resource **revokes** the access. Access revoked outside Terraform — in the " +
			"Kala UI, say — is drift: the next plan proposes creating this resource again, which " +
			"restores it.\n\n" +
			"An employee who already has access is adopted without a write. Per-case roles are not " +
			"managed: a grant this resource creates has none, and adopting one leaves its roles alone.\n\n" +
			"Access is not assignment. To assign the employee to tasks as well, use " +
			"`kala_task_assignment` — which refuses a restricted case the employee cannot access, so " +
			"reference this resource from it to order the two.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`<case_number>/<employee_number>`, also the import ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"case_number": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The case, as `kala_case.number` — the **string** number, e.g. `KA-2`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"employee_number": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "The employee to grant access, as `kala_employee.employee_number`.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"case_id": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Kala's integer id for the case.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *caseAccessResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*providerClients)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			"The kala_case_access resource expected configured Kala clients. This is a bug in the provider.")
		return
	}
	r.clients = c
}

func accessID(caseNumber string, employee int64) string {
	return caseNumber + "/" + strconv.FormatInt(employee, 10)
}

func (r *caseAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan caseAccessResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}
	number, employee := plan.CaseNumber.ValueString(), plan.EmployeeNumber.ValueInt64()

	access, err := internal.GetCaseAccess(ctx, number)
	if err != nil {
		caseReadError(&resp.Diagnostics, number, err, "nothing was granted")
		return
	}

	if slices.Contains(access.Granted, employee) {
		// Adopt. GrantAccess sends role:[], so writing here could clear roles
		// given in the Kala UI.
		tflog.Debug(ctx, "adopting an existing case access grant", map[string]any{
			"case_number": number, "employee": employee,
		})
	} else if err := internal.SetCaseAccess(ctx, number, employee, true); err != nil {
		resp.Diagnostics.AddError("Could not grant case access",
			fmt.Sprintf("Granting employee %d access to case %s failed.\n\nError: %s", employee, number, err))
		return
	}

	if !access.Restricted {
		resp.Diagnostics.AddAttributeWarning(path.Root("case_number"), "The case is not restricted",
			fmt.Sprintf("Employee %d is on the access list of case %s, but the case is not restricted: every "+
				"employee may access it, so this grant has no effect until the case is restricted.", employee, number))
	}

	plan.ID = types.StringValue(accessID(number, employee))
	plan.CaseID = types.Int64Value(access.CaseID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *caseAccessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state caseAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}
	number, employee := state.CaseNumber.ValueString(), state.EmployeeNumber.ValueInt64()

	access, err := internal.GetCaseAccess(ctx, number)
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx) // TF1.2: absence is drift
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read case access",
			fmt.Sprintf("Reading the access list of case %s failed.\n\nError: %s", number, err))
		return
	}

	// Decision D3: revoked outside Terraform is drift. Removing the resource
	// makes the next plan propose creating it again, which restores the access.
	if !slices.Contains(access.Granted, employee) {
		tflog.Debug(ctx, "case access revoked outside Terraform; removing from state", map[string]any{
			"case_number": number, "employee": employee,
		})
		resp.State.RemoveResource(ctx)
		return
	}

	state.ID = types.StringValue(accessID(number, employee))
	state.CaseID = types.Int64Value(access.CaseID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update has nothing to do: every attribute that identifies a grant forces
// replacement, and the computed ones are carried from state by their plan
// modifiers.
func (r *caseAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan caseAccessResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *caseAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state caseAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}
	number, employee := state.CaseNumber.ValueString(), state.EmployeeNumber.ValueInt64()

	access, err := internal.GetCaseAccess(ctx, number)
	if errors.Is(err, client.ErrNotFound) {
		return // no case, no grant to revoke
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read case access before revoking",
			fmt.Sprintf("Reading the access list of case %s failed, so employee %d's access was not "+
				"revoked.\n\nError: %s", number, employee, err))
		return
	}
	if !slices.Contains(access.Granted, employee) {
		return // already revoked
	}
	// A revoke that does not land must FAIL the destroy: otherwise the access
	// would silently remain while Terraform forgot it.
	if err := internal.SetCaseAccess(ctx, number, employee, false); err != nil {
		resp.Diagnostics.AddError("Could not revoke case access",
			fmt.Sprintf("Revoking employee %d's access to case %s failed; the access remains.\n\nError: %s",
				employee, number, err))
	}
}

func (r *caseAccessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	i := strings.LastIndex(req.ID, "/")
	number, rest := "", ""
	if i > 0 {
		number, rest = req.ID[:i], req.ID[i+1:]
	}
	employee, err := strconv.ParseInt(rest, 10, 64)
	if number == "" || err != nil || employee <= 0 {
		resp.Diagnostics.AddError("Invalid import ID for kala_case_access",
			fmt.Sprintf("Import expects <case_number>/<employee_number>, for example KA-2/23; got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), accessID(number, employee))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("case_number"), number)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("employee_number"), employee)...)
}

// caseReadError reports a failed case read: not-found on case_number, anything
// else as a plain error. what says what did NOT happen as a result.
func caseReadError(diags *diag.Diagnostics, number string, err error, what string) {
	if errors.Is(err, client.ErrNotFound) {
		diags.AddAttributeError(path.Root("case_number"), "Case not found",
			fmt.Sprintf("No case numbered %q exists in this Kala account, so %s.\n\n%s", number, what, err))
		return
	}
	diags.AddError("Could not read the case",
		fmt.Sprintf("Case %s could not be read, so %s.\n\nError: %s", number, what, err))
}
