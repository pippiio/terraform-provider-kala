package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// --- Read -------------------------------------------------------------------
//
// NOTE ON TDD: Read was implemented during task 3.2, whose only failing test was
// the schema shape. These tests therefore VERIFY rather than drive, which is a
// departure from this repository's strict TDD and is recorded as such in the
// plan. Each one is checked to cover a branch that was otherwise uncovered, so
// they are real tests written late rather than decoration.

func TestCaseAccessRead_PopulatesState(t *testing.T) {
	f := &caseAccessFake{access: client.CaseAccess{
		CaseID: 2, Restricted: true, Assigned: []int64{3, 5, 9},
	}}
	resp := readCaseAccess(t, f, caseNumberConfig("KA-1"))

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	if f.gotNr != "KA-1" {
		t.Errorf("client asked about %q, want KA-1", f.gotNr)
	}

	var state caseAccessDataSourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading state: %v", diags)
	}
	if state.CaseID.ValueInt64() != 2 {
		t.Errorf("case_id = %d, want 2", state.CaseID.ValueInt64())
	}
	if !state.Restricted.ValueBool() {
		t.Error("restricted = false, want true")
	}
	if got := len(state.Assigned.Elements()); got != 3 {
		t.Errorf("employee_numbers has %d elements, want 3", got)
	}
}

// A set must not care about upstream ordering. This is the provider-layer half
// of the guarantee whose client-layer half is the client's own sort: together
// they mean a reordered response can never produce a plan diff (AC3).
func TestCaseAccessRead_SetIgnoresUpstreamOrdering(t *testing.T) {
	first := readCaseAccess(t, &caseAccessFake{
		access: client.CaseAccess{CaseID: 2, Assigned: []int64{3, 5, 9}},
	}, caseNumberConfig("KA-1"))
	second := readCaseAccess(t, &caseAccessFake{
		access: client.CaseAccess{CaseID: 2, Assigned: []int64{9, 3, 5}},
	}, caseNumberConfig("KA-1"))

	var a, b caseAccessDataSourceModel
	if diags := first.State.Get(context.Background(), &a); diags.HasError() {
		t.Fatalf("first state: %v", diags)
	}
	if diags := second.State.Get(context.Background(), &b); diags.HasError() {
		t.Fatalf("second state: %v", diags)
	}
	if !a.Assigned.Equal(b.Assigned) {
		t.Errorf("the same members in a different order produced different state:\n%v\nvs\n%v",
			a.Assigned, b.Assigned)
	}
}

// An unknown case must never come back as an empty set. An empty set states
// that nobody may register time on the case, which for a case that does not
// exist is false rather than merely missing (AC7).
func TestCaseAccessRead_NotFoundIsAttributeScopedNotAnEmptySet(t *testing.T) {
	f := &caseAccessFake{err: fmt.Errorf("case KA-9: %w", client.ErrNotFound)}
	resp := readCaseAccess(t, f, caseNumberConfig("KA-9"))

	if !resp.Diagnostics.HasError() {
		t.Fatal("an unknown case must error, not return an empty set")
	}
	found := false
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "not found") &&
			strings.Contains(d.Detail(), "KA-9") {
			found = true
			// It must admit that a 500 is indistinguishable from absence, so an
			// outage does not send the operator hunting for a typo.
			if !strings.Contains(d.Detail(), "500") {
				t.Errorf("the not-found detail must disclose the 500 ambiguity; got: %s", d.Detail())
			}
		}
	}
	if !found {
		t.Errorf("want a not-found diagnostic naming KA-9; got %v", resp.Diagnostics)
	}
}

func TestCaseAccessRead_OtherErrorsAreNotReportedAsNotFound(t *testing.T) {
	f := &caseAccessFake{err: errors.New("kala: server exploded")}
	resp := readCaseAccess(t, f, caseNumberConfig("KA-1"))

	if !resp.Diagnostics.HasError() {
		t.Fatal("a client error must produce a diagnostic")
	}
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(strings.ToLower(d.Summary()), "not found") {
			t.Errorf("a generic failure must not be reported as a missing case; got: %s", d.Summary())
		}
	}
}

