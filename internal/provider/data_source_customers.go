// Story: Customer data sources (kala_customers, kala_customer)
//
// Input:  Terraform config -- optional search/page_size/include_contact_details
//         on the list; a required id on the singular one.
// Process:
//   1. Read customers through the internal-API client, which pages until the
//      account is covered or its cap is reached and reports which happened.
//   2. Surface that coverage as a `complete` attribute rather than hiding it.
//      Upstream returns totalCount, so this is a direct comparison, not a guess.
//   3. Withhold contact details unless include_contact_details is set. Email,
//      phone, address, zip, city, and ean are personal and commercial data, and
//      everything a data source exposes is written to Terraform state.
//   4. For kala_customer, select by id from the list. Upstream has NO by-id
//      endpoint, so this is client-side selection, not a lookup.
//   5. When the id is absent from an INCOMPLETE read, say the read was
//      incomplete -- never "no such customer". The read did not cover the
//      account, so absence proves nothing. Reporting it as not-found would be
//      a confident wrong answer, which is the failure mode this whole track
//      keeps running into.
// Output: Terraform state.
//
// Dependencies: client.InternalClient (ListCustomers).
// Side effects: none. Reads only; customers are owned by e-conomic.

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

var (
	_ datasource.DataSource              = &customersDataSource{}
	_ datasource.DataSourceWithConfigure = &customersDataSource{}
	_ datasource.DataSource              = &customerDataSource{}
	_ datasource.DataSourceWithConfigure = &customerDataSource{}
)

// customerModel is shared by both data sources.
//
// Contact fields are null unless the caller opted in. Null means "not
// requested" rather than "empty upstream"; the two are deliberately
// indistinguishable here because exposing which is which would leak the very
// thing the opt-in withholds.
type customerModel struct {
	ID        types.Int64  `tfsdk:"id"`
	Number    types.String `tfsdk:"number"`
	FirstName types.String `tfsdk:"first_name"`
	LastName  types.String `tfsdk:"last_name"`
	Company   types.String `tfsdk:"company"`
	CVR       types.String `tfsdk:"cvr"`
	CaseCount types.Int64  `tfsdk:"case_count"`

	Email   types.String `tfsdk:"email"`
	Phone   types.String `tfsdk:"phone"`
	Address types.String `tfsdk:"address"`
	Zip     types.String `tfsdk:"zip"`
	City    types.String `tfsdk:"city"`
	EAN     types.String `tfsdk:"ean"`
}

func customerAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.Int64Attribute{
			Computed: true,
			MarkdownDescription: "Kala's internal customer id. This is the value `kala_case` " +
				"records reference, and the identifier to join on.",
		},
		"number": schema.StringAttribute{
			Computed: true,
			MarkdownDescription: "The customer number. A **string** on this API. Do not assume it " +
				"equals `id`, and do not assume it matches the integer `number` webapiv2 returns " +
				"for the same customer -- that equivalence is unverified.",
		},
		"first_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Contact's first name."},
		"last_name":  schema.StringAttribute{Computed: true, MarkdownDescription: "Contact's last name."},
		"company":    schema.StringAttribute{Computed: true, MarkdownDescription: "Company name."},
		"cvr":        schema.StringAttribute{Computed: true, MarkdownDescription: "Danish CVR company registration number."},
		"case_count": schema.Int64Attribute{Computed: true, MarkdownDescription: "How many cases reference this customer."},

		"email":   contactAttr("Email address"),
		"phone":   contactAttr("Phone number"),
		"address": contactAttr("Street address"),
		"zip":     contactAttr("Postal code"),
		"city":    contactAttr("City"),
		"ean":     contactAttr("EAN number used for invoicing"),
	}
}

func contactAttr(desc string) schema.Attribute {
	return schema.StringAttribute{
		Computed: true,
		MarkdownDescription: desc + ". **Null unless `include_contact_details` is set** -- " +
			"contact data is personal data and everything exposed here is written to Terraform state.",
	}
}

// --- kala_customers -------------------------------------------------------

// NewCustomersDataSource returns the kala_customers data source.
func NewCustomersDataSource() datasource.DataSource { return &customersDataSource{} }

type customersDataSource struct {
	client client.InternalClient
}

type customersDataSourceModel struct {
	Search                types.String    `tfsdk:"search"`
	PageSize              types.Int64     `tfsdk:"page_size"`
	IncludeContactDetails types.Bool      `tfsdk:"include_contact_details"`
	Complete              types.Bool      `tfsdk:"complete"`
	Total                 types.Int64     `tfsdk:"total"`
	Customers             []customerModel `tfsdk:"customers"`
}

