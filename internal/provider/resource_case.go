// Story: kala_case resource
//
// Input:  a plan describing a case, or state identifying an existing one.
// Process:
//   1. Create via CreateCase, which allocates and returns caseId and
//      caseNumber. Record state as soon as they are known -- Kala has no
//      delete (FR5).
//   2. Update FIELD BY FIELD. Cases follow the employee model, not the
//      customer one: each attribute has its own endpoint, each carries the
//      value it expects to replace, and Kala validates it. Omission is
//      therefore safe, and only changed attributes are written.
//   3. Destroy ARCHIVES (ADR-003, superseding ADR-001 for cases only) and
//      warns that the case remains. Archival is reversible and verifiable by
//      set membership, which is what let it meet ADR-002's standard.
//   4. customer_number is required exactly when internal_project is false.
//      The two create bodies are different shapes upstream, so the constraint
//      is enforced at PLAN time rather than discovered at apply.
//
// Output: Terraform state carrying Kala's allocated identity.
//
// Dependencies: client.InternalClient (CreateCase, GetCase, SetCaseField,
//               SetCaseCustomer, SetCaseArchived).
// Side effects: CREATES AND MUTATES REAL CASES. Kala has no delete.

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

func NewCaseResource() resource.Resource { return &caseResource{} }

type caseResource struct {
	clients *providerClients
}

type caseResourceModel struct {
	ID     types.Int64  `tfsdk:"id"`
	Number types.String `tfsdk:"number"`

	Name            types.String `tfsdk:"name"`
	InternalProject types.Bool   `tfsdk:"internal_project"`
	CustomerNumber  types.String `tfsdk:"customer_number"`
	WorkerNumber    types.Int64  `tfsdk:"worker_number"`

	Address      types.String `tfsdk:"address"`
	Zip          types.String `tfsdk:"zip"`
	ContactPhone types.String `tfsdk:"contact_phone"`

	Archived types.Bool `tfsdk:"archived"`

	CustomerID      types.Int64  `tfsdk:"customer_id"`
	CustomerCompany types.String `tfsdk:"customer_company"`
	IsFinished      types.Bool   `tfsdk:"is_finished"`
}

func (r *caseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_case"
}

func (r *caseResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	optional := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: desc}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a case (project) in Kala.\n\n" +
			"**Creating a case is irreversible.** Kala has no delete endpoint. " +
			"`terraform destroy` ARCHIVES the case and warns that it remains — archival is " +
			"reversible and verifiable, which is why destroy does something here rather than " +
			"only removing state as it does for `kala_customer`.\n\n" +
			"`id` and `number` are allocated by Kala, so creating this resource twice creates " +
			"two cases. Adopt an existing case with `terraform import`, never by re-declaring it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
				MarkdownDescription: "Kala's internal case id. This is the value `kala_task` " +
					"references, and it is NOT the case number.",
			},
			"number": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "The case number, e.g. `KA-1`. A **string**, allocated by " +
					"Kala. Writes address a case by this; reads address it by the integer `id`.",
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Case name.",
			},
			"internal_project": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(false),
				MarkdownDescription: "Whether this is an internal project rather than customer " +
					"work. A case is one or the other. Convertible in place — Kala accepts a " +
					"change in either direction, so this does not force replacement.",
			},
			"customer_number": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "The customer this case belongs to, e.g. `KA-1`. " +
					"**Required when `internal_project` is false, and must be omitted when it " +
					"is true** — an internal project has no customer, and the two are different " +
					"requests upstream rather than one with a blank field.",
			},
			"worker_number": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "The employee creating the case. **Required when creating**, " +
					"and used only then — Kala does not return it on a read and exposes no way " +
					"to change it afterwards.\n\n" +
					"It is therefore Optional rather than Required, and does NOT force " +
					"replacement: an imported case has no value for it, and forcing replacement " +
					"there would archive the real case and create a duplicate. Setting it on an " +
					"imported case simply records what you state.",
			},
			"address":       optional("Site address for this case."),
			"zip":           optional("Site postal code for this case."),
			"contact_phone": optional("Phone number for THIS CASE's contact — the person to call about this job. This is **not** the customer's phone number; changing it does not touch `kala_customer`."),
			"archived": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(false),
				MarkdownDescription: "Whether the case is archived. Archived and active cases " +
					"are disjoint sets upstream. `terraform destroy` sets this to true.",
			},
			"customer_id": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "The customer's internal id. Null on internal projects. Read-only.",
			},
			"customer_company": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Customer company name, as Kala reports it. Read-only.",
			},
			"is_finished": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "Whether the case is finished. Read-only: completion is " +
					"work performed, not configuration.",
			},
		},
	}
}

// ConfigValidators enforces the customer/internal exclusivity at PLAN time.
//
// Upstream these are two differently shaped requests, not one request with a
// blank field, so a configuration that gets it wrong cannot be sent at all —
// and finding out at apply, after other resources have been created, is
// strictly worse than finding out at plan.
func (r *caseResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{caseCustomerValidator{}}
}

type caseCustomerValidator struct{}

func (caseCustomerValidator) Description(_ context.Context) string {
	return "customer_number is required unless internal_project is true"
}

