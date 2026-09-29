package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
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

// --- Read -------------------------------------------------------------------
//
// NOTE ON TDD: Read was implemented during task 3.2, whose only failing test was
// the schema shape. These tests therefore VERIFY rather than drive, which is a
// departure from this repository's strict TDD and is recorded as such in the
// plan. Each one is checked to cover a branch that was otherwise uncovered, so
// they are real tests written late rather than decoration.

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

// --- Schema -----------------------------------------------------------------

func TestCaseAccessDataSource_SchemaShape(t *testing.T) {
	sch := caseAccessSchema(t)

	for _, tc := range []struct {
		name     string
		required bool
	}{
		{"case_number", true}, {"case_id", false}, {"restricted", false},
		{"granted_employee_numbers", false}, {"assigned_employee_numbers", false},
		{"assigned_without_access", false},
	} {
		attr, ok := sch.Attributes[tc.name]
		if !ok {
			t.Errorf("schema is missing %s", tc.name)
			continue
		}
		if attr.IsRequired() != tc.required || attr.IsComputed() == tc.required {
			t.Errorf("%s: required=%t computed=%t, want required=%t", tc.name,
				attr.IsRequired(), attr.IsComputed(), tc.required)
		}
	}

	// The three lists are SETS of int64: upstream order is not guaranteed, and a
	// list would diff on reordering alone.
	for _, name := range []string{"granted_employee_numbers", "assigned_employee_numbers", "assigned_without_access"} {
		attr, ok := sch.Attributes[name]
		if !ok {
			continue
		}
		set, ok := attr.GetType().(basetypes.SetType)
		if !ok || !set.ElementType().Equal(basetypes.Int64Type{}) {
			t.Errorf("%s is %v, want a set of int64", name, attr.GetType())
		}
	}

	// The old attribute conflated the two lists. It must not come back.
	if _, ok := sch.Attributes["employee_numbers"]; ok {
		t.Error("employee_numbers must not exist: it reported ASSIGNED employees as having access")
	}
}

// --- Read: the two lists and the rule between them --------------------------

func readCaseAccessState(t *testing.T, access client.CaseAccess) caseAccessDataSourceModel {
	t.Helper()
	resp := readCaseAccess(t, &caseAccessFake{access: access}, caseNumberConfig("KA-2"))
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	var m caseAccessDataSourceModel
	if diags := resp.State.Get(context.Background(), &m); diags.HasError() {
		t.Fatalf("reading state: %v", diags)
	}
	return m
}

