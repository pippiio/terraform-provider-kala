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
				Required:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
				MarkdownDescription: "The employee creating the case. Required by Kala at " +
					"creation and not changeable afterwards — no endpoint exists to move it.",
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

var (
	_ = errors.Is
	_ = fmt.Sprintf
	_ = tflog.Debug
	_ client.InternalClient
)

// --- stubs (RED) -----------------------------------------------------------

func (r *caseResource) Create(_ context.Context, _ resource.CreateRequest, _ *resource.CreateResponse) {
}
func (r *caseResource) Read(_ context.Context, _ resource.ReadRequest, _ *resource.ReadResponse) {}
func (r *caseResource) Update(_ context.Context, _ resource.UpdateRequest, _ *resource.UpdateResponse) {
}
func (r *caseResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}
func (r *caseResource) ImportState(_ context.Context, _ resource.ImportStateRequest, _ *resource.ImportStateResponse) {
}