// TF1.5: a null case_number must be refused explicitly rather than read as the
// empty string, which would ask Kala about a case called "" and then report its
// 500 as "no such case" — an answer to a question nobody asked.
func TestCaseAccessRead_NullCaseNumberIsRefused(t *testing.T) {
	f := &caseAccessFake{}
	resp := readCaseAccess(t, f, map[string]tftypes.Value{
		"case_number": tftypes.NewValue(tftypes.String, nil),
	})

	if !resp.Diagnostics.HasError() {
		t.Fatal("a null case_number must be refused")
	}
	if f.gotNr != "" {
		t.Errorf("the client was called with %q; it must not be called at all", f.gotNr)
	}
}

func TestCaseAccessRead_WithoutClientNamesTheCredentials(t *testing.T) {
	resp := &datasource.ReadResponse{}
	(&caseAccessDataSource{}).Read(context.Background(), datasource.ReadRequest{}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("reading without a configured client must error")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	for _, want := range []string{"KALA_USERNAME", "KALA_PASSWORD"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the diagnostic must name %s; got: %s", want, detail)
		}
	}
}

func TestCaseAccessDataSource_Configure(t *testing.T) {
	d := &caseAccessDataSource{}
	var resp datasource.ConfigureResponse
	d.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if d.client != nil {
		t.Error("a nil ProviderData must leave the client nil, not panic")
	}

	d.Configure(context.Background(),
		datasource.ConfigureRequest{ProviderData: "not the clients struct"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("unexpected provider data must produce a diagnostic")
	}
}

func TestProvider_RegistersCaseAccessDataSource(t *testing.T) {
	for _, f := range (&kalaProvider{}).DataSources(context.Background()) {
		var resp datasource.MetadataResponse
		f().Metadata(context.Background(),
			datasource.MetadataRequest{ProviderTypeName: "kala"}, &resp)
		if resp.TypeName == "kala_case_access" {
			return
		}
	}
	t.Error("kala_case_access is not registered in the provider's DataSources")
}

// --- The descriptions ARE the mitigation ------------------------------------
//
// employee_numbers is incomplete by construction and nothing can detect it: the
// grant is only observable through task assignment, so a case with no tasks
// reports empty however many people hold access. Two of the four cases in the
// development tenant have no tasks, so this is the common case.
//
// There is no behavioural test that can fail when that misleads somebody. The
// only protection a reader gets is that the schema says so, which makes these
// assertions the sole mechanism preventing a later edit — a tidy-up, a
// shortening, a translation — from silently removing the warning.
//
// The canonical wording lives in the track's spike-findings.md § Appendix.
// Change it there first, then here.

// TestCaseAccessSchema_WarnsAboutTheUndetectableGap covers AC6a.
func TestCaseAccessSchema_WarnsAboutTheUndetectableGap(t *testing.T) {
	sch := caseAccessSchema(t)

	attr, ok := sch.Attributes["employee_numbers"]
	if !ok {
		t.Fatal("schema is missing employee_numbers")
	}

	// Both placements, because they are read in different situations: the data
	// source's own description heads the generated docs page, while the
	// attribute's reaches editor completion and the per-attribute table.
	for name, desc := range map[string]string{
		"the data source description":      sch.MarkdownDescription,
		"the employee_numbers description": attr.GetMarkdownDescription(),
	} {
		lower := strings.ToLower(desc)
		for _, want := range []string{
			"derived",       // says where the value comes from
			"no tasks",      // names the case that always reports empty
			"empty",         // and what it reports
			"authorization", // the one line a reader may act on
		} {
			if !strings.Contains(lower, want) {
				t.Errorf("%s must mention %q — it is the only protection against a misread.\nGot: %s",
					name, want, desc)
			}
		}
	}
}

// TestCaseAccessSchema_SaysEmptyIsAmbiguousEvenWhenRestricted covers AC12 and
// AC6b — two different ambiguities, and the second one derivation introduced.
//
// AC12: restricted = false means access is unrestricted, so the set says nothing
// at all. AC6b: restricted = true with an empty set means EITHER nobody is
// granted OR those granted hold no tasks, and the two are indistinguishable.
// restricted alone therefore no longer resolves the empty set, which the original
// design assumed it would.
func TestCaseAccessSchema_SaysEmptyIsAmbiguousEvenWhenRestricted(t *testing.T) {
	sch := caseAccessSchema(t)

	restricted, ok := sch.Attributes["restricted"]
	if !ok {
		t.Fatal("schema is missing restricted")
	}
	employees, ok := sch.Attributes["employee_numbers"]
	if !ok {
		t.Fatal("schema is missing employee_numbers")
	}

	// restricted must point at employee_numbers, and say that false means the
	// set is meaningless rather than empty.
	rd := strings.ToLower(restricted.GetMarkdownDescription())
	for _, want := range []string{"employee_numbers", "unrestricted"} {
		if !strings.Contains(rd, want) {
			t.Errorf("the restricted description must mention %q; got: %s", want, restricted.GetMarkdownDescription())
		}
	}

	// employee_numbers must state the ambiguity that survives restricted = true.
	ed := strings.ToLower(employees.GetMarkdownDescription())
	for _, want := range []string{"restricted", "either"} {
		if !strings.Contains(ed, want) {
			t.Errorf("the employee_numbers description must mention %q so that an empty set under "+
				"restricted = true is not read as \"nobody is granted\"; got: %s",
				want, employees.GetMarkdownDescription())
		}
	}
}

// TestCaseAccessSchema_SaysGrantedNotAble covers F9: a deactivated employee may
// remain in the grant and will not resolve through kala_employee, so anyone
// iterating the set with for_each needs warning.
func TestCaseAccessSchema_SaysGrantedNotAble(t *testing.T) {
	attr, ok := caseAccessSchema(t).Attributes["employee_numbers"]
	if !ok {
		t.Fatal("schema is missing employee_numbers")
	}
	desc := strings.ToLower(attr.GetMarkdownDescription())
	for _, want := range []string{"granted", "able"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the description must distinguish who is %q from who is able to register; got: %s",
				want, attr.GetMarkdownDescription())
		}
	}
}

