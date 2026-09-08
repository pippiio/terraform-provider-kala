// Story: kala_customer resource
//
// Input:  a plan describing a customer, or state identifying an existing one.
// Process:
//   1. Create via AddCustomer, which allocates and returns id and number.
//      RECORD STATE THE MOMENT THE ID IS KNOWN, before anything else can
//      fail -- Kala has no delete, so an id dropped on the floor strands a
//      real record permanently (FR5, AC7).
//   2. Read by id, treating a genuine absence as drift (TF1.2) and an
//      unprovable absence -- a miss inside a truncated read -- as an error.
//   3. Update via EditCustomer, which REPLACES the whole record. The model
//      carries every writable field, so the write cannot blank what it does
//      not mention.
//   4. Destroy writes NOTHING upstream and warns that the record remains
//      (ADR-001, TF1.1). Kala offers customers no off switch -- unlike
//      employees, which deactivate, and cases, which archive.
//
// Output: Terraform state carrying Kala's allocated identity.
//
// Dependencies: client.InternalClient (AddCustomer, EditCustomer, GetCustomer).
// Side effects: CREATES AND MUTATES REAL, UNDELETABLE RECORDS.

package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/techchapter/terraform-provider-kala/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func NewCustomerResource() resource.Resource {
	return &customerResource{}
}

type customerResource struct {
	clients *providerClients
}

type customerResourceModel struct {
	// Allocated by Kala, never chosen. Verified 2026-09-07 that id and number
	// hold the same values on both APIs, and that number is a string on both.
	ID     types.Int64  `tfsdk:"id"`
	Number types.String `tfsdk:"number"`

	Company     types.String `tfsdk:"company"`
	FirstName   types.String `tfsdk:"first_name"`
	LastName    types.String `tfsdk:"last_name"`
	Email       types.String `tfsdk:"email"`
	Phone       types.String `tfsdk:"phone"`
	Address     types.String `tfsdk:"address"`
	Zip         types.String `tfsdk:"zip"`
	CVR         types.String `tfsdk:"cvr"`
	EAN         types.String `tfsdk:"ean"`
	Description types.String `tfsdk:"description"`

	// Read-only: neither write body carries them.
	City      types.String `tfsdk:"city"`
	CaseCount types.Int64  `tfsdk:"case_count"`
}

func (r *customerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_customer"
}

func (r *customerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	optional := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: desc}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a customer in Kala.\n\n" +
			"**Creating a customer is irreversible.** Kala has no delete endpoint for customers " +
			"and no deactivation flag, so `terraform destroy` removes this resource from state " +
			"and leaves the customer in Kala permanently. Unlike `kala_employee`, which " +
			"deactivates, there is no off switch to fall back on.\n\n" +
			"`id` and `number` are allocated by Kala, not chosen, so creating this resource " +
			"twice creates two customers rather than adopting one. Bring an existing customer " +
			"under management with `terraform import`, never by re-declaring it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
				MarkdownDescription: "Kala's internal customer id, allocated on create.",
			},
			"number": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "The customer number, e.g. `KA-1`. Allocated by Kala, not chosen.",
			},
			"company": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Company name. **Required**: a customer with no company is " +
					"indistinguishable from an empty record, and Kala cannot delete the result.",
			},
			"first_name":  optional("Contact's first name."),
			"last_name":   optional("Contact's last name."),
			"email":       optional("Email address. Personal data -- it is written to Terraform state."),
			"phone":       optional("Phone number. Personal data -- it is written to Terraform state."),
			"address":     optional("Street address."),
			"zip":         optional("Postal code."),
			"cvr":         optional("Danish CVR company registration number."),
			"ean":         optional("EAN number used for invoicing."),
			"description": optional("Free-text note on the customer."),
			"city": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "City. Read-only: neither write endpoint carries it.",
			},
			"case_count": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "How many cases reference this customer. Read-only.",
			},
		},
	}
}

func (r *customerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	if c, ok := req.ProviderData.(*providerClients); ok {
		r.clients = c
	}
}

func (m customerResourceModel) toInput() client.CustomerInput {
	return client.CustomerInput{
		FirstName: m.FirstName.ValueString(), LastName: m.LastName.ValueString(),
		Company: m.Company.ValueString(), Phone: m.Phone.ValueString(),
		Address: m.Address.ValueString(), Zip: m.Zip.ValueString(),
		Email: m.Email.ValueString(), CVR: m.CVR.ValueString(),
		Description: m.Description.ValueString(), EAN: m.EAN.ValueString(),
	}
}

// applyCustomer writes an upstream record over the model.
//
// Every attribute is set from the response, including ones the caller left
// null: they are Optional+Computed, so Terraform requires a known value after
// apply, and the value Kala holds is the honest one.
func applyCustomer(m *customerResourceModel, c client.Customer) {
	m.ID = types.Int64Value(c.ID)
	m.Number = types.StringValue(c.Number)
	m.Company = types.StringValue(c.Company)
	m.FirstName = types.StringValue(c.FirstName)
	m.LastName = types.StringValue(c.LastName)
	m.Email = types.StringValue(c.Email)
	m.Phone = types.StringValue(c.Phone)
	m.Address = types.StringValue(c.Address)
	m.Zip = types.StringValue(c.Zip)
	m.CVR = types.StringValue(c.CVR)
	m.EAN = types.StringValue(c.EAN)
	m.Description = types.StringValue(c.Description)
	m.City = types.StringValue(c.City)
	m.CaseCount = types.Int64Value(int64(c.CaseCount))
}

