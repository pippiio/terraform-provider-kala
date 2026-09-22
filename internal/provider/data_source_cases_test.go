package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/pippiio/terraform-provider-kala/internal/client"
)

type caseFake struct {
	client.InternalClient
	scan     client.CaseScan
	scanErr  error
	detail   client.CaseDetail
	getErr   error
	gotQuery client.CaseQuery
	gotNr    string
}

func (f *caseFake) ListCases(_ context.Context, q client.CaseQuery) (client.CaseScan, error) {
	f.gotQuery = q
	return f.scan, f.scanErr
}

func (f *caseFake) GetCase(_ context.Context, nr string) (client.CaseDetail, error) {
	f.gotNr = nr
	return f.detail, f.getErr
}

func sampleCases(archived bool) []client.Case {
	return []client.Case{{
		ID: 1, Number: "KA-1", Name: "Roof works", Archived: archived,
		EconomyCaseNumber: "KA-1", Address: "Bagshot Row 1", Zip: "1000", SubText: "",
		CustomerName: "Frodo Baggins", CustomerCompany: "Bag End Ltd",
		CustomerEmail: "frodo@example.com", CustomerPhone: "+45 00 00 00 00",
		InternalProject: false, Restricted: false, Favorite: true,
	}}
}

func casesSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp datasource.SchemaResponse
	NewCasesDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

func caseSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp datasource.SchemaResponse
	NewCaseDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

func readCases(t *testing.T, f *caseFake, vals map[string]tftypes.Value) *datasource.ReadResponse {
	t.Helper()
	sch := casesSchema(t)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch}}
	(&casesDataSource{client: f}).Read(context.Background(),
		datasource.ReadRequest{Config: dsConfig(t, sch, vals)}, resp)
	return resp
}

func readCase(t *testing.T, f *caseFake, vals map[string]tftypes.Value) *datasource.ReadResponse {
	t.Helper()
	sch := caseSchema(t)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch}}
	(&caseDataSource{client: f}).Read(context.Background(),
		datasource.ReadRequest{Config: dsConfig(t, sch, vals)}, resp)
	return resp
}

func TestCasesDataSource_Metadata(t *testing.T) {
	for _, tc := range []struct {
		ds   datasource.DataSource
		want string
	}{{NewCasesDataSource(), "kala_cases"}, {NewCaseDataSource(), "kala_case"}} {
		var resp datasource.MetadataResponse
		tc.ds.Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "kala"}, &resp)
		if resp.TypeName != tc.want {
			t.Errorf("TypeName = %q, want %q", resp.TypeName, tc.want)
		}
	}
}

// The active/archived sets are disjoint upstream. A caller who reads `active`
// as a narrowing filter will silently miss every archived case, so the schema
// has to say so.
func TestCasesDataSource_ActiveSchemaDocumentsTheDisjointSets(t *testing.T) {
	attr, ok := casesSchema(t).Attributes["active"]
	if !ok {
		t.Fatal("schema is missing active")
	}
	desc := strings.ToLower(attr.GetMarkdownDescription())
	for _, want := range []string{"disjoint", "no single call", "only archived"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the active description must mention %q; got: %s", want, desc)
		}
	}
}

func TestCaseDataSource_SchemaRequiresCaseNumber(t *testing.T) {
	attr, ok := caseSchema(t).Attributes["case_number"]
	if !ok {
		t.Fatal("schema is missing case_number")
	}
	if !attr.IsRequired() {
		t.Error("case_number must be Required")
	}
}

func TestBuildCasesState_MapsFieldsAndWithholdsContacts(t *testing.T) {
	got := buildCasesState(sampleCases(false), false)
	if len(got) != 1 {
		t.Fatalf("got %d cases, want 1", len(got))
	}
	c := got[0]
	if c.ID.ValueInt64() != 1 || c.Number.ValueString() != "KA-1" || c.Name.ValueString() != "Roof works" {
		t.Errorf("identity = %d/%q/%q", c.ID.ValueInt64(), c.Number.ValueString(), c.Name.ValueString())
	}
	if c.CustomerName.ValueString() != "Frodo Baggins" {
		t.Errorf("customer_name = %q", c.CustomerName.ValueString())
	}
	if !c.CustomerEmail.IsNull() || !c.CustomerPhone.IsNull() {
		t.Error("customer contact details must be null without opt-in")
	}
	if !c.Favorite.ValueBool() {
		t.Error("favorite = false, want true")
	}
}

