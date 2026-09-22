package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// These drive the provider through the real plugin protocol, so Configure and
// Read execute exactly as Terraform would run them — but against an httptest
// server, so the suite still makes no external network calls.

func providerConfigType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"endpoint":                   tftypes.String,
			"api_key":                    tftypes.String,
			"company":                    tftypes.Number,
			"timeout_seconds":            tftypes.Number,
			"max_retries":                tftypes.Number,
			"username":                   tftypes.String,
			"password":                   tftypes.String,
			"skip_credential_validation": tftypes.Bool,
		},
	}
}

type cfgOverrides struct {
	endpoint string
	apiKey   string
	username string
	password string
	skip     *bool
}

func buildProviderConfig(t *testing.T, o cfgOverrides) *tfprotov6.DynamicValue {
	t.Helper()

	typ := providerConfigType()
	vals := map[string]tftypes.Value{
		"endpoint":                   tftypes.NewValue(tftypes.String, nullIfEmpty(o.endpoint)),
		"api_key":                    tftypes.NewValue(tftypes.String, nullIfEmpty(o.apiKey)),
		"company":                    tftypes.NewValue(tftypes.Number, nil),
		"timeout_seconds":            tftypes.NewValue(tftypes.Number, nil),
		"max_retries":                tftypes.NewValue(tftypes.Number, nil),
		"username":                   tftypes.NewValue(tftypes.String, nullIfEmpty(o.username)),
		"password":                   tftypes.NewValue(tftypes.String, nullIfEmpty(o.password)),
		"skip_credential_validation": tftypes.NewValue(tftypes.Bool, boolOrNil(o.skip)),
	}

	dv, err := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, vals))
	if err != nil {
		t.Fatalf("building provider config: %v", err)
	}
	return &dv
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolOrNil(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

func configureProvider(t *testing.T, o cfgOverrides) *tfprotov6.ConfigureProviderResponse {
	t.Helper()

	srv, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatalf("creating server: %v", err)
	}

	resp, err := srv.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{
		Config: buildProviderConfig(t, o),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	return resp
}

func diagText(diags []*tfprotov6.Diagnostic) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.Summary)
		b.WriteString(" | ")
		b.WriteString(d.Detail)
		b.WriteString("\n")
	}
	return b.String()
}

func hasError(diags []*tfprotov6.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			return true
		}
	}
	return false
}

func TestConfigure_SucceedsWithValidCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pong":"pong"}`))
	}))
	defer srv.Close()

	resp := configureProvider(t, cfgOverrides{endpoint: srv.URL, apiKey: "valid-key"})

	if hasError(resp.Diagnostics) {
		t.Fatalf("want success, got: %s", diagText(resp.Diagnostics))
	}
}

// a 401 during configuration must be an actionable, attribute-scoped error.
func TestConfigure_401ProducesActionableDiagnostic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	resp := configureProvider(t, cfgOverrides{endpoint: srv.URL, apiKey: "bad-key"})

	if !hasError(resp.Diagnostics) {
		t.Fatal("want an error diagnostic for a rejected key")
	}
	text := diagText(resp.Diagnostics)
	if !strings.Contains(text, "authenticate") {
		t.Errorf("diagnostic should explain the auth failure, got: %s", text)
	}
	// The remedy must be discoverable from the message itself.
	if !strings.Contains(text, "skip_credential_validation") {
		t.Errorf("diagnostic should mention the escape hatch, got: %s", text)
	}
}

// the credential must never appear in a diagnostic, even though
// it travels in the query string.
func TestConfigure_DiagnosticsNeverContainTheAPIKey(t *testing.T) {
	const key = "top-secret-credential"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	resp := configureProvider(t, cfgOverrides{endpoint: srv.URL, apiKey: key})

	if got := diagText(resp.Diagnostics); strings.Contains(got, key) {
		t.Fatalf("API key leaked into a diagnostic: %s", got)
	}
}

// Pre-mortem finding 4: with the escape hatch set, configuration must not call
// the API at all — otherwise credential-less CI jobs cannot run terraform validate.
func TestConfigure_SkipValidationMakesNoRequest(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	skip := true
	resp := configureProvider(t, cfgOverrides{endpoint: srv.URL, apiKey: "any", skip: &skip})

	if called {
		t.Error("the API was contacted despite skip_credential_validation = true")
	}
	if hasError(resp.Diagnostics) {
		t.Errorf("configuration should succeed when validation is skipped: %s", diagText(resp.Diagnostics))
	}
}

