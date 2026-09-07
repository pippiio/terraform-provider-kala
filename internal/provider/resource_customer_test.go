package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	fwschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

func newCustomerResource(fi *fakeInternal) *customerResource {
	return &customerResource{clients: &providerClients{Web: &fakeClient{}, Internal: fi}}
}

func customerResSchema(t *testing.T) fwschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewCustomerResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func customerResValue(t *testing.T, m customerResourceModel) tftypes.Value {
	t.Helper()
	typ := customerResSchema(t).Type().TerraformType(context.Background())
	str := func(v types.String) tftypes.Value {
		switch {
		case v.IsUnknown():
			return tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
		case v.IsNull():
			return tftypes.NewValue(tftypes.String, nil)
		}
		return tftypes.NewValue(tftypes.String, v.ValueString())
	}
	i64 := func(v types.Int64) tftypes.Value {
		switch {
		case v.IsUnknown():
			return tftypes.NewValue(tftypes.Number, tftypes.UnknownValue)
		case v.IsNull():
			return tftypes.NewValue(tftypes.Number, nil)
		}
		return tftypes.NewValue(tftypes.Number, v.ValueInt64())
	}
	return tftypes.NewValue(typ.(tftypes.Object), map[string]tftypes.Value{
		"id":          i64(m.ID),
		"number":      str(m.Number),
		"company":     str(m.Company),
		"first_name":  str(m.FirstName),
		"last_name":   str(m.LastName),
		"email":       str(m.Email),
		"phone":       str(m.Phone),
		"address":     str(m.Address),
		"zip":         str(m.Zip),
		"cvr":         str(m.CVR),
		"ean":         str(m.EAN),
		"description": str(m.Description),
		"city":        str(m.City),
		"case_count":  i64(m.CaseCount),
	})
}

func customerResPlan(t *testing.T, m customerResourceModel) tfsdk.Plan {
	t.Helper()
	return tfsdk.Plan{Schema: customerResSchema(t), Raw: customerResValue(t, m)}
}

func customerResState(t *testing.T, m customerResourceModel) tfsdk.State {
	t.Helper()
	return tfsdk.State{Schema: customerResSchema(t), Raw: customerResValue(t, m)}
}

func emptyCustomerResState(t *testing.T) tfsdk.State {
	t.Helper()
	return tfsdk.State{Schema: customerResSchema(t), Raw: tftypes.Value{}}
}

func customerResModelFor(company string) customerResourceModel {
	return customerResourceModel{
		ID: types.Int64Unknown(), Number: types.StringUnknown(),
		Company:   types.StringValue(company),
		FirstName: types.StringValue("Bilbo"), LastName: types.StringValue("Baggins"),
		Email: types.StringValue("bilbo@example.com"), Phone: types.StringValue("+4520000001"),
		Address: types.StringValue("Bagshot Row 1"), Zip: types.StringValue("2200"),
		CVR: types.StringValue("12345678"), EAN: types.StringNull(),
		Description: types.StringValue("Managed by Terraform"),
		City:        types.StringUnknown(), CaseCount: types.Int64Unknown(),
	}
}

func TestCustomerResource_Metadata(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewCustomerResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "kala"}, resp)
	if resp.TypeName != "kala_customer" {
		t.Errorf("TypeName = %q, want kala_customer", resp.TypeName)
	}
}

// Kala cannot delete a customer and has no deactivation flag, so the schema
// must say so where a reader will actually meet it.
func TestCustomerResource_SchemaDocumentsIrreversibleCreate(t *testing.T) {
	desc := customerResSchema(t).MarkdownDescription
	for _, want := range []string{"irreversible", "import"} {
		if !strings.Contains(strings.ToLower(desc), want) {
			t.Errorf("schema description does not mention %q: %s", want, desc)
		}
	}
}

func TestCreateCustomer_RecordsAllocatedIdentity(t *testing.T) {
	fi := newFakeInternal()
	r := newCustomerResource(fi)

	resp := &resource.CreateResponse{State: emptyCustomerResState(t)}
	r.Create(context.Background(),
		resource.CreateRequest{Plan: customerResPlan(t, customerResModelFor("Bag End Ltd"))}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %s", diagsText(resp.Diagnostics))
	}
	if !fi.addCustomerCalled {
		t.Fatal("AddCustomer was not called")
	}
	if fi.customerIn.Company != "Bag End Ltd" || fi.customerIn.CVR != "12345678" {
		t.Errorf("AddCustomer got %+v", fi.customerIn)
	}

	var got customerResourceModel
	resp.State.Get(context.Background(), &got)
	if got.ID.ValueInt64() != 4 {
		t.Errorf("id = %d, want the allocated 4", got.ID.ValueInt64())
	}
	if got.Number.ValueString() != "KA-4" {
		t.Errorf("number = %q, want KA-4", got.Number.ValueString())
	}
}