func (r *customerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan customerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	created, err := internal.AddCustomer(ctx, plan.toInput())
	if err != nil {
		// The id is the whole point of this branch. Kala has no delete and no
		// deactivation for customers, so a record created before the failure
		// exists permanently -- abandoning its id would leave an orphan the
		// operator can neither find nor remove.
		if created.ID != 0 {
			r.recordPartialCreate(ctx, plan, created.ID, resp)
		}
		resp.Diagnostics.AddError(
			"Could not create the Kala customer",
			fmt.Sprintf("Company %q.\n\nError: %s", plan.Company.ValueString(), err.Error()),
		)
		return
	}

	applyCustomer(&plan, created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	tflog.Debug(ctx, "created customer", map[string]any{"id": created.ID, "number": created.Number})
}

// recordPartialCreate stores the identity of a customer that exists upstream
// after a failed apply, so the next plan converges it instead of creating a
// second one.
func (r *customerResource) recordPartialCreate(
	ctx context.Context, plan customerResourceModel, id int64, resp *resource.CreateResponse,
) {
	plan.ID = types.Int64Value(id)
	plan.Number = types.StringUnknown()

	// Nothing unknown may reach state; null records honestly that Terraform
	// does not know what Kala holds, and the next Read fills it in.
	for _, p := range []*types.String{
		&plan.Number, &plan.FirstName, &plan.LastName, &plan.Email, &plan.Phone,
		&plan.Address, &plan.Zip, &plan.CVR, &plan.EAN, &plan.Description, &plan.City,
	} {
		if p.IsUnknown() {
			*p = types.StringNull()
		}
	}
	if plan.CaseCount.IsUnknown() {
		plan.CaseCount = types.Int64Null()
	}

	resp.Diagnostics.AddWarning(
		"Customer was created, but the apply did not finish",
		fmt.Sprintf(
			"Customer %d (%q) was created in Kala and a later step failed (see the error "+
				"below). Kala has no delete endpoint and no deactivation flag for customers, "+
				"so this customer now exists PERMANENTLY and cannot be removed.\n\n"+
				"Terraform has recorded it in state rather than abandoning it. Apply again to "+
				"finish converging it; without this the next apply would create a SECOND "+
				"customer rather than fixing this one.",
			id, plan.Company.ValueString()),
	)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *customerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state customerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	got, err := internal.GetCustomer(ctx, state.ID.ValueInt64())
	if err != nil {
		// TF1.2: a genuine absence is drift. An UNPROVEN absence is not --
		// GetCustomer reports those separately, and treating a truncated read
		// as deletion would drop a live customer out of state and create a
		// duplicate on the next apply.
		if errors.Is(err, client.ErrNotFound) {
			tflog.Debug(ctx, "customer is gone upstream; removing from state",
				map[string]any{"id": state.ID.ValueInt64()})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Could not read the Kala customer",
			fmt.Sprintf("Customer %d.\n\nError: %s", state.ID.ValueInt64(), err.Error()),
		)
		return
	}

	applyCustomer(&state, got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *customerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state customerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	internal, ok := r.clients.requireInternal(&resp.Diagnostics)
	if !ok {
		return
	}

	id := state.ID.ValueInt64()

	// EditCustomer REPLACES the record, so the whole model is sent. Sending
	// only changed fields would blank everything else upstream.
	updated, err := internal.EditCustomer(ctx, id, plan.toInput())
	if err != nil {
		resp.Diagnostics.AddError(
			"Could not update the Kala customer",
			fmt.Sprintf("Customer %d.\n\nError: %s", id, err.Error()),
		)
		return
	}

	applyCustomer(&plan, updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete performs NO upstream write (ADR-001, ARCH1.6).
//
// Customers are the one entity here with no off switch: employees deactivate
// via SetValidated, cases archive via ArchiveCase, and customers simply remain.
// The warning is mandatory (TF1.1) -- without it the output is
// indistinguishable from a real deletion.
func (r *customerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state customerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddWarning(
		"Customer removed from state, not deleted",
		fmt.Sprintf(
			"Customer %s (%q, id %d) has been removed from Terraform state and REMAINS in "+
				"Kala.\n\n"+
				"Kala provides no way to delete a customer and no deactivation flag, so nothing "+
				"was written upstream. The customer, its cases, and its history are unchanged. "+
				"Re-adding this resource would create a SECOND customer rather than adopting "+
				"this one -- use `terraform import %d` instead.",
			state.Number.ValueString(), state.Company.ValueString(),
			state.ID.ValueInt64(), state.ID.ValueInt64()),
	)

	tflog.Debug(ctx, "customer removed from state; no upstream write",
		map[string]any{"id": state.ID.ValueInt64()})
}

// ImportState accepts the numeric customer id.
//
// The string number ("KA-4") is rejected deliberately: it looks like an
// identifier and is not the one this resource keys on, so accepting it
// silently would import the wrong record or none at all.
func (r *customerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.ParseInt(strings.TrimSpace(req.ID), 10, 64)
	if err != nil || id <= 0 {
		resp.Diagnostics.AddError(
			"Invalid import ID for kala_customer",
			fmt.Sprintf(
				"Import expects Kala's numeric customer id, for example:\n\n"+
					"    terraform import kala_customer.acme 4\n\n"+
					"Got %q. This is not the customer NUMBER (%q-style); the two are different "+
					"values and only the numeric id addresses the record.",
				req.ID, "KA-4"),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.AddWarning(
		"Imported customer is now managed by Terraform",
		"Run `terraform plan` and reconcile the configuration with what Kala holds before "+
			"applying. `kala_customer` updates replace the whole record, so an attribute the "+
			"configuration omits will be blanked upstream on the next apply.",
	)
}