// With no credential of either kind the provider can do nothing, so it must
// fail — naming every source, since either credential set is now sufficient on
// its own for the resources that use it.
func TestConfigure_MissingAllCredentialsNamesEverySource(t *testing.T) {
	t.Setenv("KALA_API_KEY", "")
	t.Setenv("KALA_USERNAME", "")
	t.Setenv("KALA_PASSWORD", "")

	resp := configureProvider(t, cfgOverrides{endpoint: "https://example.test"})

	if !hasError(resp.Diagnostics) {
		t.Fatal("want an error when no credential is available")
	}
	text := diagText(resp.Diagnostics)
	for _, want := range []string{"api_key", "KALA_API_KEY", "username", "KALA_USERNAME", "password", "KALA_PASSWORD"} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostic must name %s, got: %s", want, text)
		}
	}
}

// api_key backs exactly one data source. Requiring it from someone who only
// manages cases, customers and tasks forces them to obtain a credential the
// provider will never send.
func TestConfigure_SucceedsWithoutAPIKeyWhenInternalCredentialsAreSet(t *testing.T) {
	t.Setenv("KALA_API_KEY", "")

	skip := true
	resp := configureProvider(t, cfgOverrides{
		endpoint: "https://example.test",
		username: "user@example.com",
		password: "hunter2",
		skip:     &skip,
	})

	if hasError(resp.Diagnostics) {
		t.Fatalf("username/password alone must configure the provider: %s", diagText(resp.Diagnostics))
	}
}

// skip_credential_validation documents itself as the switch for credential-less
// CI jobs that only run terraform validate. That is only true if it also lifts
// the requirement to supply a credential at all.
func TestConfigure_SkipValidationAllowsNoCredentialsAtAll(t *testing.T) {
	t.Setenv("KALA_API_KEY", "")
	t.Setenv("KALA_USERNAME", "")
	t.Setenv("KALA_PASSWORD", "")

	skip := true
	resp := configureProvider(t, cfgOverrides{skip: &skip})

	if hasError(resp.Diagnostics) {
		t.Fatalf("a credential-less validate run must configure cleanly: %s", diagText(resp.Diagnostics))
	}
}

// With api_key present the provider is usable, so half an internal pair is a
// warning rather than an error — but it must still be said, or employee
// lifecycle resources fail later with no hint that a typo caused it.
func TestConfigure_HalfAnInternalPairWarnsWhenAPIKeyCarriesTheRun(t *testing.T) {
	t.Setenv("KALA_PASSWORD", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pong":"pong"}`))
	}))
	defer srv.Close()

	resp := configureProvider(t, cfgOverrides{
		endpoint: srv.URL,
		apiKey:   "valid-key",
		username: "user@example.com",
	})

	if hasError(resp.Diagnostics) {
		t.Fatalf("api_key alone must still configure the provider: %s", diagText(resp.Diagnostics))
	}
	if !strings.Contains(diagText(resp.Diagnostics), "Incomplete Kala internal API credentials") {
		t.Errorf("want a warning about the incomplete pair, got: %s", diagText(resp.Diagnostics))
	}
}

// Supplying only one half of the internal pair is a mistake, not an opt-out —
// but it must not be mistaken for "no internal credentials" and silently
// swallowed when it is the only credential offered.
func TestConfigure_UsernameWithoutPasswordIsNotACredential(t *testing.T) {
	t.Setenv("KALA_API_KEY", "")
	t.Setenv("KALA_PASSWORD", "")

	resp := configureProvider(t, cfgOverrides{endpoint: "https://example.test", username: "user@example.com"})

	if !hasError(resp.Diagnostics) {
		t.Fatalf("want an error: half a credential pair is not a credential, got: %s", diagText(resp.Diagnostics))
	}
}

func TestConfigure_UsesAPIKeyFromEnvironment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("api_key"); got != "env-key" {
			t.Errorf("api_key = %q, want env-key", got)
		}
		_, _ = w.Write([]byte(`{"pong":"pong"}`))
	}))
	defer srv.Close()

	t.Setenv("KALA_API_KEY", "env-key")

	resp := configureProvider(t, cfgOverrides{endpoint: srv.URL})
	if hasError(resp.Diagnostics) {
		t.Fatalf("want success using the environment credential: %s", diagText(resp.Diagnostics))
	}
}

