// Story: kala_case_access data source
//
// Input:  a case number.
// Process:
//   1. Read the grant via client.GetCaseAccess, which is ONE upstream call --
//      caseId, restricted and the worker set all come from the same response.
//   2. Report an unknown case number as an attribute-scoped diagnostic, never as
//      an empty set. An empty set reads as "nobody may register time", which for
//      a case that does not exist is a false statement rather than a missing one.
//   3. Map into state as a SET of int64, not a list: upstream ordering is not
//      guaranteed, and a list would show a diff on reordering alone.
//
// Output: case_id, restricted, employee_numbers.
//
// THE DESCRIPTIONS ARE THE FEATURE HERE, not decoration.
//
//	employee_numbers is incomplete by construction and nothing can detect it:
//	the grant is only observable through task assignment, so a case with no tasks
//	always reports empty however many people hold access. Two of the four cases
//	in the development tenant have no tasks. There is no test that can fail when
//	this misleads someone -- the only protection is that the schema says so, in
//	both the data source's own description and the attribute's. That is why a
//	test asserts the wording: it is the sole mechanism keeping a later edit from
//	silently removing the warning.
//
//	The canonical wording lives in
//	draft/tracks/case-time-registration-access/spike-findings.md § Appendix.
//	Change it there first.
//
// Dependencies: client.InternalClient.GetCaseAccess, configureInternal.
// Side effects: none. Read-only; this data source never writes to Kala.

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippiio/terraform-provider-kala/internal/client"
)

func NewCaseAccessDataSource() datasource.DataSource { return &caseAccessDataSource{} }

type caseAccessDataSource struct {
	client client.InternalClient
}

type caseAccessDataSourceModel struct {
	CaseNumber types.String `tfsdk:"case_number"`

	CaseID          types.Int64 `tfsdk:"case_id"`
	Restricted      types.Bool  `tfsdk:"restricted"`
	EmployeeNumbers types.Set   `tfsdk:"employee_numbers"`
}

func (d *caseAccessDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_case_access"
}

func (d *caseAccessDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{} // TODO(task 3.2)
}

func (d *caseAccessDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureInternal(req, resp, "kala_case_access")
}

func (d *caseAccessDataSource) Read(_ context.Context, _ datasource.ReadRequest, _ *datasource.ReadResponse) {
	// TODO(task 3.2)
}
