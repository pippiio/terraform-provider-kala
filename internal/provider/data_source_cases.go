// Story: Case data sources (kala_cases, kala_case)
//
// Input:  Terraform config -- optional active/search/page_size and the two
//         opt-ins on the list; a required case_number on the singular one.
// Process:
//   1. Map `active` onto the client's Archived query. archivedJobs upstream is
//      a MODE SWITCH: the active and archived sets are DISJOINT and there is no
//      single call returning both. The schema documentation says so plainly,
//      because a caller who assumes `active` merely narrows will silently miss
//      every archived case.
//   2. Default to active-only when `active` is unset, mirroring kala_employees.
//   3. Surface read coverage as `complete`, and warn when the cap was reached.
//   4. Withhold customer contact details, and case financials, unless the
//      matching opt-in is set. Financial data is both commercially
//      sensitive and transactional.
//   5. For kala_case, read the DETAIL endpoint: the list returns ~25 fields
//      against 64, so a list-derived case would be missing most of what makes
//      the singular data source worth having (confirmed).
//   6. Render dates as RFC 3339 strings; upstream sends .NET /Date(ms)/, which
//      the client has already parsed. Null stays null.
//
// Output: Terraform state.
//
// Dependencies: client.InternalClient (ListCases, GetCase).
// Side effects: none. CreateCase and ArchiveCase exist upstream and are not
//               called; neither is on the permitted-write list.

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

var (
	_ datasource.DataSource              = &casesDataSource{}
	_ datasource.DataSourceWithConfigure = &casesDataSource{}
	_ datasource.DataSource              = &caseDataSource{}
	_ datasource.DataSourceWithConfigure = &caseDataSource{}
)

type caseModel struct {
	ID                types.Int64  `tfsdk:"id"`
	Number            types.String `tfsdk:"number"`
	Name              types.String `tfsdk:"name"`
	Archived          types.Bool   `tfsdk:"archived"`
	EconomyCaseNumber types.String `tfsdk:"economy_case_number"`
	Address           types.String `tfsdk:"address"`
	Zip               types.String `tfsdk:"zip"`
	SubText           types.String `tfsdk:"sub_text"`
	CustomerName      types.String `tfsdk:"customer_name"`
	CustomerCompany   types.String `tfsdk:"customer_company"`
	InternalProject   types.Bool   `tfsdk:"internal_project"`
	Restricted        types.Bool   `tfsdk:"restricted"`
	Favorite          types.Bool   `tfsdk:"favorite"`

	CustomerEmail types.String `tfsdk:"customer_email"`
	CustomerPhone types.String `tfsdk:"customer_phone"`
}

func caseAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":     schema.Int64Attribute{Computed: true, MarkdownDescription: "Kala's internal case id. This is the value tasks reference via `case_id`."},
		"number": schema.StringAttribute{Computed: true, MarkdownDescription: "The case number, e.g. `KA-1`. This is how a case is addressed, and it is a **string** -- distinct from the integer `id`."},
		"name":   schema.StringAttribute{Computed: true, MarkdownDescription: "Case name."},
		"archived": schema.BoolAttribute{
			Computed: true,
			MarkdownDescription: "Whether this case is archived. Derived from which set was requested, " +
				"not from a response field: the list endpoint's own `isFinished` tracks archived-ness " +
				"rather than completion and disagrees with the detail endpoint.",
		},
		"economy_case_number": schema.StringAttribute{
			Computed: true,
			MarkdownDescription: "The e-conomic case number. In practice this **mirrors `number`**, " +
				"including on Kala-native internal projects that have no e-conomic counterpart.",
		},
		"address":          schema.StringAttribute{Computed: true, MarkdownDescription: "Site address."},
		"zip":              schema.StringAttribute{Computed: true, MarkdownDescription: "Postal code."},
		"sub_text":         schema.StringAttribute{Computed: true, MarkdownDescription: "Secondary descriptive line."},
		"customer_name":    schema.StringAttribute{Computed: true, MarkdownDescription: "Contact name for **this case** — the person to call about this job. Despite the attribute name this is the CASE's own contact, not a copy of the customer's record: changing it does not touch `kala_customer` (verified 2026-09-08)."},
		"customer_company": schema.StringAttribute{Computed: true, MarkdownDescription: "Customer company name."},
		"internal_project": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether this is an internal project rather than customer work. Internal projects have no customer."},
		"restricted":       schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether access to the case is restricted."},
		"favorite":         schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the case is flagged as a favourite."},

		"customer_email": contactAttr("Customer email address"),
		"customer_phone": contactAttr("Customer phone number"),
	}
}

