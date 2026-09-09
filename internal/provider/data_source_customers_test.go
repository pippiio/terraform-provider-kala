package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

func sampleCustomers() []client.Customer {
	return []client.Customer{
		{
			ID: 1, Number: "K-001", FirstName: "Frodo", LastName: "Baggins",
			Company: "Bag End Ltd", CVR: "12345678", CaseCount: 2,
			Email: "frodo@example.com", Phone: "+45 00 00 00 00",
			Address: "Bagshot Row 1", Zip: "1000", City: "Hobbiton", EAN: "5790000000000",
		},
		{
			ID: 2, Number: "K-002", FirstName: "Samwise", LastName: "Gamgee",
			Company: "Gamgee Gardening", CVR: "87654321", CaseCount: 0,
			Email: "sam@example.com", Phone: "+45 11 11 11 11",
			Address: "Bagshot Row 3", Zip: "1000", City: "Hobbiton", EAN: "",
		},
	}
}

func TestCustomersDataSource_Metadata(t *testing.T) {
	for _, tc := range []struct {
		ds   datasource.DataSource
		want string
	}{
		{NewCustomersDataSource(), "kala_customers"},
		{NewCustomerDataSource(), "kala_customer"},
	} {
		var resp datasource.MetadataResponse
		tc.ds.Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "kala"}, &resp)
		if resp.TypeName != tc.want {
			t.Errorf("TypeName = %q, want %q", resp.TypeName, tc.want)
		}
	}
}

func TestCustomersDataSource_SchemaExposesCompleteness(t *testing.T) {
	var resp datasource.SchemaResponse
	NewCustomersDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	for _, name := range []string{"complete", "total", "include_contact_details", "customers"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("schema is missing %q", name)
		}
	}
}

func TestBuildCustomersState_MapsNonContactFields(t *testing.T) {
	got := buildCustomersState(sampleCustomers(), false)
	if len(got) != 2 {
		t.Fatalf("got %d customers, want 2", len(got))
	}
	c := got[0]
	if c.ID.ValueInt64() != 1 {
		t.Errorf("ID = %d, want 1", c.ID.ValueInt64())
	}
	if c.Number.ValueString() != "K-001" {
		t.Errorf("Number = %q, want K-001 -- a string on this API", c.Number.ValueString())
	}
	if c.FirstName.ValueString() != "Frodo" || c.LastName.ValueString() != "Baggins" {
		t.Errorf("name = %q %q", c.FirstName.ValueString(), c.LastName.ValueString())
	}
	if c.Company.ValueString() != "Bag End Ltd" || c.CVR.ValueString() != "12345678" {
		t.Errorf("company/cvr = %q/%q", c.Company.ValueString(), c.CVR.ValueString())
	}
	if c.CaseCount.ValueInt64() != 2 {
		t.Errorf("CaseCount = %d, want 2", c.CaseCount.ValueInt64())
	}
}

// contact data is personal data, and everything a data source exposes is
// written to state. It must be absent unless explicitly asked for.
func TestBuildCustomersState_WithholdsContactDetailsByDefault(t *testing.T) {
	got := buildCustomersState(sampleCustomers(), false)
	for i, c := range got {
		for name, v := range map[string]string{
			"email": c.Email.ValueString(), "phone": c.Phone.ValueString(),
			"address": c.Address.ValueString(), "zip": c.Zip.ValueString(),
			"city": c.City.ValueString(), "ean": c.EAN.ValueString(),
		} {
			if v != "" {
				t.Errorf("customer %d: %s = %q, want empty without opt-in", i, name, v)
			}
		}
		if !c.Email.IsNull() {
			t.Errorf("customer %d: email must be NULL, not an empty string -- null says 'not requested'", i)
		}
	}
}

func TestBuildCustomersState_ExposesContactDetailsWhenRequested(t *testing.T) {
	got := buildCustomersState(sampleCustomers(), true)
	if len(got) != 2 {
		t.Fatalf("got %d customers, want 2", len(got))
	}
	c := got[0]
	if c.Email.ValueString() != "frodo@example.com" {
		t.Errorf("email = %q", c.Email.ValueString())
	}
	if c.Phone.ValueString() != "+45 00 00 00 00" {
		t.Errorf("phone = %q", c.Phone.ValueString())
	}
	if c.Address.ValueString() != "Bagshot Row 1" || c.Zip.ValueString() != "1000" {
		t.Errorf("address/zip = %q/%q", c.Address.ValueString(), c.Zip.ValueString())
	}
	if c.City.ValueString() != "Hobbiton" || c.EAN.ValueString() != "5790000000000" {
		t.Errorf("city/ean = %q/%q", c.City.ValueString(), c.EAN.ValueString())
	}
}