func (d *customersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_customers"
}

func (d *customersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists customers in Kala. Customers are imported from e-conomic, " +
			"which owns them, so they are read-only here by design rather than by API limitation.",
		Attributes: map[string]schema.Attribute{
			"search": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Free-text filter applied upstream.",
			},
			"page_size": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Records fetched per request while paginating.",
			},
			"include_contact_details": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Expose email, phone, address, zip, city, and ean. Off by " +
					"default: these are personal data under GDPR and Terraform state must be " +
					"treated as confidential.",
			},
			"complete": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "Whether the read covered every customer the account holds. " +
					"**False means this list is a subset** -- the page cap was reached before the " +
					"end of the data. Acting on a partial list as though it were whole is a bug.",
			},
			"total": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "How many customers the account holds, as reported upstream.",
			},
			"customers": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The customers returned.",
				NestedObject:        schema.NestedAttributeObject{Attributes: customerAttributes()},
			},
		},
	}
}

func (d *customersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*providerClients)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data",
			"The kala_customers data source expected configured Kala clients. This is a bug in the provider.",
		)
		return
	}
	d.client = c.Internal
}

func (d *customersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError(
			"Kala internal API client not configured",
			"kala_customers requires KALA_USERNAME and KALA_PASSWORD (or the provider's username "+
				"and password attributes): customers are served only by Kala's internal API.",
		)
		return
	}

	var config customersDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scan, err := d.client.ListCustomers(ctx, client.CustomerQuery{
		Search:   config.Search.ValueString(),
		PageSize: int(config.PageSize.ValueInt64()),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Could not read Kala customers",
			"The Kala API returned an error while listing customers.\n\nError: "+err.Error(),
		)
		return
	}

	// Counts only. Customer records carry personal and commercial data and must
	// never be bulk-logged.
	tflog.Debug(ctx, "read Kala customers", map[string]any{
		"count": len(scan.Customers), "total": scan.Total, "complete": scan.Complete(),
	})

	if !scan.Complete() {
		resp.Diagnostics.AddWarning(
			"Customer list is incomplete",
			fmt.Sprintf("Read %d of %d customers before reaching the pagination cap. "+
				"The `customers` list is a subset and `complete` is false. Raise `page_size` "+
				"or narrow with `search` if you need the whole account.", scan.Fetched, scan.Total),
		)
	}

	state := customersDataSourceModel{
		Search:                config.Search,
		PageSize:              config.PageSize,
		IncludeContactDetails: config.IncludeContactDetails,
		Complete:              types.BoolValue(scan.Complete()),
		Total:                 types.Int64Value(int64(scan.Total)),
		Customers:             buildCustomersState(scan.Customers, config.IncludeContactDetails.ValueBool()),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// --- kala_customer --------------------------------------------------------

// NewCustomerDataSource returns the kala_customer data source.
func NewCustomerDataSource() datasource.DataSource { return &customerDataSource{} }

type customerDataSource struct {
	client client.InternalClient
}

// customerDataSourceModel is flattened rather than embedding customerModel:
// the framework's reflection does not resolve tfsdk tags through an embedded
// struct, so the fields are spelled out.
type customerDataSourceModel struct {
	ID                    types.Int64  `tfsdk:"id"`
	IncludeContactDetails types.Bool   `tfsdk:"include_contact_details"`
	Number                types.String `tfsdk:"number"`
	FirstName             types.String `tfsdk:"first_name"`
	LastName              types.String `tfsdk:"last_name"`
	Company               types.String `tfsdk:"company"`
	CVR                   types.String `tfsdk:"cvr"`
	CaseCount             types.Int64  `tfsdk:"case_count"`
	Email                 types.String `tfsdk:"email"`
	Phone                 types.String `tfsdk:"phone"`
	Address               types.String `tfsdk:"address"`
	Zip                   types.String `tfsdk:"zip"`
	City                  types.String `tfsdk:"city"`
	EAN                   types.String `tfsdk:"ean"`
}

func (d *customerDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_customer"
}

func (d *customerDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := customerAttributes()
	// Selectors. Exactly one must be set; which one is checked at read time
	// rather than by a schema validator, so the diagnostic can name all of them.
	attrs["id"] = schema.Int64Attribute{
		Optional: true, Computed: true,
		MarkdownDescription: "Look up by Kala's internal customer id. Unlike the other selectors " +
			"this cannot be narrowed upstream -- Kala's `query` parameter searches text -- so an " +
			"id lookup reads the customer list and selects from it.",
	}
	attrs["number"] = schema.StringAttribute{
		Optional: true, Computed: true,
		MarkdownDescription: "Look up by customer number, e.g. `K-001`. Narrowed upstream before matching.",
	}
	attrs["cvr"] = schema.StringAttribute{
		Optional: true, Computed: true,
		MarkdownDescription: "Look up by Danish CVR registration number. Narrowed upstream before matching.",
	}
	attrs["include_contact_details"] = schema.BoolAttribute{
		Optional:            true,
		MarkdownDescription: "Expose email, phone, address, zip, city, and ean. Off by default.",
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a single customer by `id`, `number`, or `cvr`. Set exactly one.\n\n" +
			"Kala has **no single-customer endpoint**. `number` and `cvr` are narrowed upstream " +
			"before matching, so they do not read the whole account; `id` cannot be, because Kala's " +
			"query parameter searches text.\n\n" +
			"If the account is larger than the read can cover, the lookup reports that the read was " +
			"incomplete rather than claiming the customer does not exist.",
		Attributes: attrs,
	}
}

func (d *customerDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*providerClients)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data",
			"The kala_customer data source expected configured Kala clients. This is a bug in the provider.",
		)
		return
	}
	d.client = c.Internal
}