func (v caseCustomerValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (caseCustomerValidator) ValidateResource(
	ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse,
) {
	var cfg caseResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	internal := cfg.InternalProject.ValueBool()
	hasCustomer := !cfg.CustomerNumber.IsNull() && cfg.CustomerNumber.ValueString() != ""

	// Unknown at plan time (e.g. a customer being created in the same apply)
	// is not a violation — it is a value that is simply not resolved yet.
	if cfg.InternalProject.IsUnknown() || cfg.CustomerNumber.IsUnknown() {
		return
	}

	switch {
	case internal && hasCustomer:
		resp.Diagnostics.AddAttributeError(
			path.Root("customer_number"),
			"An internal project cannot have a customer",
			"internal_project is true, so customer_number must be omitted. Kala sends a "+
				"different request for an internal project — one with no customer block at "+
				"all — so this is not a case of leaving the field blank.",
		)
	case !internal && !hasCustomer:
		resp.Diagnostics.AddAttributeError(
			path.Root("customer_number"),
			"A customer-facing case requires a customer",
			"customer_number is required unless internal_project is set to true. A case is "+
				"either customer work or an internal project; there is no third option.",
		)
	}
}

func (r *caseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	if c, ok := req.ProviderData.(*providerClients); ok {
		r.clients = c
	}
}

// applyCase writes an upstream record over the model.
func applyCase(m *caseResourceModel, d client.CaseDetail) {
	m.ID = types.Int64Value(d.ID)
	m.Number = types.StringValue(d.Number)
	m.Name = types.StringValue(d.Name)
	m.Address = types.StringValue(d.Address)
	m.Zip = types.StringValue(d.Zip)
	m.ContactPhone = types.StringValue(d.CustomerPhone)
	m.InternalProject = types.BoolValue(d.InternalProject)
	m.Archived = types.BoolValue(d.Archived)
	m.CustomerCompany = types.StringValue(d.CustomerCompany)
	m.IsFinished = types.BoolValue(d.IsFinished)
	if d.CustomerID == 0 {
		m.CustomerID = types.Int64Null()
	} else {
		m.CustomerID = types.Int64Value(d.CustomerID)
	}
}

func (r *caseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan caseResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	// Optional in the schema so that import does not force replacement, but
	// Kala requires it to create. Checked here rather than made Required,
	// which would make an imported case unmanageable.
	if plan.WorkerNumber.IsNull() || plan.WorkerNumber.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("worker_number"),
			"worker_number is required to create a case",
			"Kala records which employee created a case and will not accept one without it.\n\n"+
				"It is Optional in the schema only so that an imported case -- for which Kala "+
				"reports no creator -- does not appear to need replacing.",
		)
		return
	}

	created, err := internal.CreateCase(ctx, client.NewCase{
		Name:            plan.Name.ValueString(),
		WorkerNr:        plan.WorkerNumber.ValueInt64(),
		CustomerNumber:  plan.CustomerNumber.ValueString(),
		InternalProject: plan.InternalProject.ValueBool(),
		Address:         plan.Address.ValueString(),
		Zip:             plan.Zip.ValueString(),
	})
	if err != nil {
		// The case exists upstream and Kala has no delete, so its identity is
		// recorded before the failure is surfaced (FR5). Without this the next
		// apply creates a SECOND case rather than converging this one.
		if created.Number != "" {
			r.recordPartialCase(ctx, plan, created, resp)
		}
		resp.Diagnostics.AddError(
			"Could not create the Kala case",
			fmt.Sprintf("Case %q.\n\nError: %s", plan.Name.ValueString(), err.Error()),
		)
		return
	}

	applyCase(&plan, created)
	if plan.ContactPhone.IsUnknown() {
		plan.ContactPhone = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	tflog.Debug(ctx, "created case", map[string]any{"id": created.ID, "number": created.Number})
}

