package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"

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

func TestCustomerDataSource_SchemaRequiresID(t *testing.T) {
	var resp datasource.SchemaResponse
	NewCustomerDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	attr, ok := resp.Schema.Attributes["id"]
	if !ok {
		t.Fatal("schema is missing id")
	}
	if !attr.IsRequired() {
		t.Error("id must be Required -- it is the lookup key, not an optional filter")
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

// FR7: contact data is personal data, and everything a data source exposes is
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