func TestBuildCustomersState_EmptyListIsAnEmptySliceNotNil(t *testing.T) {
	got := buildCustomersState(nil, false)
	if got == nil {
		t.Fatal("nil slice renders as null in state and produces a spurious diff against an empty list")
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}
}

func TestSelectCustomer_FindsByID(t *testing.T) {
	scan := client.CustomerScan{Customers: sampleCustomers(), Total: 2, Fetched: 2}
	got, found, partial := selectCustomer(scan, 2, false)
	if !found {
		t.Fatal("customer 2 is present and the scan is complete")
	}
	if partial {
		t.Error("a complete scan must not report itself partial")
	}
	if got.Number.ValueString() != "K-002" {
		t.Errorf("Number = %q, want K-002", got.Number.ValueString())
	}
}

func TestSelectCustomer_AbsentFromCompleteScanIsNotFound(t *testing.T) {
	scan := client.CustomerScan{Customers: sampleCustomers(), Total: 2, Fetched: 2}
	_, found, partial := selectCustomer(scan, 99, false)
	if found {
		t.Fatal("customer 99 is not in the sample")
	}
	if partial {
		t.Error("the scan covered the account; absence is a real not-found")
	}
}

// The important one. A partial read proves nothing about absence, so the caller
// must be able to say "the read did not cover the account" rather than
// confidently reporting a customer that exists as missing.
func TestSelectCustomer_AbsentFromPartialScanReportsPartialNotMissing(t *testing.T) {
	scan := client.CustomerScan{Customers: sampleCustomers(), Total: 500, Fetched: 2}
	_, found, partial := selectCustomer(scan, 99, false)
	if found {
		t.Fatal("customer 99 is not in the records that were read")
	}
	if !partial {
		t.Fatal("the read covered 2 of 500 records; absence from it must NOT be reported as not-found")
	}
}

func TestSelectCustomer_HonoursContactOptIn(t *testing.T) {
	scan := client.CustomerScan{Customers: sampleCustomers(), Total: 2, Fetched: 2}
	without, _, _ := selectCustomer(scan, 1, false)
	if !without.Email.IsNull() {
		t.Error("contact details leaked without opt-in")
	}
	with, _, _ := selectCustomer(scan, 1, true)
	if with.Email.ValueString() != "frodo@example.com" {
		t.Errorf("email = %q with opt-in", with.Email.ValueString())
	}
}

func TestCustomersDataSource_ConfigureRejectsWrongType(t *testing.T) {
	for _, ds := range []datasource.DataSourceWithConfigure{
		&customersDataSource{}, &customerDataSource{},
	} {
		var resp datasource.ConfigureResponse
		ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "nonsense"}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Error("wrong provider data type must produce a diagnostic")
		}
	}
}

func TestCustomersDataSource_ConfigureIgnoresNilProviderData(t *testing.T) {
	for _, ds := range []datasource.DataSourceWithConfigure{
		&customersDataSource{}, &customerDataSource{},
	} {
		var resp datasource.ConfigureResponse
		ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
		if resp.Diagnostics.HasError() {
			t.Error("nil provider data is the normal pre-configure call and must be ignored")
		}
	}
}

func TestProvider_RegistersCustomerDataSources(t *testing.T) {
	want := map[string]bool{"kala_customers": false, "kala_customer": false}
	for _, mk := range (&kalaProvider{}).DataSources(context.Background()) {
		var resp datasource.MetadataResponse
		mk().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "kala"}, &resp)
		if _, tracked := want[resp.TypeName]; tracked {
			want[resp.TypeName] = true
		}
	}
	for name, registered := range want {
		if !registered {
			t.Errorf("%s is not registered on the provider", name)
		}
	}
}

// The list data source must warn, not fail silently, when the read was capped.
func TestCustomersDataSource_ReadWithoutClientIsAClearError(t *testing.T) {
	var resp datasource.ReadResponse
	(&customersDataSource{}).Read(context.Background(), datasource.ReadRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("reading without a configured internal client must error")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "KALA_USERNAME") {
		t.Errorf("the diagnostic must name the missing credentials, got: %s",
			resp.Diagnostics.Errors()[0].Detail())
	}
}