func (r *caseResource) recordPartialCase(
	ctx context.Context, plan caseResourceModel, created client.CaseDetail, resp *resource.CreateResponse,
) {
	plan.ID = types.Int64Value(created.ID)
	plan.Number = types.StringValue(created.Number)
	for _, p := range []*types.String{
		&plan.Address, &plan.Zip, &plan.ContactPhone, &plan.CustomerCompany,
	} {
		if p.IsUnknown() {
			*p = types.StringNull()
		}
	}
	if plan.CustomerID.IsUnknown() {
		plan.CustomerID = types.Int64Null()
	}
	if plan.IsFinished.IsUnknown() {
		plan.IsFinished = types.BoolValue(false)
	}

	resp.Diagnostics.AddWarning(
		"Case was created, but the apply did not finish",
		fmt.Sprintf(
			"Case %s was created in Kala and a later step failed (see the error below). Kala "+
				"has no delete endpoint, so this case exists permanently; `terraform destroy` "+
				"can archive it but not remove it.\n\n"+
				"Terraform has recorded it in state rather than abandoning it. Apply again to "+
				"finish converging it; without this the next apply would create a SECOND case.",
			created.Number),
	)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *caseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state caseResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	got, err := internal.GetCase(ctx, state.Number.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			tflog.Debug(ctx, "case is gone upstream; removing from state",
				map[string]any{"number": state.Number.ValueString()})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Could not read the Kala case",
			fmt.Sprintf("Case %s.\n\nError: %s", state.Number.ValueString(), err.Error()),
		)
		return
	}

	applyCase(&state, got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update writes FIELD BY FIELD, and only what changed.
//
// Cases are per-field upstream, so omission is safe -- the inverse of
// kala_customer. Writing an unchanged value would also be a no-op against a
// compare-and-swap endpoint, which is a race that can only lose.
func (r *caseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state caseResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	number := state.Number.ValueString()

	for _, f := range []struct {
		field      client.CaseField
		want, have types.String
		label      string
	}{
		{client.CaseFieldName, plan.Name, state.Name, "name"},
		{client.CaseFieldAddress, plan.Address, state.Address, "address"},
		{client.CaseFieldZip, plan.Zip, state.Zip, "zip"},
		{client.CaseFieldContactPhone, plan.ContactPhone, state.ContactPhone, "contact_phone"},
	} {
		if f.want.IsNull() || f.want.IsUnknown() || f.want.Equal(f.have) {
			continue
		}
		if err := internal.SetCaseField(ctx, number, f.field, f.want.ValueString()); err != nil {
			resp.Diagnostics.AddError(
				fmt.Sprintf("Could not change the case %s", f.label),
				conflictHint(fmt.Sprintf("Case %s.\n\nError: %s", number, err.Error()), err),
			)
			return
		}
	}

	if !plan.InternalProject.Equal(state.InternalProject) ||
		!plan.CustomerNumber.Equal(state.CustomerNumber) {
		if err := internal.SetCaseCustomer(
			ctx, number, state.CustomerID.ValueInt64(), plan.InternalProject.ValueBool(),
		); err != nil {
			resp.Diagnostics.AddError(
				"Could not change the case customer",
				fmt.Sprintf("Case %s.\n\nError: %s", number, err.Error()),
			)
			return
		}
	}

	if !plan.Archived.Equal(state.Archived) {
		if err := internal.SetCaseArchived(ctx, number, plan.Archived.ValueBool()); err != nil {
			resp.Diagnostics.AddError(
				"Could not change the case archived state",
				fmt.Sprintf("Case %s.\n\nError: %s", number, err.Error()),
			)
			return
		}
	}

	got, err := internal.GetCase(ctx, number)
	if err != nil {
		resp.Diagnostics.AddError(
			"The case could not be read back after updating",
			fmt.Sprintf("Case %s.\n\nError: %s", number, err.Error()),
		)
		return
	}
	applyCase(&plan, got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// conflictHint turns Kala's compare-and-swap refusal into advice.
func conflictHint(detail string, err error) string {
	if !errors.Is(err, client.ErrConflict) {
		return detail
	}
	return detail + "\n\nThis means the case was changed in Kala after Terraform read it. " +
		"Run `terraform refresh` (or plan again) and re-apply; the change was NOT made."
}

// Delete ARCHIVES the case (ADR-003, superseding ADR-001 for cases only).
//
// A failed archive is an error, not a warning: reporting a teardown that did
// not happen is the worst available outcome.
func (r *caseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state caseResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	number := state.Number.ValueString()
	if err := internal.SetCaseArchived(ctx, number, true); err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.Diagnostics.AddWarning(
				"Case no longer exists in Kala",
				fmt.Sprintf("Case %s could not be found, so there was nothing to archive.", number),
			)
			return
		}
		resp.Diagnostics.AddError(
			"Could not archive the Kala case",
			fmt.Sprintf("Case %s is still active in Kala. Destroy failed rather than reporting "+
				"a teardown that did not happen.\n\nError: %s", number, err.Error()),
		)
		return
	}

	resp.Diagnostics.AddWarning(
		"Case archived, not deleted",
		fmt.Sprintf(
			"Case %s (%q) has been ARCHIVED in Kala and removed from Terraform state.\n\n"+
				"Kala provides no way to delete a case. Its checklist items, registered hours, "+
				"and history all remain, and the case number stays in use. Archiving is "+
				"reversible in the Kala UI; re-adding this resource would create a NEW case "+
				"rather than adopting this one — use `terraform import %s` instead.",
			number, state.Name.ValueString(), number),
	)
	tflog.Debug(ctx, "archived case on destroy", map[string]any{"number": number})
}

// ImportState accepts the STRING case number, which is what every case write
// keys on. The integer id addresses a case only on reads.
func (r *caseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	number := strings.TrimSpace(req.ID)
	if number == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID for kala_case",
			"Import expects the case NUMBER, for example:\n\n"+
				"    terraform import kala_case.roof KA-4\n\n"+
				"Not the integer id: writes address a case by its string number.",
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("number"), number)...)
	resp.Diagnostics.AddWarning(
		"Imported case is now managed by Terraform",
		"Run `terraform plan` and reconcile the configuration with what Kala holds before "+
			"applying. worker_number cannot be recovered — it is not part of the case record — "+
			"so set it explicitly; changing it later forces replacement, which would create a "+
			"second case.",
	)
}