func setOf(t *testing.T, v types.Set) []int64 {
	t.Helper()
	var out []int64
	if diags := v.ElementsAs(context.Background(), &out, false); diags.HasError() {
		t.Fatalf("set elements: %v", diags)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func equalInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Case KA-2 exactly as found live on 2026-09-29: restricted, employee 23 still
// assigned after his access was revoked.
func TestCaseAccessRead_KA2_AssignedWithoutAccessIsReported(t *testing.T) {
	m := readCaseAccessState(t, client.CaseAccess{
		CaseID: 2, Restricted: true, Granted: []int64{1}, Assigned: []int64{1, 23},
	})

	if m.CaseID.ValueInt64() != 2 || !m.Restricted.ValueBool() {
		t.Errorf("case_id/restricted = %d/%t, want 2/true", m.CaseID.ValueInt64(), m.Restricted.ValueBool())
	}
	for _, tc := range []struct {
		name string
		got  types.Set
		want []int64
	}{
		{"granted_employee_numbers", m.GrantedEmployeeNumbers, []int64{1}},
		{"assigned_employee_numbers", m.AssignedEmployeeNumbers, []int64{1, 23}},
		{"assigned_without_access", m.AssignedWithoutAccess, []int64{23}},
	} {
		if got := setOf(t, tc.got); !equalInts(got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Case KA-4 as found live: unrestricted, nobody on the access list, one employee
// assigned. On an unrestricted case every employee has access, so nobody is
// assigned WITHOUT it -- reporting 1 here would be a false alarm.
func TestCaseAccessRead_UnrestrictedCaseHasNobodyWithoutAccess(t *testing.T) {
	m := readCaseAccessState(t, client.CaseAccess{
		CaseID: 4, Restricted: false, Granted: []int64{}, Assigned: []int64{1},
	})
	if got := setOf(t, m.AssignedWithoutAccess); len(got) != 0 {
		t.Errorf("assigned_without_access = %v, want empty on an unrestricted case", got)
	}
	if got := setOf(t, m.AssignedEmployeeNumbers); !equalInts(got, []int64{1}) {
		t.Errorf("assigned_employee_numbers = %v, want [1]", got)
	}
}

// Granted but not assigned is fine: access without a task is normal.
func TestCaseAccessRead_GrantedWithoutAssignmentIsNotAFinding(t *testing.T) {
	m := readCaseAccessState(t, client.CaseAccess{
		CaseID: 2, Restricted: true, Granted: []int64{1, 5, 23}, Assigned: []int64{1},
	})
	if got := setOf(t, m.AssignedWithoutAccess); len(got) != 0 {
		t.Errorf("assigned_without_access = %v, want empty", got)
	}
	if got := setOf(t, m.GrantedEmployeeNumbers); !equalInts(got, []int64{1, 5, 23}) {
		t.Errorf("granted_employee_numbers = %v, want [1 5 23]", got)
	}
}

// Sets must not care about upstream ordering (AC3).
func TestCaseAccessRead_SetsIgnoreUpstreamOrdering(t *testing.T) {
	a := readCaseAccessState(t, client.CaseAccess{CaseID: 2, Restricted: true,
		Granted: []int64{1, 5}, Assigned: []int64{1, 9, 23}})
	b := readCaseAccessState(t, client.CaseAccess{CaseID: 2, Restricted: true,
		Granted: []int64{5, 1}, Assigned: []int64{23, 1, 9}})
	if !a.GrantedEmployeeNumbers.Equal(b.GrantedEmployeeNumbers) ||
		!a.AssignedEmployeeNumbers.Equal(b.AssignedEmployeeNumbers) ||
		!a.AssignedWithoutAccess.Equal(b.AssignedWithoutAccess) {
		t.Error("the same members in a different order produced different state")
	}
}

// --- Descriptions: the two concepts must not be conflated again --------------

func TestCaseAccessSchema_SaysAssignmentIsNotAccess(t *testing.T) {
	sch := caseAccessSchema(t)
	ds := strings.ToLower(sch.MarkdownDescription)
	for _, want := range []string{"granted", "assigned", "different"} {
		if !strings.Contains(ds, want) {
			t.Errorf("the data source description must mention %q; got: %s", want, sch.MarkdownDescription)
		}
	}
	a := strings.ToLower(sch.Attributes["assigned_employee_numbers"].GetMarkdownDescription())
	if !strings.Contains(a, "not access") {
		t.Errorf("assigned_employee_numbers must say assignment is not access; got: %s", a)
	}
}

func TestCaseAccessSchema_GrantedGovernsOnlyWhenRestricted(t *testing.T) {
	sch := caseAccessSchema(t)
	g := strings.ToLower(sch.Attributes["granted_employee_numbers"].GetMarkdownDescription())
	if !strings.Contains(g, "restricted") {
		t.Errorf("granted_employee_numbers must say it governs access only when restricted; got: %s", g)
	}
	r := strings.ToLower(sch.Attributes["restricted"].GetMarkdownDescription())
	if !strings.Contains(r, "granted_employee_numbers") {
		t.Errorf("restricted must point at granted_employee_numbers; got: %s", r)
	}
}

func TestCaseAccessSchema_WithoutAccessStatesItsRule(t *testing.T) {
	d := strings.ToLower(caseAccessSchema(t).Attributes["assigned_without_access"].GetMarkdownDescription())
	for _, want := range []string{"restricted", "cannot see", "empty"} {
		if !strings.Contains(d, want) {
			t.Errorf("assigned_without_access must mention %q; got: %s", want, d)
		}
	}
}