// --- kala_cases -----------------------------------------------------------

// NewCasesDataSource returns the kala_cases data source.
func NewCasesDataSource() datasource.DataSource { return &casesDataSource{} }

type casesDataSource struct {
	client client.InternalClient
}

type casesDataSourceModel struct {
	Active                types.Bool   `tfsdk:"active"`
	Search                types.String `tfsdk:"search"`
	CustomerCompany       types.String `tfsdk:"customer_company"`
	PageSize              types.Int64  `tfsdk:"page_size"`
	IncludeContactDetails types.Bool   `tfsdk:"include_contact_details"`
	Complete              types.Bool   `tfsdk:"complete"`
	Total                 types.Int64  `tfsdk:"total"`
	Cases                 []caseModel  `tfsdk:"cases"`
}

func (d *casesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cases"
}

func (d *casesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists cases in Kala.\n\n" +
			"Kala names this entity three ways -- \"project\" on webapiv2, \"job\" in internal " +
			"endpoint names, \"case\" in responses. This provider says **case** throughout.",
		Attributes: map[string]schema.Attribute{
			"active": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Which set of cases to return. Defaults to `true`.\n\n" +
					"**These are disjoint sets, not a narrowing filter.** `true` returns only " +
					"non-archived cases; `false` returns only archived ones. Kala offers **no " +
					"single call that returns both**, so retrieving every case requires two " +
					"`kala_cases` blocks, one per value.",
			},
			"search": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Free-text filter applied upstream. Matches case names and customer fields.",
			},
			"customer_company": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Return only cases whose customer company matches exactly, " +
					"ignoring case.\n\n" +
					"**Applied client-side.** The case list carries the customer's company as text " +
					"but no customer id, so this is string matching rather than a join: it will not " +
					"follow a renamed company, and two customers sharing a company name are " +
					"indistinguishable here. Use `kala_customer` when you need the id.\n\n" +
					"Deliberately not pushed into `search`, which is a broad text match over case " +
					"names as well as customer fields, so narrowing with it could drop cases that " +
					"genuinely match.\n\n" +
					"Set to `\"\"` to select cases with **no** customer, such as internal projects. " +
					"Leaving it unset applies no filter.",
			},
			"page_size": schema.Int64Attribute{Optional: true, MarkdownDescription: "Records fetched per request while paginating."},
			"include_contact_details": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Expose the customer email and phone denormalised onto each case. Off by default; this is personal data and everything here is written to state.",
			},
			"complete": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the read covered every case in the requested set. **False means this list is a subset.**",
			},
			"total": schema.Int64Attribute{Computed: true, MarkdownDescription: "How many cases the requested set holds, as reported upstream."},
			"cases": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The cases returned.",
				NestedObject:        schema.NestedAttributeObject{Attributes: caseAttributes()},
			},
		},
	}
}

func (d *casesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureInternal(req, resp, "kala_cases")
}

