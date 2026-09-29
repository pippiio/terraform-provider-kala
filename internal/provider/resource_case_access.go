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

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
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
	// SKELETON: shape only; plan modifiers and descriptions come with task 2.2.
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":              schema.StringAttribute{Computed: true},
			"case_number":     schema.StringAttribute{Required: true},
			"employee_number": schema.Int64Attribute{Required: true},
			"case_id":         schema.Int64Attribute{Computed: true},
		},
	}
}

func (r *caseAccessResource) Configure(_ context.Context, _ resource.ConfigureRequest, _ *resource.ConfigureResponse) {
}

func (r *caseAccessResource) Create(_ context.Context, _ resource.CreateRequest, _ *resource.CreateResponse) {
}

func (r *caseAccessResource) Read(_ context.Context, _ resource.ReadRequest, _ *resource.ReadResponse) {
}

func (r *caseAccessResource) Update(_ context.Context, _ resource.UpdateRequest, _ *resource.UpdateResponse) {
}

func (r *caseAccessResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

func (r *caseAccessResource) ImportState(_ context.Context, _ resource.ImportStateRequest, _ *resource.ImportStateResponse) {
}