func TestCustomerDataSource_ReadWithoutClientIsAClearError(t *testing.T) {
	var resp datasource.ReadResponse
	(&customerDataSource{}).Read(context.Background(), datasource.ReadRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("reading without a configured internal client must error")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "KALA_USERNAME") {
		t.Errorf("the diagnostic must name the missing credentials, got: %s",
			resp.Diagnostics.Errors()[0].Detail())
	}
}

// --- Read, exercised through the framework -------------------------------

// customerFake is an InternalClient double for the customer read path.
type customerFake struct {
	client.InternalClient
	scan client.CustomerScan
	err  error
	got  client.CustomerQuery
}

func (f *customerFake) ListCustomers(_ context.Context, q client.CustomerQuery) (client.CustomerScan, error) {
	f.got = q
	return f.scan, f.err
}

// dsConfig builds a Config carrying only the attributes a test sets; the rest
// are null, which is what Terraform supplies for unset optional attributes.
func dsConfig(t *testing.T, sch schema.Schema, vals map[string]tftypes.Value) tfsdk.Config {
	t.Helper()
	typ := sch.Type().TerraformType(context.Background())
	obj, ok := typ.(tftypes.Object)
	if !ok {
		t.Fatalf("schema type is %T, want tftypes.Object", typ)
	}
	full := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for name, at := range obj.AttributeTypes {
		if v, set := vals[name]; set {
			full[name] = v
			continue
		}
		full[name] = tftypes.NewValue(at, nil)
	}
	return tfsdk.Config{Schema: sch, Raw: tftypes.NewValue(obj, full)}
}

func customersSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp datasource.SchemaResponse
	NewCustomersDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

func customerSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp datasource.SchemaResponse
	NewCustomerDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

func readCustomers(t *testing.T, f *customerFake, vals map[string]tftypes.Value) *datasource.ReadResponse {
	t.Helper()
	sch := customersSchema(t)
	ds := &customersDataSource{client: f}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch}}
	ds.Read(context.Background(), datasource.ReadRequest{Config: dsConfig(t, sch, vals)}, resp)
	return resp
}

func readCustomer(t *testing.T, f *customerFake, vals map[string]tftypes.Value) *datasource.ReadResponse {
	t.Helper()
	sch := customerSchema(t)
	ds := &customerDataSource{client: f}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch}}
	ds.Read(context.Background(), datasource.ReadRequest{Config: dsConfig(t, sch, vals)}, resp)
	return resp
}