func TestBuildCasesState_ExposesContactsWhenRequested(t *testing.T) {
	got := buildCasesState(sampleCases(false), true)
	if len(got) != 1 {
		t.Fatalf("got %d cases, want 1", len(got))
	}
	if got[0].CustomerEmail.ValueString() != "frodo@example.com" {
		t.Errorf("customer_email = %q", got[0].CustomerEmail.ValueString())
	}
}

func TestBuildCasesState_EmptyIsAnEmptySliceNotNil(t *testing.T) {
	if got := buildCasesState(nil, false); got == nil {
		t.Fatal("nil renders as null in state and produces a spurious diff")
	}
}

func TestRFC3339OrNull(t *testing.T) {
	if got := rfc3339OrNull(nil); !got.IsNull() {
		t.Errorf("nil must stay null, got %q", got.ValueString())
	}
	ts := time.Date(2026, 9, 2, 7, 19, 3, 0, time.UTC)
	if got := rfc3339OrNull(&ts); got.ValueString() != "2026-09-02T07:19:03Z" {
		t.Errorf("got %q, want 2026-09-02T07:19:03Z", got.ValueString())
	}
}

// Unset `active` means active-only, matching kala_employees.
func TestCasesRead_UnsetActiveRequestsTheNonArchivedSet(t *testing.T) {
	f := &caseFake{scan: client.CaseScan{Cases: sampleCases(false), Total: 1, Fetched: 1}}
	resp := readCases(t, f, nil)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	if f.gotQuery.Archived {
		t.Error("unset active must request the NON-archived set")
	}
}

