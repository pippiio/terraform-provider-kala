package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/pippiio/terraform-provider-kala/internal/client"
)

type caseAccessFake struct {
	client.InternalClient
	access client.CaseAccess
	err    error
	gotNr  string
}

func (f *caseAccessFake) Ping(context.Context) error { return nil }

func (f *caseAccessFake) GetCaseAccess(_ context.Context, nr string) (client.CaseAccess, error) {
	f.gotNr = nr
	return f.access, f.err
}

func caseAccessSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp datasource.SchemaResponse
	NewCaseAccessDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

func readCaseAccess(t *testing.T, f *caseAccessFake, vals map[string]tftypes.Value) *datasource.ReadResponse {
	t.Helper()
	sch := caseAccessSchema(t)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch}}
	(&caseAccessDataSource{client: f}).Read(context.Background(),
		datasource.ReadRequest{Config: dsConfig(t, sch, vals)}, resp)
	return resp
}

func caseNumberConfig(nr string) map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"case_number": tftypes.NewValue(tftypes.String, nr),
	}
}

func TestCaseAccessDataSource_Metadata(t *testing.T) {
	var resp datasource.MetadataResponse
	NewCaseAccessDataSource().Metadata(context.Background(),
		datasource.MetadataRequest{ProviderTypeName: "kala"}, &resp)
	if resp.TypeName != "kala_case_access" {
		t.Errorf("TypeName = %q, want kala_case_access", resp.TypeName)
	}
}

// TestCaseAccessDataSource_SchemaShape covers AC2: the four attributes, their
// types and their modes. Separate from the description assertions below, because
// the shape and what it says about itself are different failures.
func TestCaseAccessDataSource_SchemaShape(t *testing.T) {
	sch := caseAccessSchema(t)

	for _, tc := range []struct {
		name     string
		required bool
		computed bool
	}{
		{"case_number", true, false},
		{"case_id", false, true},
		{"restricted", false, true},
		{"employee_numbers", false, true},
	} {
		attr, ok := sch.Attributes[tc.name]
		if !ok {
			t.Errorf("schema is missing %s", tc.name)
			continue
		}
		if attr.IsRequired() != tc.required {
			t.Errorf("%s: IsRequired = %t, want %t", tc.name, attr.IsRequired(), tc.required)
		}
		if attr.IsComputed() != tc.computed {
			t.Errorf("%s: IsComputed = %t, want %t", tc.name, attr.IsComputed(), tc.computed)
		}
	}

	// employee_numbers must be a SET of int64, never a list. Upstream ordering
	// is not guaranteed, and a list would report a diff on reordering alone.
	attr, ok := sch.Attributes["employee_numbers"]
	if !ok {
		t.Fatal("schema is missing employee_numbers")
	}
	set, ok := attr.GetType().(basetypes.SetType)
	if !ok {
		t.Fatalf("employee_numbers is %T, want a set — a list diffs on reordering", attr.GetType())
	}
	if !set.ElementType().Equal(basetypes.Int64Type{}) {
		t.Errorf("employee_numbers element type is %v, want int64", set.ElementType())
	}
}