// End-to-end through the protocol: configure, then read the data source and
// confirm employees come back through every layer.
func TestReadDataSource_ReturnsEmployees(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Ping") {
			_, _ = w.Write([]byte(`{"pong":"pong"}`))
			return
		}
		_, _ = w.Write([]byte(`[
			{"number":4711,"name":"Frodo","title":"Ringbearer","phone":"+45","isAdmin":false,"isLeader":true,
			 "settings":[{"key":"default_work_type","value":"montage"}]},
			{"number":4712,"name":"Sam","settings":[]}
		]`))
	}))
	defer srv.Close()

	ps, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatalf("creating server: %v", err)
	}

	cfgResp, err := ps.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{
		Config: buildProviderConfig(t, cfgOverrides{endpoint: srv.URL, apiKey: "k"}),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	if hasError(cfgResp.Diagnostics) {
		t.Fatalf("configure failed: %s", diagText(cfgResp.Diagnostics))
	}

	dsType := tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"page_size": tftypes.Number,
			"employees": tftypes.List{ElementType: employeeObjectType()},
		},
	}
	dsCfg, err := tfprotov6.NewDynamicValue(dsType, tftypes.NewValue(dsType, map[string]tftypes.Value{
		"page_size": tftypes.NewValue(tftypes.Number, nil),
		"employees": tftypes.NewValue(tftypes.List{ElementType: employeeObjectType()}, nil),
	}))
	if err != nil {
		t.Fatalf("building data source config: %v", err)
	}

	readResp, err := ps.ReadDataSource(context.Background(), &tfprotov6.ReadDataSourceRequest{
		TypeName: "kala_employees",
		Config:   &dsCfg,
	})
	if err != nil {
		t.Fatalf("ReadDataSource: %v", err)
	}
	if hasError(readResp.Diagnostics) {
		t.Fatalf("read failed: %s", diagText(readResp.Diagnostics))
	}
	if readResp.State == nil {
		t.Fatal("no state returned")
	}

	got, err := readResp.State.Unmarshal(dsType)
	if err != nil {
		t.Fatalf("unmarshalling state: %v", err)
	}
	var obj map[string]tftypes.Value
	if err := got.As(&obj); err != nil {
		t.Fatalf("decoding state object: %v", err)
	}

	var employees []tftypes.Value
	if err := obj["employees"].As(&employees); err != nil {
		t.Fatalf("decoding employees list: %v", err)
	}
	if len(employees) != 2 {
		t.Fatalf("got %d employees, want 2", len(employees))
	}
}

func TestReadDataSource_SurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Ping") {
			_, _ = w.Write([]byte(`{"pong":"pong"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ps, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatalf("creating server: %v", err)
	}
	if _, err := ps.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{
		Config: buildProviderConfig(t, cfgOverrides{endpoint: srv.URL, apiKey: "k"}),
	}); err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}

	dsType := tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"page_size": tftypes.Number,
			"employees": tftypes.List{ElementType: employeeObjectType()},
		},
	}
	dsCfg, _ := tfprotov6.NewDynamicValue(dsType, tftypes.NewValue(dsType, map[string]tftypes.Value{
		"page_size": tftypes.NewValue(tftypes.Number, nil),
		"employees": tftypes.NewValue(tftypes.List{ElementType: employeeObjectType()}, nil),
	}))

	resp, err := ps.ReadDataSource(context.Background(), &tfprotov6.ReadDataSourceRequest{
		TypeName: "kala_employees",
		Config:   &dsCfg,
	})
	if err != nil {
		t.Fatalf("ReadDataSource: %v", err)
	}
	if !hasError(resp.Diagnostics) {
		t.Error("want an error diagnostic when the API fails")
	}
}

func employeeObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"number":    tftypes.Number,
			"name":      tftypes.String,
			"title":     tftypes.String,
			"phone":     tftypes.String,
			"image":     tftypes.String,
			"is_admin":  tftypes.Bool,
			"is_leader": tftypes.Bool,
			"settings": tftypes.List{ElementType: tftypes.Object{
				AttributeTypes: map[string]tftypes.Type{
					"key":   tftypes.String,
					"value": tftypes.String,
				},
			}},
		},
	}
}