func (d *casesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError(
			"Kala internal API client not configured",
			"kala_cases requires KALA_USERNAME and KALA_PASSWORD (or the provider's username and "+
				"password attributes): cases are served only by Kala's internal API.",
		)
		return
	}

	var config casesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Unset means active-only, matching kala_employees. Note this selects a SET
	// rather than narrowing one: the archived and non-archived sets are disjoint.
	archived := false
	if !config.Active.IsNull() && !config.Active.IsUnknown() {
		archived = !config.Active.ValueBool()
	}

	scan, err := d.client.ListCases(ctx, client.CaseQuery{
		Archived: archived,
		Search:   config.Search.ValueString(),
		PageSize: int(config.PageSize.ValueInt64()),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Could not read Kala cases",
			"The Kala API returned an error while listing cases.\n\nError: "+err.Error(),
		)
		return
	}

	// Client-side, over records already received. Null means "no filter"; an
	// explicit "" means "cases with no customer" -- a real distinction that
	// ValueString() alone would collapse.
	cases := scan.Cases
	if !config.CustomerCompany.IsNull() && !config.CustomerCompany.IsUnknown() {
		want := strings.ToLower(config.CustomerCompany.ValueString())
		filtered := make([]client.Case, 0, len(cases))
		for _, c := range cases {
			if strings.ToLower(c.CustomerCompany) == want {
				filtered = append(filtered, c)
			}
		}
		cases = filtered
	}

	tflog.Debug(ctx, "read Kala cases", map[string]any{
		"received": len(scan.Cases), "returned": len(cases),
		"total": scan.Total, "archived": archived, "complete": scan.Complete(),
	})

	if !scan.Complete() {
		resp.Diagnostics.AddWarning(
			"Case list is incomplete",
			fmt.Sprintf("Read %d of %d cases before reaching the pagination cap. The `cases` list "+
				"is a subset and `complete` is false. Raise `page_size` or narrow with `search`.",
				scan.Fetched, scan.Total),
		)
	}

	state := casesDataSourceModel{
		Active:                config.Active,
		Search:                config.Search,
		CustomerCompany:       config.CustomerCompany,
		PageSize:              config.PageSize,
		IncludeContactDetails: config.IncludeContactDetails,
		Complete:              types.BoolValue(scan.Complete()),
		Total:                 types.Int64Value(int64(scan.Total)),
		// Complete and Total describe the READ, not the filtered list -- the same
		// invariant the task assignee filter holds.
		Cases: buildCasesState(cases, config.IncludeContactDetails.ValueBool()),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// --- kala_case ------------------------------------------------------------

// NewCaseDataSource returns the kala_case data source.
func NewCaseDataSource() datasource.DataSource { return &caseDataSource{} }

type caseDataSource struct {
	client client.InternalClient
}

type caseDataSourceModel struct {
	CaseNumber            types.String `tfsdk:"case_number"`
	IncludeContactDetails types.Bool   `tfsdk:"include_contact_details"`
	IncludeFinancials     types.Bool   `tfsdk:"include_financials"`

	ID                types.Int64  `tfsdk:"id"`
	Number            types.String `tfsdk:"number"`
	Name              types.String `tfsdk:"name"`
	Archived          types.Bool   `tfsdk:"archived"`
	EconomyCaseNumber types.String `tfsdk:"economy_case_number"`
	Address           types.String `tfsdk:"address"`
	Zip               types.String `tfsdk:"zip"`
	SubText           types.String `tfsdk:"sub_text"`
	CustomerName      types.String `tfsdk:"customer_name"`
	CustomerCompany   types.String `tfsdk:"customer_company"`
	InternalProject   types.Bool   `tfsdk:"internal_project"`
	Restricted        types.Bool   `tfsdk:"restricted"`
	Favorite          types.Bool   `tfsdk:"favorite"`
	CustomerEmail     types.String `tfsdk:"customer_email"`
	CustomerPhone     types.String `tfsdk:"customer_phone"`

	CustomerID              types.Int64  `tfsdk:"customer_id"`
	IsFinished              types.Bool   `tfsdk:"is_finished"`
	ChecklistItemsTotal     types.Int64  `tfsdk:"checklist_items_total"`
	ChecklistItemsCompleted types.Int64  `tfsdk:"checklist_items_completed"`
	EconomySyncFailed       types.Bool   `tfsdk:"economy_sync_failed"`
	StartDate               types.String `tfsdk:"start_date"`
	EndDate                 types.String `tfsdk:"end_date"`
	Deadline                types.String `tfsdk:"deadline"`

	Cost                 types.Int64 `tfsdk:"cost"`
	Sales                types.Int64 `tfsdk:"sales"`
	Result               types.Int64 `tfsdk:"result"`
	Invoiced             types.Int64 `tfsdk:"invoiced"`
	Uninvoiced           types.Int64 `tfsdk:"uninvoiced"`
	Realised             types.Int64 `tfsdk:"realised"`
	RegisteredHoursTotal types.Int64 `tfsdk:"registered_hours_total"`
	BilledHours          types.Int64 `tfsdk:"billed_hours"`
}

func (d *caseDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_case"
}

func (d *caseDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := caseAttributes()
	attrs["case_number"] = schema.StringAttribute{
		Required:            true,
		MarkdownDescription: "The case number to look up, e.g. `KA-1`. Note this is the **string** number, not the integer `id` that tasks reference.",
	}
	attrs["include_contact_details"] = schema.BoolAttribute{Optional: true, MarkdownDescription: "Expose the customer email and phone. Off by default."}
	attrs["include_financials"] = schema.BoolAttribute{
		Optional: true,
		MarkdownDescription: "Expose cost, sales, result, invoiced, uninvoiced, realised, and hour totals. " +
			"**Off by default.** This is commercially sensitive and transactional data, and everything " +
			"exposed here is written to Terraform state.",
	}
	attrs["customer_id"] = schema.Int64Attribute{Computed: true, MarkdownDescription: "The customer this case belongs to. Null on internal projects. Join to `kala_customer.id`."}
	attrs["is_finished"] = schema.BoolAttribute{
		Computed: true,
		MarkdownDescription: "Whether the case is finished. Sourced from the **detail** endpoint, where it means completion. " +
			"The list endpoint has a field of the same name that tracks archived-ness instead; that one is not exposed.",
	}
	attrs["checklist_items_total"] = schema.Int64Attribute{Computed: true, MarkdownDescription: "How many tasks (checklist items) the case holds."}
	attrs["checklist_items_completed"] = schema.Int64Attribute{Computed: true, MarkdownDescription: "How many of those are finished."}
	attrs["economy_sync_failed"] = schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the last e-conomic sync failed. True on internal projects, which cannot sync."}
	for name, desc := range map[string]string{
		"start_date": "Case start date", "end_date": "Case end date", "deadline": "Case deadline",
	} {
		attrs[name] = schema.StringAttribute{Computed: true, MarkdownDescription: desc + ", as RFC 3339. Null when unset upstream."}
	}
	for name, desc := range map[string]string{
		"cost": "Recorded cost", "sales": "Recorded sales", "result": "Sales minus cost",
		"invoiced": "Amount invoiced", "uninvoiced": "Amount not yet invoiced", "realised": "Realised value",
		"registered_hours_total": "Hours registered on the case", "billed_hours": "Hours billed",
	} {
		attrs[name] = schema.Int64Attribute{
			Computed:            true,
			MarkdownDescription: desc + ". **Null unless `include_financials` is set.**",
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up one case by case number, returning the full detail record.\n\n" +
			"The list endpoint returns roughly 25 fields; this one returns 64. Anything beyond " +
			"identity and customer name must come from here.",
		Attributes: attrs,
	}
}

func (d *caseDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureInternal(req, resp, "kala_case")
}

func (d *caseDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError(
			"Kala internal API client not configured",
			"kala_case requires KALA_USERNAME and KALA_PASSWORD (or the provider's username and "+
				"password attributes): cases are served only by Kala's internal API.",
		)
		return
	}

	var config caseDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	number := config.CaseNumber.ValueString()
	detail, err := d.client.GetCase(ctx, number)
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.Diagnostics.AddAttributeError(
				path.Root("case_number"),
				"Case not found",
				fmt.Sprintf("No case numbered %q exists in this Kala account.\n\n"+
					"Note that Kala answers an unknown case number with an HTTP 500 rather than a "+
					"404; the provider retries first, so this is reported only after a consistent "+
					"failure.", number),
			)
			return
		}
		resp.Diagnostics.AddError(
			"Could not read Kala case",
			"The Kala API returned an error while reading the case.\n\nError: "+err.Error(),
		)
		return
	}

	tflog.Debug(ctx, "read Kala case", map[string]any{"case_number": number})

	contacts := config.IncludeContactDetails.ValueBool()
	fin := config.IncludeFinancials.ValueBool()
	base := buildCaseModel(detail.Case, contacts)

	state := caseDataSourceModel{
		CaseNumber:            config.CaseNumber,
		IncludeContactDetails: config.IncludeContactDetails,
		IncludeFinancials:     config.IncludeFinancials,

		ID:                base.ID,
		Number:            base.Number,
		Name:              base.Name,
		Archived:          base.Archived,
		EconomyCaseNumber: base.EconomyCaseNumber,
		Address:           base.Address,
		Zip:               base.Zip,
		SubText:           base.SubText,
		CustomerName:      base.CustomerName,
		CustomerCompany:   base.CustomerCompany,
		InternalProject:   base.InternalProject,
		Restricted:        base.Restricted,
		Favorite:          base.Favorite,
		CustomerEmail:     base.CustomerEmail,
		CustomerPhone:     base.CustomerPhone,

		CustomerID:              types.Int64Value(detail.CustomerID),
		IsFinished:              types.BoolValue(detail.IsFinished),
		ChecklistItemsTotal:     types.Int64Value(int64(detail.ChecklistItemsTotal)),
		ChecklistItemsCompleted: types.Int64Value(int64(detail.ChecklistItemsCompleted)),
		EconomySyncFailed:       types.BoolValue(detail.EconomySyncFailed),
		StartDate:               rfc3339OrNull(detail.StartDate),
		EndDate:                 rfc3339OrNull(detail.EndDate),
		Deadline:                rfc3339OrNull(detail.Deadline),

		Cost:                 int64OrNull(detail.Cost, fin),
		Sales:                int64OrNull(detail.Sales, fin),
		Result:               int64OrNull(detail.Result, fin),
		Invoiced:             int64OrNull(detail.Invoiced, fin),
		Uninvoiced:           int64OrNull(detail.Uninvoiced, fin),
		Realised:             int64OrNull(detail.Realised, fin),
		RegisteredHoursTotal: int64OrNull(detail.RegisteredHoursTotal, fin),
		BilledHours:          int64OrNull(detail.BilledHours, fin),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// configureInternal resolves the internal-API client from provider data,
// shared by every data source that needs it.
func configureInternal(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse, dsName string) client.InternalClient {
	if req.ProviderData == nil {
		return nil
	}
	c, ok := req.ProviderData.(*providerClients)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data",
			"The "+dsName+" data source expected configured Kala clients. This is a bug in the provider.",
		)
		return nil
	}
	return c.Internal
}

