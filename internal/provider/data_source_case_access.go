// Story: kala_case_access data source
//
// Kala keeps two lists per case, and this data source reports both, plus the
// disagreement between them:
//
//   - granted_employee_numbers: Kala's own access list (GET /api/GrantedWorkers/).
//   - assigned_employee_numbers: employees assigned to the case's tasks.
//   - assigned_without_access: on a RESTRICTED case, those assigned but not
//     granted -- they are assigned to tasks they cannot see. Always empty on an
//     unrestricted case, where every employee has access.
//
// The last is the point of reporting both. Case KA-2 was found live with an
// employee assigned whose access had been revoked, and an earlier version of
// this data source -- which reported the assigned set as access -- showed him as
// still having it.
//
// Input:  a case number.
// Output: case_id, restricted, and the three sets above.
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

func NewCaseAccessDataSource() datasource.DataSource { return &caseAccessDataSource{} }

type caseAccessDataSource struct {
	client client.InternalClient
}

type caseAccessDataSourceModel struct {
	CaseNumber types.String `tfsdk:"case_number"`

	CaseID                  types.Int64 `tfsdk:"case_id"`
	Restricted              types.Bool  `tfsdk:"restricted"`
	GrantedEmployeeNumbers  types.Set   `tfsdk:"granted_employee_numbers"`
	AssignedEmployeeNumbers types.Set   `tfsdk:"assigned_employee_numbers"`
	AssignedWithoutAccess   types.Set   `tfsdk:"assigned_without_access"`
}

func (d *caseAccessDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_case_access"
}

func (d *caseAccessDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	set := func(desc string) schema.SetAttribute {
		return schema.SetAttribute{Computed: true, ElementType: types.Int64Type, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Who is granted access to a Kala case, and who is assigned to its tasks.",
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
			"restricted":                schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether access to the case is restricted."},
			"granted_employee_numbers":  set("Employees granted access."),
			"assigned_employee_numbers": set("Employees assigned to its tasks."),
			"assigned_without_access":   set("Assigned but not granted."),
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

	// Identifiers only, so this logs counts rather than members (SEC1.5).
	tflog.Debug(ctx, "read kala case access", map[string]any{
		"case_number": number,
		"case_id":     access.CaseID,
		"restricted":  access.Restricted,
		"granted":     len(access.Granted),
		"assigned":    len(access.Assigned),
	})

	// SKELETON (task: granted list): granted and assigned_without_access are
	// empty until the failing tests drive them.
	resp.Diagnostics.Append(resp.State.Set(ctx, &caseAccessDataSourceModel{
		CaseNumber:              config.CaseNumber,
		CaseID:                  types.Int64Value(access.CaseID),
		Restricted:              types.BoolValue(access.Restricted),
		GrantedEmployeeNumbers:  int64Set(ctx, resp, nil),
		AssignedEmployeeNumbers: int64Set(ctx, resp, access.Assigned),
		AssignedWithoutAccess:   int64Set(ctx, resp, nil),
	})...)
}

// int64Set converts a slice into a set of int64.
//
// No early return on the diagnostics, deliberately. Converting a []int64 into a
// set of int64 has no failure mode, so a guard would be an untestable branch --
// and a redundant one: the framework discards the state it is handed whenever
// the response carries an error diagnostic, so appending is enough.
func int64Set(ctx context.Context, resp *datasource.ReadResponse, in []int64) types.Set {
	if in == nil {
		in = []int64{}
	}
	v, diags := types.SetValueFrom(ctx, types.Int64Type, in)
	resp.Diagnostics.Append(diags...)
	return v
}