// TestCaseAccessRead_MismatchedConfigSchemaIsReported covers the guard after
// Config.Get. A config whose schema does not match the model is a provider bug
// rather than a user error, and it must surface as a diagnostic instead of
// proceeding with a zero-valued config — which would ask Kala about a case
// numbered "" and report its 500 as "no such case".
func TestCaseAccessRead_MismatchedConfigSchemaIsReported(t *testing.T) {
	f := &caseAccessFake{}
	sch := caseAccessSchema(t)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch}}

	// Deliberately the WRONG schema: kala_case's, which has attributes this
	// model does not declare.
	wrong := dsConfig(t, caseSchema(t), map[string]tftypes.Value{
		"case_number": tftypes.NewValue(tftypes.String, "KA-1"),
	})
	(&caseAccessDataSource{client: f}).Read(context.Background(),
		datasource.ReadRequest{Config: wrong}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a config that does not match the model must produce a diagnostic")
	}
	if f.gotNr != "" {
		t.Errorf("the client was called with %q; it must not be called at all", f.gotNr)
	}
}

// TestCaseAccessSchema_SaysCaseGrantsNotEffectivePermission covers AC10 and
// decision D4. Without it the data source's opening line — "the employees who
// may register time against it" — overclaims: anyone who may register time
// through a role rather than a grant on this case is not in the set.
//
// Found by reading the generated docs page against AC10, which the other
// wording tests did not cover.
func TestCaseAccessSchema_SaysCaseGrantsNotEffectivePermission(t *testing.T) {
	sch := caseAccessSchema(t)
	attr, ok := sch.Attributes["employee_numbers"]
	if !ok {
		t.Fatal("schema is missing employee_numbers")
	}
	for name, desc := range map[string]string{
		"the data source description":      sch.MarkdownDescription,
		"the employee_numbers description": attr.GetMarkdownDescription(),
	} {
		lower := strings.ToLower(desc)
		for _, want := range []string{"effective-permission", "role"} {
			if !strings.Contains(lower, want) {
				t.Errorf("%s must say the set is not an effective-permission set and excludes "+
					"role-based access (missing %q).\nGot: %s", name, want, desc)
			}
		}
	}
}

// TestCaseAccessSchema_LongFormHasNoList keeps the generated page's frontmatter
// readable. tfplugindocs strips the markdown description into the frontmatter
// summary, and it glues list items together with no space between them
// ("...access to it.An employee granted..."). Paragraphs survive intact.
//
// An earlier version of this test asserted that a plain Description was set.
// It passed, and the frontmatter did not change: tfplugindocs does not use that
// field. This asserts on the cause instead.
func TestCaseAccessSchema_LongFormHasNoList(t *testing.T) {
	if strings.Contains(caseAccessSchema(t).MarkdownDescription, "\n- ") {
		t.Error("the data source description contains a markdown list; tfplugindocs glues list " +
			"items together in the frontmatter summary. Use short paragraphs instead.")
	}
}
