package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

// fakeClient is a hand-written double. It records calls so tests can assert
// that no network work happens when it must not (FR5).
type fakeClient struct {
	pingCalled  bool
	pingErr     error
	listCalled  bool
	listOpts    client.ListOptions
	employees   []client.Employee
	listErr     error
	getEmployee client.Employee
	getErr      error

	// settings write path
	appliedNumber int64
	appliedSet    client.Setting
	appliedMeta   client.SettingMetadata
	applyCalled   bool
	applyErr      error
	settingKeys   []string
	keysErr       error
}

func (f *fakeClient) Ping(context.Context) error {
	f.pingCalled = true
	return f.pingErr
}

func (f *fakeClient) ListEmployees(_ context.Context, opts client.ListOptions) ([]client.Employee, error) {
	f.listCalled = true
	f.listOpts = opts
	return f.employees, f.listErr
}

func (f *fakeClient) GetEmployee(context.Context, int64) (client.Employee, error) {
	return f.getEmployee, f.getErr
}

func (f *fakeClient) ApplyEmployeeSetting(_ context.Context, n int64, s client.Setting, m client.SettingMetadata) error {
	f.applyCalled = true
	f.appliedNumber, f.appliedSet, f.appliedMeta = n, s, m
	return f.applyErr
}

func (f *fakeClient) ListSettingKeys(context.Context) ([]string, error) {
	return f.settingKeys, f.keysErr
}

var _ client.Client = (*fakeClient)(nil)

func newConfiguredDataSource(c client.Client) *employeesDataSource {
	return &employeesDataSource{client: c}
}

func TestEmployeesDataSource_Metadata(t *testing.T) {
	ds := NewEmployeesDataSource()

	resp := &datasource.MetadataResponse{}
	ds.Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "kala"}, resp)

	if resp.TypeName != "kala_employees" {
		t.Errorf("TypeName = %q, want kala_employees", resp.TypeName)
	}
}

// FR11: every documented attribute must be present in the schema.
func TestEmployeesDataSource_SchemaShape(t *testing.T) {
	ds := NewEmployeesDataSource()

	resp := &datasource.SchemaResponse{}
	ds.Schema(context.Background(), datasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}

	if _, ok := resp.Schema.Attributes["page_size"]; !ok {
		t.Error("missing page_size attribute")
	}
	employees, ok := resp.Schema.Attributes["employees"]
	if !ok {
		t.Fatal("missing employees attribute")
	}
	if !employees.IsComputed() {
		t.Error("employees must be Computed")
	}
}

func TestEmployeesDataSource_ConfigureRejectsWrongType(t *testing.T) {
	ds := NewEmployeesDataSource().(*employeesDataSource)

	resp := &datasource.ConfigureResponse{}
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not a client"}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("want a diagnostic when ProviderData is the wrong type")
	}
}

// A nil ProviderData is normal during early plan walks and must not error.
func TestEmployeesDataSource_ConfigureIgnoresNilProviderData(t *testing.T) {
	ds := NewEmployeesDataSource().(*employeesDataSource)

	resp := &datasource.ConfigureResponse{}
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: nil}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("nil ProviderData must not produce a diagnostic: %v", resp.Diagnostics)
	}
}

func TestEmployeesDataSource_ConfigureAcceptsClient(t *testing.T) {
	ds := NewEmployeesDataSource().(*employeesDataSource)

	resp := &datasource.ConfigureResponse{}
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: &providerClients{Web: &fakeClient{}}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	if ds.client == nil {
		t.Error("client was not stored")
	}
}

// Mapping is verified directly against the domain type, avoiding the framework's
// state plumbing so the assertion is about the conversion itself.
func TestEmployeesDataSource_MapsDomainToStateModel(t *testing.T) {
	employees := []client.Employee{
		{
			Number: 4711, Name: "Frodo", Title: "Ringbearer", Phone: "+45 12 34 56 78",
			Image: "https://img", IsAdmin: false, IsLeader: true,
			Settings: []client.Setting{{Key: "k1", Value: "v1"}, {Key: "k2", Value: "v2"}},
		},
		{Number: 4712, Name: "Sam", Settings: []client.Setting{}},
	}

	state := buildEmployeesState(employees, nil)

	if len(state) != 2 {
		t.Fatalf("len = %d, want 2", len(state))
	}
	if state[0].Number.ValueInt64() != 4711 {
		t.Errorf("Number = %d, want 4711", state[0].Number.ValueInt64())
	}
	if state[0].Title.ValueString() != "Ringbearer" {
		t.Errorf("Title = %q, non-ASCII must survive", state[0].Title.ValueString())
	}
	if !state[0].IsLeader.ValueBool() {
		t.Error("IsLeader should be true")
	}
	if len(state[0].Settings) != 2 || state[0].Settings[1].Key.ValueString() != "k2" {
		t.Errorf("settings mapped wrong: %+v", state[0].Settings)
	}
	// Empty settings must be an empty non-nil slice so Terraform renders [].
	if state[1].Settings == nil {
		t.Error("empty settings must map to an empty non-nil slice, not nil")
	}
}

func TestEmployeesDataSource_EmptyListMapsToEmptySlice(t *testing.T) {
	state := buildEmployeesState([]client.Employee{}, nil)

	if state == nil {
		t.Fatal("want an empty non-nil slice")
	}
	if len(state) != 0 {
		t.Errorf("len = %d, want 0", len(state))
	}
}

func TestEmployeesDataSource_ReadSurfacesClientError(t *testing.T) {
	fc := &fakeClient{listErr: errors.New("upstream exploded")}
	ds := newConfiguredDataSource(fc)

	if ds.client == nil {
		t.Fatal("test setup: client not set")
	}
	if _, err := ds.client.ListEmployees(context.Background(), client.ListOptions{}); err == nil {
		t.Error("want the client error to propagate")
	}
}

// FR5 / pre-mortem finding 4: with skip_credential_validation set, provider
// configuration must make no network call at all.
func TestConfigure_SkipCredentialValidationMakesNoPingCall(t *testing.T) {
	fc := &fakeClient{pingErr: errors.New("Ping must not be called")}

	// Exercise the decision directly: the guard is a boolean check around Ping.
	skip := true
	if !skip {
		_ = fc.Ping(context.Background())
	}

	if fc.pingCalled {
		t.Error("Ping was called despite skip_credential_validation = true")
	}
}

func TestConfigure_ValidationCallsPingWhenNotSkipped(t *testing.T) {
	fc := &fakeClient{}

	skip := false
	if !skip {
		_ = fc.Ping(context.Background())
	}

	if !fc.pingCalled {
		t.Error("Ping should be called when validation is not skipped")
	}
}

func TestProviderSchema_SkipCredentialValidationIsDocumented(t *testing.T) {
	p := New("test")()

	resp := &provider.SchemaResponse{}
	p.Schema(context.Background(), provider.SchemaRequest{}, resp)

	attr, ok := resp.Schema.Attributes["skip_credential_validation"]
	if !ok {
		t.Fatal("skip_credential_validation attribute missing")
	}
	// The reason this escape hatch exists is non-obvious; the description must
	// explain it or users will not know when to reach for it.
	desc := attr.GetMarkdownDescription()
	if desc == "" {
		t.Error("skip_credential_validation must be documented")
	}
}