func (d *customerDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError(
			"Kala internal API client not configured",
			"kala_customer requires KALA_USERNAME and KALA_PASSWORD (or the provider's username "+
				"and password attributes): customers are served only by Kala's internal API.",
		)
		return
	}

	var config customerDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sel, err := resolveCustomerSelector(config)
	if err != nil {
		resp.Diagnostics.AddError("Ambiguous or missing customer selector", err.Error())
		return
	}

	// Narrow server-side where the selector allows it. This is what keeps a cvr
	// lookup from having to read the whole account.
	scan, err := d.client.ListCustomers(ctx, client.CustomerQuery{Search: sel.prefilter})
	if err != nil {
		resp.Diagnostics.AddError(
			"Could not read Kala customers",
			"The Kala API returned an error while looking up the customer.\n\nError: "+err.Error(),
		)
		return
	}

	// The query parameter narrows; it does not exact-match. Filtering here is
	// what turns "customers mentioning 12345678" into "the customer whose cvr
	// IS 12345678".
	matches, partial := matchCustomers(scan, sel.match, config.IncludeContactDetails.ValueBool())

	switch {
	case len(matches) == 1:
		// resolved
	case len(matches) > 1:
		resp.Diagnostics.AddAttributeError(
			path.Root(sel.attr),
			"Customer lookup matched more than one record",
			fmt.Sprintf("%d customers have %s = %q. A data source must resolve to exactly one "+
				"record, so this cannot be narrowed automatically. Use `id`, which is unique.",
				len(matches), sel.attr, sel.value),
		)
		return
	case partial:
		resp.Diagnostics.AddAttributeError(
			path.Root(sel.attr),
			"Customer lookup could not be completed",
			fmt.Sprintf("No customer with %s = %q was among the %d of %d records read before the "+
				"pagination cap was reached, so it cannot be reported as missing. Raise "+
				"`page_size` and try again.", sel.attr, sel.value, scan.Fetched, scan.Total),
		)
		return
	default:
		resp.Diagnostics.AddAttributeError(
			path.Root(sel.attr),
			"Customer not found",
			fmt.Sprintf("No customer with %s = %q exists in this Kala account.", sel.attr, sel.value),
		)
		return
	}

	found := matches[0]
	tflog.Debug(ctx, "read Kala customer", map[string]any{"selector": sel.attr})

	state := customerDataSourceModel{
		IncludeContactDetails: config.IncludeContactDetails,
		ID:                    found.ID,
		Number:                found.Number,
		CVR:                   found.CVR,
		FirstName:             found.FirstName,
		LastName:              found.LastName,
		Company:               found.Company,
		CaseCount:             found.CaseCount,
		Email:                 found.Email,
		Phone:                 found.Phone,
		Address:               found.Address,
		Zip:                   found.Zip,
		City:                  found.City,
		EAN:                   found.EAN,
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// buildCustomersState converts domain customers into state models.
//
// Extracted from Read so the mapping -- including the contact opt-in, which is
// the part with security consequences -- is testable without framework plumbing.
func buildCustomersState(customers []client.Customer, includeContacts bool) []customerModel {
	// Always non-nil: a nil slice renders as null in state and would produce a
	// spurious diff against a config expecting an empty list.
	out := make([]customerModel, 0, len(customers))
	for _, c := range customers {
		out = append(out, buildCustomerModel(c, includeContacts))
	}
	return out
}

// buildCustomerModel maps one customer, applying the contact opt-in.
//
// Withheld fields are NULL rather than empty. Null says "not requested"; an
// empty string would assert that upstream holds nothing, which is a different
// and possibly false claim.
func buildCustomerModel(c client.Customer, includeContacts bool) customerModel {
	m := customerModel{
		ID:        types.Int64Value(c.ID),
		Number:    types.StringValue(c.Number),
		FirstName: types.StringValue(c.FirstName),
		LastName:  types.StringValue(c.LastName),
		Company:   types.StringValue(c.Company),
		CVR:       types.StringValue(c.CVR),
		CaseCount: types.Int64Value(int64(c.CaseCount)),

		Email:   types.StringNull(),
		Phone:   types.StringNull(),
		Address: types.StringNull(),
		Zip:     types.StringNull(),
		City:    types.StringNull(),
		EAN:     types.StringNull(),
	}
	if includeContacts {
		m.Email = types.StringValue(c.Email)
		m.Phone = types.StringValue(c.Phone)
		m.Address = types.StringValue(c.Address)
		m.Zip = types.StringValue(c.Zip)
		m.City = types.StringValue(c.City)
		m.EAN = types.StringValue(c.EAN)
	}
	return m
}

// selectCustomer finds one customer by id within a scan.
//
// Returns found=false only when the scan was COMPLETE. An id missing from a
// partial read is reported through the third return value so the caller can say
// "the read did not cover the account" instead of "no such customer".
func selectCustomer(scan client.CustomerScan, id int64, includeContacts bool) (customerModel, bool, bool) {
	matches, partial := matchCustomers(scan, func(c client.Customer) bool { return c.ID == id }, includeContacts)
	if len(matches) == 0 {
		return customerModel{}, false, partial
	}
	return matches[0], true, false
}

// matchCustomers returns every customer satisfying match, and whether a lack of
// matches is inconclusive because the read did not cover the account.
//
// The partial flag is the important half. Kala's query parameter narrows a read
// but a capped page still proves nothing about absence, so "no match" and "no
// match that we saw" must stay distinguishable.
func matchCustomers(scan client.CustomerScan, match func(client.Customer) bool, includeContacts bool) ([]customerModel, bool) {
	var out []customerModel
	for _, c := range scan.Customers {
		if match(c) {
			out = append(out, buildCustomerModel(c, includeContacts))
		}
	}
	return out, len(out) == 0 && !scan.Complete()
}

// customerSelector describes which attribute a lookup was addressed by.
type customerSelector struct {
	attr  string
	value string
	match func(client.Customer) bool

	// prefilter is the value sent as Kala's `query` parameter to narrow the
	// read server-side. Empty for id: query searches TEXT, so an id sent
	// through it returns nothing and the lookup would report an existing
	// customer as missing.
	prefilter string
}

// resolveCustomerSelector requires exactly one selector to be set.
func resolveCustomerSelector(cfg customerDataSourceModel) (customerSelector, error) {
	var set []customerSelector

	if !cfg.ID.IsNull() && !cfg.ID.IsUnknown() {
		id := cfg.ID.ValueInt64()
		set = append(set, customerSelector{
			attr: "id", value: fmt.Sprintf("%d", id),
			match: func(c client.Customer) bool { return c.ID == id },
		})
	}
	if !cfg.Number.IsNull() && !cfg.Number.IsUnknown() {
		v := cfg.Number.ValueString()
		set = append(set, customerSelector{
			attr: "number", value: v, prefilter: v,
			match: func(c client.Customer) bool { return c.Number == v },
		})
	}
	if !cfg.CVR.IsNull() && !cfg.CVR.IsUnknown() {
		v := cfg.CVR.ValueString()
		set = append(set, customerSelector{
			attr: "cvr", value: v, prefilter: v,
			match: func(c client.Customer) bool { return c.CVR == v },
		})
	}

	switch len(set) {
	case 1:
		return set[0], nil
	case 0:
		//nolint:staticcheck // ST1005: rendered verbatim as diagnostic prose, where a
		// terminal period is correct -- this is not appended to another sentence.
		return customerSelector{}, errors.New(
			"Set exactly one of `id`, `number`, or `cvr` to identify the customer.")
	default:
		names := make([]string, 0, len(set))
		for _, s := range set {
			names = append(names, "`"+s.attr+"`")
		}
		//nolint:staticcheck // ST1005: rendered verbatim as diagnostic prose.
		return customerSelector{}, fmt.Errorf(
			"Set exactly one of `id`, `number`, or `cvr`; %s were all set. "+
				"A data source must resolve to a single customer, and combining selectors "+
				"hides which one actually decided the result.", strings.Join(names, ", "))
	}
}
