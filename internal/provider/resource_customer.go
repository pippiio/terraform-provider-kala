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

// --- stubs (RED) -----------------------------------------------------------

func (r *customerResource) Create(_ context.Context, _ resource.CreateRequest, _ *resource.CreateResponse) {
}

func (r *customerResource) Read(_ context.Context, _ resource.ReadRequest, _ *resource.ReadResponse) {
}

func (r *customerResource) Update(_ context.Context, _ resource.UpdateRequest, _ *resource.UpdateResponse) {
}

func (r *customerResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

func (r *customerResource) ImportState(_ context.Context, _ resource.ImportStateRequest, _ *resource.ImportStateResponse) {
}