func TestCustomersRead_PopulatesStateAndForwardsSearch(t *testing.T) {
	f := &customerFake{scan: client.CustomerScan{
		Customers: sampleCustomers(), Total: 2, Fetched: 2, Pages: 1,
	}}
	resp := readCustomers(t, f, map[string]tftypes.Value{
		"search": tftypes.NewValue(tftypes.String, "baggins"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	if f.got.Search != "baggins" {
		t.Errorf("search forwarded as %q, want baggins", f.got.Search)
	}
	var state customersDataSourceModel
	resp.State.Get(context.Background(), &state)
	if len(state.Customers) != 2 {
		t.Fatalf("state holds %d customers, want 2", len(state.Customers))
	}
	if !state.Complete.ValueBool() {
		t.Error("a full read must set complete = true")
	}
	if state.Total.ValueInt64() != 2 {
		t.Errorf("total = %d, want 2", state.Total.ValueInt64())
	}
}

// A capped read must warn. Silence would let a caller treat a subset as whole.
func TestCustomersRead_IncompleteReadWarns(t *testing.T) {
	f := &customerFake{scan: client.CustomerScan{
		Customers: sampleCustomers(), Total: 500, Fetched: 2, Pages: 1,
	}}
	resp := readCustomers(t, f, nil)
	if resp.Diagnostics.HasError() {
		t.Fatalf("an incomplete read is a warning, not an error: %v", resp.Diagnostics.Errors())
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("a capped read must warn that the list is a subset")
	}
	var state customersDataSourceModel
	resp.State.Get(context.Background(), &state)
	if state.Complete.ValueBool() {
		t.Error("complete must be false when the cap was reached")
	}
}

func TestCustomersRead_SurfacesClientError(t *testing.T) {
	f := &customerFake{err: errors.New("upstream exploded")}
	resp := readCustomers(t, f, nil)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a client error must surface as a diagnostic")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "upstream exploded") {
		t.Errorf("the upstream message must be preserved, got: %s", resp.Diagnostics.Errors()[0].Detail())
	}
}

func TestCustomerRead_FindsAndPopulates(t *testing.T) {
	f := &customerFake{scan: client.CustomerScan{Customers: sampleCustomers(), Total: 2, Fetched: 2}}
	resp := readCustomer(t, f, map[string]tftypes.Value{
		"id": tftypes.NewValue(tftypes.Number, 2),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	var state customerDataSourceModel
	resp.State.Get(context.Background(), &state)
	if state.Number.ValueString() != "K-002" {
		t.Errorf("number = %q, want K-002", state.Number.ValueString())
	}
	if !state.Email.IsNull() {
		t.Error("contact details must be null without opt-in")
	}
}

func TestCustomerRead_NotFoundInACompleteReadSaysNotFound(t *testing.T) {
	f := &customerFake{scan: client.CustomerScan{Customers: sampleCustomers(), Total: 2, Fetched: 2}}
	resp := readCustomer(t, f, map[string]tftypes.Value{
		"id": tftypes.NewValue(tftypes.Number, 99),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("a missing customer must error")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "not found") {
		t.Errorf("summary = %q, want a not-found", resp.Diagnostics.Errors()[0].Summary())
	}
}

// The one that matters: a partial read must NOT claim the customer is missing.
func TestCustomerRead_NotFoundInAPartialReadSaysIncompleteNotMissing(t *testing.T) {
	f := &customerFake{scan: client.CustomerScan{Customers: sampleCustomers(), Total: 500, Fetched: 2}}
	resp := readCustomer(t, f, map[string]tftypes.Value{
		"id": tftypes.NewValue(tftypes.Number, 99),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("an inconclusive lookup must error rather than return empty state")
	}
	d := resp.Diagnostics.Errors()[0]
	if strings.Contains(strings.ToLower(d.Summary()), "not found") {
		t.Errorf("a partial read must not report not-found; got summary %q", d.Summary())
	}
	if !strings.Contains(d.Detail(), "500") || !strings.Contains(d.Detail(), "cap") {
		t.Errorf("the diagnostic must explain the read was capped, got: %s", d.Detail())
	}
}

func TestCustomerRead_SurfacesClientError(t *testing.T) {
	f := &customerFake{err: errors.New("upstream exploded")}
	resp := readCustomer(t, f, map[string]tftypes.Value{
		"id": tftypes.NewValue(tftypes.Number, 1),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("a client error must surface as a diagnostic")
	}
}

// --- selectors (id / number / cvr) ---------------------------------------

func TestCustomerDataSource_SelectorsAreOptionalNotRequired(t *testing.T) {
	sch := customerSchema(t)
	for _, name := range []string{"id", "number", "cvr"} {
		attr, ok := sch.Attributes[name]
		if !ok {
			t.Fatalf("schema is missing selector %q", name)
		}
		if attr.IsRequired() {
			t.Errorf("%s must be Optional -- exactly one selector is required, not this one specifically", name)
		}
		if !attr.IsOptional() {
			t.Errorf("%s must be Optional", name)
		}
	}
}

func TestCustomerRead_NoSelectorIsAnError(t *testing.T) {
	f := &customerFake{scan: client.CustomerScan{Customers: sampleCustomers(), Total: 2, Fetched: 2}}
	resp := readCustomer(t, f, nil)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a lookup with no selector must error")
	}
	d := resp.Diagnostics.Errors()[0].Detail()
	for _, want := range []string{"id", "number", "cvr"} {
		if !strings.Contains(d, want) {
			t.Errorf("the diagnostic must list the available selectors; %q missing from: %s", want, d)
		}
	}
}

func TestCustomerRead_TwoSelectorsIsAnError(t *testing.T) {
	f := &customerFake{scan: client.CustomerScan{Customers: sampleCustomers(), Total: 2, Fetched: 2}}
	resp := readCustomer(t, f, map[string]tftypes.Value{
		"id":  tftypes.NewValue(tftypes.Number, 1),
		"cvr": tftypes.NewValue(tftypes.String, "12345678"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("more than one selector must error rather than silently preferring one")
	}
}

// A cvr lookup prefilters SERVER-SIDE via the query parameter, so it does not
// have to read the whole account the way an id lookup does.
func TestCustomerRead_CVRPrefiltersUpstream(t *testing.T) {
	f := &customerFake{scan: client.CustomerScan{
		Customers: sampleCustomers()[:1], Total: 1, Fetched: 1,
	}}
	resp := readCustomer(t, f, map[string]tftypes.Value{
		"cvr": tftypes.NewValue(tftypes.String, "12345678"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	if f.got.Search != "12345678" {
		t.Errorf("upstream query = %q, want the cvr so the set is narrowed server-side", f.got.Search)
	}
	var state customerDataSourceModel
	resp.State.Get(context.Background(), &state)
	if state.ID.ValueInt64() != 1 {
		t.Errorf("resolved id = %d, want 1", state.ID.ValueInt64())
	}
}

func TestCustomerRead_NumberSelectorPrefiltersUpstream(t *testing.T) {
	f := &customerFake{scan: client.CustomerScan{Customers: sampleCustomers(), Total: 2, Fetched: 2}}
	resp := readCustomer(t, f, map[string]tftypes.Value{
		"number": tftypes.NewValue(tftypes.String, "K-002"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	if f.got.Search != "K-002" {
		t.Errorf("upstream query = %q, want K-002", f.got.Search)
	}
	var state customerDataSourceModel
	resp.State.Get(context.Background(), &state)
	if state.ID.ValueInt64() != 2 {
		t.Errorf("resolved id = %d, want 2", state.ID.ValueInt64())
	}
}

// An id cannot be prefiltered: query searches text, so passing an id through it
// would return nothing and the lookup would report an existing customer as
// missing. The id path must scan instead.
func TestCustomerRead_IDSelectorDoesNotPrefilterUpstream(t *testing.T) {
	f := &customerFake{scan: client.CustomerScan{Customers: sampleCustomers(), Total: 2, Fetched: 2}}
	resp := readCustomer(t, f, map[string]tftypes.Value{
		"id": tftypes.NewValue(tftypes.Number, 2),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	if f.got.Search != "" {
		t.Errorf("query = %q; an id must not be sent as a text search", f.got.Search)
	}
}

// The query parameter narrows but does not exact-match: searching a cvr can
// return neighbours. The result must still be filtered client-side.
func TestCustomerRead_PrefilterIsNarrowedByAnExactClientSideMatch(t *testing.T) {
	loose := sampleCustomers() // both returned by a loose upstream query
	f := &customerFake{scan: client.CustomerScan{Customers: loose, Total: 2, Fetched: 2}}
	resp := readCustomer(t, f, map[string]tftypes.Value{
		"cvr": tftypes.NewValue(tftypes.String, "87654321"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	var state customerDataSourceModel
	resp.State.Get(context.Background(), &state)
	if state.ID.ValueInt64() != 2 {
		t.Errorf("resolved id = %d, want 2 -- upstream returned both, the exact cvr match is customer 2",
			state.ID.ValueInt64())
	}
}

// A data source must resolve to exactly one record. Two matches is an error,
// not a silent first-wins.
func TestCustomerRead_AmbiguousMatchIsAnError(t *testing.T) {
	dupes := sampleCustomers()
	dupes[1].CVR = dupes[0].CVR // two customers sharing a cvr
	f := &customerFake{scan: client.CustomerScan{Customers: dupes, Total: 2, Fetched: 2}}
	resp := readCustomer(t, f, map[string]tftypes.Value{
		"cvr": tftypes.NewValue(tftypes.String, "12345678"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("two matches must error rather than silently returning the first")
	}
	d := resp.Diagnostics.Errors()[0]
	if !strings.Contains(d.Detail(), "2") {
		t.Errorf("the diagnostic should say how many matched, got: %s", d.Detail())
	}
}

func TestCustomerRead_CVRNotFoundInACompleteReadSaysNotFound(t *testing.T) {
	f := &customerFake{scan: client.CustomerScan{Customers: sampleCustomers(), Total: 2, Fetched: 2}}
	resp := readCustomer(t, f, map[string]tftypes.Value{
		"cvr": tftypes.NewValue(tftypes.String, "00000000"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("no match must error")
	}
	if !strings.Contains(strings.ToLower(resp.Diagnostics.Errors()[0].Summary()), "not found") {
		t.Errorf("summary = %q", resp.Diagnostics.Errors()[0].Summary())
	}
}

// The partial-read guard applies to every selector, not just id.
func TestCustomerRead_CVRNotFoundInAPartialReadSaysIncomplete(t *testing.T) {
	f := &customerFake{scan: client.CustomerScan{Customers: sampleCustomers(), Total: 500, Fetched: 2}}
	resp := readCustomer(t, f, map[string]tftypes.Value{
		"cvr": tftypes.NewValue(tftypes.String, "00000000"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("an inconclusive lookup must error")
	}
	if strings.Contains(strings.ToLower(resp.Diagnostics.Errors()[0].Summary()), "not found") {
		t.Error("a partial read must not report not-found for a cvr selector either")
	}
}