func TestCasesRead_ActiveFalseRequestsTheArchivedSet(t *testing.T) {
	f := &caseFake{scan: client.CaseScan{Cases: sampleCases(true), Total: 1, Fetched: 1}}
	resp := readCases(t, f, map[string]tftypes.Value{
		"active": tftypes.NewValue(tftypes.Bool, false),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	if !f.gotQuery.Archived {
		t.Error("active = false must request the ARCHIVED set, not merely exclude active ones")
	}
	var state casesDataSourceModel
	resp.State.Get(context.Background(), &state)
	if len(state.Cases) != 1 || !state.Cases[0].Archived.ValueBool() {
		t.Error("cases from the archived query must be marked archived")
	}
}

func TestCasesRead_IncompleteReadWarns(t *testing.T) {
	f := &caseFake{scan: client.CaseScan{Cases: sampleCases(false), Total: 900, Fetched: 1}}
	resp := readCases(t, f, nil)
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("a capped read must warn")
	}
	var state casesDataSourceModel
	resp.State.Get(context.Background(), &state)
	if state.Complete.ValueBool() {
		t.Error("complete must be false")
	}
}

func TestCasesRead_SurfacesClientError(t *testing.T) {
	f := &caseFake{scanErr: errors.New("upstream exploded")}
	if resp := readCases(t, f, nil); !resp.Diagnostics.HasError() {
		t.Fatal("a client error must surface")
	}
}

func caseDetailFixture() client.CaseDetail {
	start := time.Date(2026, 9, 2, 7, 19, 3, 0, time.UTC)
	return client.CaseDetail{
		Case:                    sampleCases(false)[0],
		CustomerID:              7,
		IsFinished:              false,
		ChecklistItemsTotal:     3,
		ChecklistItemsCompleted: 1,
		EconomySyncFailed:       false,
		StartDate:               &start,
		Cost:                    1200, Sales: 4000, Result: 2800,
		Invoiced: 1500, Uninvoiced: 2500, Realised: 900,
		RegisteredHoursTotal: 37, BilledHours: 30,
	}
}

func TestCaseRead_PopulatesDetailFields(t *testing.T) {
	f := &caseFake{detail: caseDetailFixture()}
	resp := readCase(t, f, map[string]tftypes.Value{
		"case_number": tftypes.NewValue(tftypes.String, "KA-1"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	if f.gotNr != "KA-1" {
		t.Errorf("looked up %q, want KA-1", f.gotNr)
	}
	var state caseDataSourceModel
	resp.State.Get(context.Background(), &state)
	if state.CustomerID.ValueInt64() != 7 {
		t.Errorf("customer_id = %d, want 7", state.CustomerID.ValueInt64())
	}
	if state.ChecklistItemsTotal.ValueInt64() != 3 || state.ChecklistItemsCompleted.ValueInt64() != 1 {
		t.Errorf("checklist = %d/%d", state.ChecklistItemsTotal.ValueInt64(), state.ChecklistItemsCompleted.ValueInt64())
	}
	if state.StartDate.ValueString() != "2026-09-02T07:19:03Z" {
		t.Errorf("start_date = %q, want RFC3339", state.StartDate.ValueString())
	}
	if !state.EndDate.IsNull() {
		t.Error("an unset date must stay null")
	}
}

// financial data is commercially sensitive and transactional.
func TestCaseRead_WithholdsFinancialsByDefault(t *testing.T) {
	f := &caseFake{detail: caseDetailFixture()}
	resp := readCase(t, f, map[string]tftypes.Value{
		"case_number": tftypes.NewValue(tftypes.String, "KA-1"),
	})
	var state caseDataSourceModel
	resp.State.Get(context.Background(), &state)
	for name, v := range map[string]interface{ IsNull() bool }{
		"cost": state.Cost, "sales": state.Sales, "result": state.Result,
		"invoiced": state.Invoiced, "uninvoiced": state.Uninvoiced, "realised": state.Realised,
		"registered_hours_total": state.RegisteredHoursTotal, "billed_hours": state.BilledHours,
	} {
		if !v.IsNull() {
			t.Errorf("%s must be null without include_financials", name)
		}
	}
}

func TestCaseRead_ExposesFinancialsWhenRequested(t *testing.T) {
	f := &caseFake{detail: caseDetailFixture()}
	resp := readCase(t, f, map[string]tftypes.Value{
		"case_number":        tftypes.NewValue(tftypes.String, "KA-1"),
		"include_financials": tftypes.NewValue(tftypes.Bool, true),
	})
	var state caseDataSourceModel
	resp.State.Get(context.Background(), &state)
	if state.Sales.ValueInt64() != 4000 || state.Result.ValueInt64() != 2800 {
		t.Errorf("sales/result = %d/%d, want 4000/2800", state.Sales.ValueInt64(), state.Result.ValueInt64())
	}
	if state.RegisteredHoursTotal.ValueInt64() != 37 {
		t.Errorf("registered_hours_total = %d, want 37", state.RegisteredHoursTotal.ValueInt64())
	}
}

// An unknown case number 500s upstream; the client maps that to ErrNotFound and
// the diagnostic must be actionable rather than relaying a raw server error.
func TestCaseRead_NotFoundIsAClearDiagnostic(t *testing.T) {
	f := &caseFake{getErr: client.ErrNotFound}
	resp := readCase(t, f, map[string]tftypes.Value{
		"case_number": tftypes.NewValue(tftypes.String, "ZZ-9"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("a missing case must error")
	}
	d := resp.Diagnostics.Errors()[0]
	if !strings.Contains(strings.ToLower(d.Summary()), "not found") {
		t.Errorf("summary = %q, want a not-found", d.Summary())
	}
	if !strings.Contains(d.Detail(), "ZZ-9") {
		t.Errorf("the diagnostic must name the case number, got: %s", d.Detail())
	}
}

func TestCaseRead_OtherErrorsAreNotReportedAsNotFound(t *testing.T) {
	f := &caseFake{getErr: errors.New("upstream exploded")}
	resp := readCase(t, f, map[string]tftypes.Value{
		"case_number": tftypes.NewValue(tftypes.String, "KA-1"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	if strings.Contains(strings.ToLower(resp.Diagnostics.Errors()[0].Summary()), "not found") {
		t.Error("a generic failure must not be reported as a missing case")
	}
}

func TestProvider_RegistersCaseDataSources(t *testing.T) {
	want := map[string]bool{"kala_cases": false, "kala_case": false}
	for _, mk := range (&kalaProvider{}).DataSources(context.Background()) {
		var resp datasource.MetadataResponse
		mk().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "kala"}, &resp)
		if _, tracked := want[resp.TypeName]; tracked {
			want[resp.TypeName] = true
		}
	}
	for name, ok := range want {
		if !ok {
			t.Errorf("%s is not registered on the provider", name)
		}
	}
}

func TestCasesDataSource_Configure(t *testing.T) {
	for _, ds := range []datasource.DataSourceWithConfigure{&casesDataSource{}, &caseDataSource{}} {
		var nilResp datasource.ConfigureResponse
		ds.Configure(context.Background(), datasource.ConfigureRequest{}, &nilResp)
		if nilResp.Diagnostics.HasError() {
			t.Error("nil provider data is the normal pre-configure call and must be ignored")
		}

		var badResp datasource.ConfigureResponse
		ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "nonsense"}, &badResp)
		if !badResp.Diagnostics.HasError() {
			t.Error("wrong provider data type must produce a diagnostic")
		}

		var okResp datasource.ConfigureResponse
		ds.Configure(context.Background(),
			datasource.ConfigureRequest{ProviderData: &providerClients{Internal: &caseFake{}}}, &okResp)
		if okResp.Diagnostics.HasError() {
			t.Errorf("valid provider data must configure cleanly: %v", okResp.Diagnostics.Errors())
		}
	}
}

func TestCasesDataSource_ReadWithoutClientNamesTheCredentials(t *testing.T) {
	for name, read := range map[string]func() *datasource.ReadResponse{
		"kala_cases": func() *datasource.ReadResponse {
			r := &datasource.ReadResponse{}
			(&casesDataSource{}).Read(context.Background(), datasource.ReadRequest{}, r)
			return r
		},
		"kala_case": func() *datasource.ReadResponse {
			r := &datasource.ReadResponse{}
			(&caseDataSource{}).Read(context.Background(), datasource.ReadRequest{}, r)
			return r
		},
	} {
		resp := read()
		if !resp.Diagnostics.HasError() {
			t.Fatalf("%s: reading without a configured client must error", name)
		}
		if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "KALA_USERNAME") {
			t.Errorf("%s: the diagnostic must name the missing credentials", name)
		}
	}
}

func TestCasesRead_ActiveTrueRequestsTheNonArchivedSet(t *testing.T) {
	f := &caseFake{scan: client.CaseScan{Cases: sampleCases(false), Total: 1, Fetched: 1}}
	readCases(t, f, map[string]tftypes.Value{"active": tftypes.NewValue(tftypes.Bool, true)})
	if f.gotQuery.Archived {
		t.Error("active = true must request the non-archived set")
	}
}

func TestCasesRead_ForwardsSearchAndPageSize(t *testing.T) {
	f := &caseFake{scan: client.CaseScan{Cases: sampleCases(false), Total: 1, Fetched: 1}}
	readCases(t, f, map[string]tftypes.Value{
		"search":    tftypes.NewValue(tftypes.String, "roof"),
		"page_size": tftypes.NewValue(tftypes.Number, 25),
	})
	if f.gotQuery.Search != "roof" {
		t.Errorf("search = %q, want roof", f.gotQuery.Search)
	}
	if f.gotQuery.PageSize != 25 {
		t.Errorf("page_size = %d, want 25", f.gotQuery.PageSize)
	}
}

func TestCaseRead_ExposesContactsWhenRequested(t *testing.T) {
	f := &caseFake{detail: caseDetailFixture()}
	resp := readCase(t, f, map[string]tftypes.Value{
		"case_number":             tftypes.NewValue(tftypes.String, "KA-1"),
		"include_contact_details": tftypes.NewValue(tftypes.Bool, true),
	})
	var state caseDataSourceModel
	resp.State.Get(context.Background(), &state)
	if state.CustomerEmail.ValueString() != "frodo@example.com" {
		t.Errorf("customer_email = %q", state.CustomerEmail.ValueString())
	}
}

// --- customer_company filter ---------------------------------------------

func casesWithCustomers() []client.Case {
	return []client.Case{
		{ID: 1, Number: "KA-1", Name: "Roof works", CustomerCompany: "Bag End Ltd", CustomerName: "Frodo Baggins"},
		{ID: 2, Number: "KA-2", Name: "Internal", CustomerCompany: "", CustomerName: ""},
		{ID: 3, Number: "KA-3", Name: "Fence", CustomerCompany: "Gamgee Gardening", CustomerName: "Samwise Gamgee"},
		{ID: 4, Number: "KA-4", Name: "Gutter", CustomerCompany: "Bag End Ltd", CustomerName: "Frodo Baggins"},
	}
}

func TestCasesRead_CustomerCompanyFiltersClientSide(t *testing.T) {
	f := &caseFake{scan: client.CaseScan{Cases: casesWithCustomers(), Total: 4, Fetched: 4}}
	resp := readCases(t, f, map[string]tftypes.Value{
		"customer_company": tftypes.NewValue(tftypes.String, "Bag End Ltd"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	var state casesDataSourceModel
	resp.State.Get(context.Background(), &state)
	if len(state.Cases) != 2 {
		t.Fatalf("got %d cases, want 2", len(state.Cases))
	}
	for _, c := range state.Cases {
		if c.CustomerCompany.ValueString() != "Bag End Ltd" {
			t.Errorf("case %s leaked through the filter", c.Number.ValueString())
		}
	}
}

// Matching is case-insensitive: `search` upstream is, and a user copying a
// company name out of kala_customers should not have to match capitalisation.
func TestCasesRead_CustomerCompanyIsCaseInsensitive(t *testing.T) {
	f := &caseFake{scan: client.CaseScan{Cases: casesWithCustomers(), Total: 4, Fetched: 4}}
	resp := readCases(t, f, map[string]tftypes.Value{
		"customer_company": tftypes.NewValue(tftypes.String, "bag end ltd"),
	})
	var state casesDataSourceModel
	resp.State.Get(context.Background(), &state)
	if len(state.Cases) != 2 {
		t.Fatalf("got %d cases, want 2 -- matching must be case-insensitive", len(state.Cases))
	}
}

// Exact match, not substring: "Bag End" must not match "Bag End Ltd", or a
// filter would silently widen as customers are added.
func TestCasesRead_CustomerCompanyIsExactNotSubstring(t *testing.T) {
	f := &caseFake{scan: client.CaseScan{Cases: casesWithCustomers(), Total: 4, Fetched: 4}}
	resp := readCases(t, f, map[string]tftypes.Value{
		"customer_company": tftypes.NewValue(tftypes.String, "Bag End"),
	})
	var state casesDataSourceModel
	resp.State.Get(context.Background(), &state)
	if len(state.Cases) != 0 {
		t.Errorf("got %d cases, want 0 -- the filter is an exact match", len(state.Cases))
	}
}

// The same invariant the task assignee filter has: a client-side filter narrows
// the RESULT, not the READ.
func TestCasesRead_CustomerCompanyDoesNotMakeCompleteFalse(t *testing.T) {
	f := &caseFake{scan: client.CaseScan{Cases: casesWithCustomers(), Total: 4, Fetched: 4}}
	resp := readCases(t, f, map[string]tftypes.Value{
		"customer_company": tftypes.NewValue(tftypes.String, "Bag End Ltd"),
	})
	var state casesDataSourceModel
	resp.State.Get(context.Background(), &state)
	if !state.Complete.ValueBool() {
		t.Error("all 4 of 4 records were received; filtering must not report the read as partial")
	}
	if state.Total.ValueInt64() != 4 {
		t.Errorf("total = %d, want 4 -- total describes the account, not the filtered list",
			state.Total.ValueInt64())
	}
}

// customer_company must NOT be sent upstream. `search` is a broad text match
// across case name and customer fields whose coverage is unverified, so using
// it to prefilter could silently drop cases that genuinely match.
func TestCasesRead_CustomerCompanyIsNotSentUpstream(t *testing.T) {
	f := &caseFake{scan: client.CaseScan{Cases: casesWithCustomers(), Total: 4, Fetched: 4}}
	readCases(t, f, map[string]tftypes.Value{
		"customer_company": tftypes.NewValue(tftypes.String, "Bag End Ltd"),
	})
	if f.gotQuery.Search != "" {
		t.Errorf("search = %q; customer_company must not be pushed into the upstream text search",
			f.gotQuery.Search)
	}
}

func TestCasesRead_CustomerCompanyMatchesEmptyString(t *testing.T) {
	f := &caseFake{scan: client.CaseScan{Cases: casesWithCustomers(), Total: 4, Fetched: 4}}
	resp := readCases(t, f, map[string]tftypes.Value{
		"customer_company": tftypes.NewValue(tftypes.String, ""),
	})
	var state casesDataSourceModel
	resp.State.Get(context.Background(), &state)
	if len(state.Cases) != 1 || state.Cases[0].Number.ValueString() != "KA-2" {
		t.Errorf("an explicit empty string must select the internal project with no customer, got %d", len(state.Cases))
	}
}