// buildCasesState converts domain cases into state models.
func buildCasesState(cases []client.Case, includeContacts bool) []caseModel {
	// Always non-nil: nil renders as null in state and produces a spurious diff.
	out := make([]caseModel, 0, len(cases))
	for _, c := range cases {
		out = append(out, buildCaseModel(c, includeContacts))
	}
	return out
}

func buildCaseModel(c client.Case, includeContacts bool) caseModel {
	m := caseModel{
		ID:                types.Int64Value(c.ID),
		Number:            types.StringValue(c.Number),
		Name:              types.StringValue(c.Name),
		Archived:          types.BoolValue(c.Archived),
		EconomyCaseNumber: types.StringValue(c.EconomyCaseNumber),
		Address:           types.StringValue(c.Address),
		Zip:               types.StringValue(c.Zip),
		SubText:           types.StringValue(c.SubText),
		CustomerName:      types.StringValue(c.CustomerName),
		CustomerCompany:   types.StringValue(c.CustomerCompany),
		InternalProject:   types.BoolValue(c.InternalProject),
		Restricted:        types.BoolValue(c.Restricted),
		Favorite:          types.BoolValue(c.Favorite),

		CustomerEmail: types.StringNull(),
		CustomerPhone: types.StringNull(),
	}
	if includeContacts {
		m.CustomerEmail = types.StringValue(c.CustomerEmail)
		m.CustomerPhone = types.StringValue(c.CustomerPhone)
	}
	return m
}

// rfc3339OrNull renders a parsed timestamp, preserving null.
//
// The client has already converted upstream's .NET /Date(ms)/ form; this only
// decides the string rendering. Null must survive as null rather than becoming
// the zero time, which would read as year 1 in state.
func rfc3339OrNull(t *time.Time) types.String {
	if t == nil {
		return types.StringNull()
	}
	return types.StringValue(t.UTC().Format(time.RFC3339))
}

// int64OrNull gates a value behind an opt-in, preserving null when withheld.
func int64OrNull(v int, include bool) types.Int64 {
	if !include {
		return types.Int64Null()
	}
	return types.Int64Value(int64(v))
}