// AC7 / FR5. Kala has no delete: a create that fails after the record exists
// must still leave it in state, or the operator has an orphan they cannot find
// and cannot remove.
func TestCreateCustomer_FailureAfterCreationStillRecordsState(t *testing.T) {
	fi := newFakeInternal()
	fi.addCustomerErr = errors.New("read-back failed")
	r := newCustomerResource(fi)

	resp := &resource.CreateResponse{State: emptyCustomerResState(t)}
	r.Create(context.Background(),
		resource.CreateRequest{Plan: customerResPlan(t, customerResModelFor("Bag End Ltd"))}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed create must report an error")
	}

	var got customerResourceModel
	resp.State.Get(context.Background(), &got)
	if got.ID.ValueInt64() != 4 {
		t.Fatalf("id = %d; the customer exists upstream and must be in state, "+
			"because Kala cannot delete it", got.ID.ValueInt64())
	}

	var warned bool
	for _, d := range resp.Diagnostics.Warnings() {
		if strings.Contains(strings.ToLower(d.Summary()+d.Detail()), "permanent") {
			warned = true
		}
	}
	if !warned {
		t.Error("want a warning that the customer exists permanently")
	}
}

// TF1.2: a genuine absence is drift, not an error.
func TestReadCustomer_MissingIsDrift(t *testing.T) {
	fi := newFakeInternal()
	r := newCustomerResource(fi)

	m := customerResModelFor("Bag End Ltd")
	m.ID, m.Number = types.Int64Value(99), types.StringValue("KA-99")
	m.City, m.CaseCount = types.StringNull(), types.Int64Value(0)

	resp := &resource.ReadResponse{State: customerResState(t, m)}
	r.Read(context.Background(), resource.ReadRequest{State: customerResState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a missing customer is drift, not an error: %s", diagsText(resp.Diagnostics))
	}
	if !resp.State.Raw.IsNull() {
		t.Error("state should have been removed for a customer that no longer exists")
	}
}

// EditCustomer is a full-record replace: everything the model holds must be
// sent, or the write blanks whatever it omitted.
func TestUpdateCustomer_SendsEveryFieldSoNothingIsBlanked(t *testing.T) {
	fi := newFakeInternal()
	fi.customers[4] = client.Customer{ID: 4, Number: "KA-4", Company: "Old Name"}
	r := newCustomerResource(fi)

	m := customerResModelFor("Bag End Ltd")
	m.ID, m.Number = types.Int64Value(4), types.StringValue("KA-4")
	m.City, m.CaseCount = types.StringNull(), types.Int64Value(0)

	resp := &resource.UpdateResponse{State: customerResState(t, m)}
	r.Update(context.Background(),
		resource.UpdateRequest{Plan: customerResPlan(t, m), State: customerResState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %s", diagsText(resp.Diagnostics))
	}
	if !fi.editCustomerCalled {
		t.Fatal("EditCustomer was not called")
	}
	if fi.editedID != 4 {
		t.Errorf("edited id = %d, want 4", fi.editedID)
	}
	in := fi.customerIn
	if in.Company == "" || in.Email == "" || in.Phone == "" || in.Address == "" ||
		in.Zip == "" || in.CVR == "" || in.Description == "" {
		t.Errorf("update sent a partial record, which blanks fields upstream: %+v", in)
	}
}

// ADR-001 and TF1.1: destroy writes nothing and says so. Customers have no
// off switch at all -- not deactivation, not archival.
func TestDeleteCustomer_WritesNothingUpstreamAndWarns(t *testing.T) {
	fi := newFakeInternal()
	fi.customers[4] = client.Customer{ID: 4, Number: "KA-4", Company: "Bag End Ltd"}
	r := newCustomerResource(fi)

	m := customerResModelFor("Bag End Ltd")
	m.ID, m.Number = types.Int64Value(4), types.StringValue("KA-4")
	m.City, m.CaseCount = types.StringNull(), types.Int64Value(0)

	resp := &resource.DeleteResponse{State: customerResState(t, m)}
	r.Delete(context.Background(), resource.DeleteRequest{State: customerResState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("destroy must not fail: %s", diagsText(resp.Diagnostics))
	}
	if fi.editCustomerCalled || fi.addCustomerCalled {
		t.Error("destroy issued an upstream write; ADR-001 forbids it for customers")
	}

	warnings := resp.Diagnostics.Warnings()
	if len(warnings) == 0 {
		t.Fatal("TF1.1: destroy must warn that the record remains")
	}
	text := strings.ToLower(warnings[0].Summary() + " " + warnings[0].Detail())
	if !strings.Contains(text, "ka-4") && !strings.Contains(text, "bag end ltd") {
		t.Errorf("the warning must name the record left behind: %s", text)
	}
}

// TF1.7: import is the ONLY way to adopt an existing customer, because create
// allocates a new one rather than matching.
func TestImportCustomer_ByNumericID(t *testing.T) {
	r := newCustomerResource(newFakeInternal())
	resp := &resource.ImportStateResponse{State: emptyCustomerResState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "4"}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %s", diagsText(resp.Diagnostics))
	}
	var got customerResourceModel
	resp.State.Get(context.Background(), &got)
	if got.ID.ValueInt64() != 4 {
		t.Errorf("imported id = %d, want 4", got.ID.ValueInt64())
	}
}

func TestImportCustomer_NonNumericIDIsAnError(t *testing.T) {
	r := newCustomerResource(newFakeInternal())
	resp := &resource.ImportStateResponse{State: emptyCustomerResState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "KA-4"}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("import takes the numeric id, not the customer number; a number must be rejected clearly")
	}
}
