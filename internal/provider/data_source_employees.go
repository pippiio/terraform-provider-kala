package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

var (
	_ datasource.DataSource              = &employeesDataSource{}
	_ datasource.DataSourceWithConfigure = &employeesDataSource{}
)

// NewEmployeesDataSource returns the kala_employees data source.
func NewEmployeesDataSource() datasource.DataSource {
	return &employeesDataSource{}
}

type employeesDataSource struct {
	client client.Client
}

type employeesDataSourceModel struct {
	PageSize  types.Int64     `tfsdk:"page_size"`
	Employees []employeeModel `tfsdk:"employees"`
}

type employeeModel struct {
	Number   types.Int64    `tfsdk:"number"`
	Name     types.String   `tfsdk:"name"`
	Title    types.String   `tfsdk:"title"`
	Phone    types.String   `tfsdk:"phone"`
	Image    types.String   `tfsdk:"image"`
	IsAdmin  types.Bool     `tfsdk:"is_admin"`
	IsLeader types.Bool     `tfsdk:"is_leader"`
	Settings []settingModel `tfsdk:"settings"`
}

type settingModel struct {
	Key   types.String `tfsdk:"key"`
	Value types.String `tfsdk:"value"`
}

func (d *employeesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_employees"
}

func (d *employeesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists active employees in Kala.",
		Attributes: map[string]schema.Attribute{
			"page_size": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "Records fetched per request while paginating. Defaults to a value well " +
					"below the API's own default of 5000, to bound response size.",
			},
			"employees": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The active employees returned by Kala.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"number": schema.Int64Attribute{
							Computed:            true,
							MarkdownDescription: "Kala employee number. This is the record's identity.",
						},
						"name":  schema.StringAttribute{Computed: true, MarkdownDescription: "Employee's full name."},
						"title": schema.StringAttribute{Computed: true, MarkdownDescription: "Job title."},
						"phone": schema.StringAttribute{Computed: true, MarkdownDescription: "Phone number."},
						"image": schema.StringAttribute{Computed: true, MarkdownDescription: "URL of the employee's profile image."},
						"is_admin": schema.BoolAttribute{
							Computed: true, MarkdownDescription: "Whether the employee has administrator rights.",
						},
						"is_leader": schema.BoolAttribute{
							Computed: true, MarkdownDescription: "Whether the employee is a leader.",
						},
						"settings": schema.ListNestedAttribute{
							Computed: true,
							MarkdownDescription: "Configuration settings on the employee. Kala's read endpoints " +
								"return only `key` and `value`; the `friendlyName` and `type` fields that writes " +
								"require are never returned, so they cannot be exposed here.",
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"key":   schema.StringAttribute{Computed: true, MarkdownDescription: "Setting key."},
									"value": schema.StringAttribute{Computed: true, MarkdownDescription: "Setting value."},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (d *employeesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		// Normal during early plan walks — the provider has not been configured yet.
		return
	}

	c, ok := req.ProviderData.(client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			"The kala_employees data source expected a configured Kala client. This is a bug in the provider.",
		)
		return
	}
	d.client = c
}

func (d *employeesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError(
			"Kala client not configured",
			"The provider was not configured before this data source was read. This is a bug in the provider.",
		)
		return
	}

	var config employeesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	employees, err := d.client.ListEmployees(ctx, client.ListOptions{
		PageSize: int(config.PageSize.ValueInt64()),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Could not read Kala employees",
			"The Kala API returned an error while listing employees.\n\nError: "+err.Error(),
		)
		return
	}

	// Log the count only. Employee records are personal data and must never be
	// bulk-logged (guardrail SEC1.5) — the reference prototype printed the raw
	// response to stdout, which is exactly what this avoids.
	tflog.Debug(ctx, "read Kala employees", map[string]any{"count": len(employees)})

	state := employeesDataSourceModel{
		PageSize:  config.PageSize,
		Employees: buildEmployeesState(employees, nil),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// buildEmployeesState converts domain employees into the data source's state
// model.
//
// Extracted from Read so the conversion can be tested without the framework's
// state plumbing — mapping bugs are the usual cause of perpetual diffs, so this
// is the part worth testing directly. The unused second parameter is reserved
// for future per-employee filtering.
func buildEmployeesState(employees []client.Employee, _ any) []employeeModel {
	out := make([]employeeModel, 0, len(employees))

	for _, e := range employees {
		// Always non-nil: a nil slice renders as null in state and would produce
		// a spurious diff against a config expecting an empty list.
		settings := make([]settingModel, 0, len(e.Settings))
		for _, s := range e.Settings {
			settings = append(settings, settingModel{
				Key:   types.StringValue(s.Key),
				Value: types.StringValue(s.Value),
			})
		}

		out = append(out, employeeModel{
			Number:   types.Int64Value(e.Number),
			Name:     types.StringValue(e.Name),
			Title:    types.StringValue(e.Title),
			Phone:    types.StringValue(e.Phone),
			Image:    types.StringValue(e.Image),
			IsAdmin:  types.BoolValue(e.IsAdmin),
			IsLeader: types.BoolValue(e.IsLeader),
			Settings: settings,
		})
	}

	return out
}
