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
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/pippiio/terraform-provider-kala/internal/client"
)

// The limitation wording, written once and used in both placements.
//
// It is a constant rather than two hand-written strings because three
// descriptions drafted independently end up saying subtly different things, and
// the reader believes the mildest one. The canonical source is
// draft/tracks/case-time-registration-access/spike-findings.md § Appendix;
// change it there first.
//
// The register is deliberately DEFINITE. "May not include everyone" was drafted
// and rejected as too soft: a case with no tasks does not merely risk reporting
// empty, it ALWAYS reports empty, and two of the four cases in the development
// tenant have no tasks.
const (
	caseAccessLimitationLong = "**This list is derived from task assignment, and it is incomplete " +
		"by construction.**\n\n" +
		"Kala exposes no endpoint that reports who may register time on a case. " +
		"`employee_numbers` is computed from the employees assigned to the case's individual " +
		"tasks, which is the only readable source. Three consequences follow, and this provider " +
		"cannot detect any of them:\n\n" +
		"- **A case with no tasks always reports an empty set**, however many employees have " +
		"been granted access to it.\n" +
		"- **An employee granted access but not assigned to any task on the case never " +
		"appears.**\n" +
		"- The set reports who has been *granted* access, not who is currently *able* to " +
		"register time. A deactivated employee may remain in it, and will not resolve through " +
		"`kala_employee`.\n\n" +
		"`restricted` does not resolve this. `restricted = false` means access is unrestricted, " +
		"so the set says nothing at all. `restricted = true` with an empty set means **either** " +
		"that nobody has been granted access **or** that those who have hold no tasks — the two " +
		"are indistinguishable.\n\n" +
		"**Do not use this attribute as an authorization check.** It reports configuration for " +
		"review; it does not decide access."

	caseAccessLimitationShort = "The employees granted access to this case specifically, derived " +
		"from the employees assigned to its tasks — the only source Kala exposes.\n\n" +
		"**Incomplete by construction, and undetectably so.** A case with no tasks always " +
		"reports an empty set regardless of who has access, and an employee granted access " +
		"without a task on the case never appears. Reports who is *granted* access, not who is " +
		"*able* to register time: a deactivated employee may remain in the set and will not " +
		"resolve through `kala_employee`, so take care iterating it with `for_each`.\n\n" +
		"Read together with `restricted`: `restricted = true` with an empty set means **either** " +
		"that nobody is granted access **or** that nobody granted holds a task. " +
		"**Not an authorization check.**"
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
	resp.Schema = schema.Schema{
		MarkdownDescription: "Which employees are granted access to a Kala case — the employees " +
			"who may register time against it.\n\n" +
			caseAccessLimitationLong,
		Attributes: map[string]schema.Attribute{
			"case_number": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The case number to look up, e.g. `KA-1`. This is the " +
					"**string** number that `kala_case` exposes as `number`, not the integer `id`.",
			},
			"case_id": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Kala's integer id for the case. Join to `kala_task.case_id`.",
			},
			"restricted": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "Whether access to this case is limited at all. `false` means " +
					"the case is **unrestricted**, and `employee_numbers` then says nothing about " +
					"who may register time on it.\n\n" +
					"Only meaningful read together with `employee_numbers` — and see that " +
					"attribute for why an empty set is ambiguous even when this is `true`.",
			},
			"employee_numbers": schema.SetAttribute{
				Computed:            true,
				ElementType:         types.Int64Type,
				MarkdownDescription: caseAccessLimitationShort,
			},
		},
	}
}

func (d *caseAccessDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureInternal(req, resp, "kala_case_access")
}

func (d *caseAccessDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError(
			"Kala internal API client not configured",
			"kala_case_access requires KALA_USERNAME and KALA_PASSWORD (or the provider's username "+
				"and password attributes): case access is served only by Kala's internal API.",
		)
		return
	}

	var config caseAccessDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// TF1.5: never call ValueString on a value that may be null or unknown. A
	// null case_number here would otherwise read as the empty string and ask
	// Kala about a case called "", whose 500 would then be reported as "no such
	// case" -- a confusing answer to a question the operator never asked.
	if config.CaseNumber.IsNull() || config.CaseNumber.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("case_number"),
			"case_number is required",
			"kala_case_access looks a case up by its number, so the value must be known at plan "+
				"time. If it comes from a resource that has not been created yet, reference "+
				"`kala_case.<name>.number` so Terraform orders them.",
		)
		return
	}
	number := config.CaseNumber.ValueString()

	access, err := d.client.GetCaseAccess(ctx, number)
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			// Never an empty set. An empty set states that nobody may register
			// time, which for a case that does not exist is false rather than
			// merely missing.
			resp.Diagnostics.AddAttributeError(
				path.Root("case_number"),
				"Case not found",
				fmt.Sprintf("No case numbered %q exists in this Kala account.\n\n"+
					"Kala answers an unknown case number with an HTTP 500 rather than a 404, so "+
					"the provider retries first and reports this only after a consistent failure. "+
					"That also means a sustained Kala outage can surface here; if the case number "+
					"is definitely correct, check Kala before changing your configuration.", number),
			)
			return
		}
		resp.Diagnostics.AddError(
			"Could not read Kala case access",
			"The Kala API returned an error while reading which employees may register time on "+
				"case "+number+".\n\nError: "+err.Error(),
		)
		return
	}

	// Identifiers only, so this logs the count rather than the members (SEC1.5).
	tflog.Debug(ctx, "read kala case access", map[string]any{
		"case_number": number,
		"case_id":     access.CaseID,
		"restricted":  access.Restricted,
		"granted":     len(access.EmployeeNumbers),
	})

	// No early return on these diagnostics, deliberately. Converting a []int64
	// into a set of int64 has no failure mode, so a guard here would be an
	// untestable branch -- and it would be redundant anyway: the framework
	// discards the state it is handed whenever the response carries an error
	// diagnostic, so appending is enough for the failure to surface.
	numbers, diags := types.SetValueFrom(ctx, types.Int64Type, access.EmployeeNumbers)
	resp.Diagnostics.Append(diags...)

	resp.Diagnostics.Append(resp.State.Set(ctx, &caseAccessDataSourceModel{
		CaseNumber:      config.CaseNumber,
		CaseID:          types.Int64Value(access.CaseID),
		Restricted:      types.BoolValue(access.Restricted),
		EmployeeNumbers: numbers,
	})...)
}
