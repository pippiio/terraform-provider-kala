package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Fills the remaining branches in Configure, Read, and resolveCredential.

// An unknown value means another resource must apply first; the provider must
// say so rather than proceeding with an empty credential.
func TestConfigure_UnknownAPIKeyIsRejected(t *testing.T) {
	typ := providerConfigType()
	dv, err := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, map[string]tftypes.Value{
		"endpoint":                   tftypes.NewValue(tftypes.String, "https://example.test"),
		"api_key":                    tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"company":                    tftypes.NewValue(tftypes.Number, nil),
		"timeout_seconds":            tftypes.NewValue(tftypes.Number, nil),
		"max_retries":                tftypes.NewValue(tftypes.Number, nil),
		"username":                   tftypes.NewValue(tftypes.String, nil),
		"password":                   tftypes.NewValue(tftypes.String, nil),
		"skip_credential_validation": tftypes.NewValue(tftypes.Bool, nil),
	}))
	if err != nil {
		t.Fatalf("building config: %v", err)
	}

	srv, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatalf("creating server: %v", err)
	}
	resp, err := srv.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{Config: &dv})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}

	if !hasError(resp.Diagnostics) {
		t.Fatal("want an error for an unknown api_key")
	}
	if !strings.Contains(diagText(resp.Diagnostics), "not known at plan time") {
		t.Errorf("diagnostic should explain the unknown value, got: %s", diagText(resp.Diagnostics))
	}
}

func TestConfigure_UnknownEndpointIsRejected(t *testing.T) {
	typ := providerConfigType()
	dv, err := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, map[string]tftypes.Value{
		"endpoint":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"api_key":                    tftypes.NewValue(tftypes.String, "k"),
		"company":                    tftypes.NewValue(tftypes.Number, nil),
		"timeout_seconds":            tftypes.NewValue(tftypes.Number, nil),
		"max_retries":                tftypes.NewValue(tftypes.Number, nil),
		"username":                   tftypes.NewValue(tftypes.String, nil),
		"password":                   tftypes.NewValue(tftypes.String, nil),
		"skip_credential_validation": tftypes.NewValue(tftypes.Bool, nil),
	}))
	if err != nil {
		t.Fatalf("building config: %v", err)
	}

	srv, _ := providerserver.NewProtocol6WithError(New("test")())()
	resp, err := srv.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{Config: &dv})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	if !hasError(resp.Diagnostics) {
		t.Fatal("want an error for an unknown endpoint")
	}
}

// Exercises the explicit-value branches for company, timeout, and max_retries.
func TestConfigure_ExplicitTuningValuesAreAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pong":"pong"}`))
	}))
	defer srv.Close()

	typ := providerConfigType()
	dv, err := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, map[string]tftypes.Value{
		"endpoint":                   tftypes.NewValue(tftypes.String, srv.URL),
		"api_key":                    tftypes.NewValue(tftypes.String, "k"),
		"company":                    tftypes.NewValue(tftypes.Number, 42),
		"timeout_seconds":            tftypes.NewValue(tftypes.Number, 15),
		"max_retries":                tftypes.NewValue(tftypes.Number, 1),
		"username":                   tftypes.NewValue(tftypes.String, nil),
		"password":                   tftypes.NewValue(tftypes.String, nil),
		"skip_credential_validation": tftypes.NewValue(tftypes.Bool, false),
	}))
	if err != nil {
		t.Fatalf("building config: %v", err)
	}

	ps, _ := providerserver.NewProtocol6WithError(New("test")())()
	resp, err := ps.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{Config: &dv})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	if hasError(resp.Diagnostics) {
		t.Errorf("explicit tuning values should be accepted: %s", diagText(resp.Diagnostics))
	}
}

func TestConfigure_EndpointFromEnvironment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pong":"pong"}`))
	}))
	defer srv.Close()

	t.Setenv("KALA_ENDPOINT", srv.URL)

	resp := configureProvider(t, cfgOverrides{apiKey: "k"})
	if hasError(resp.Diagnostics) {
		t.Errorf("endpoint should be read from KALA_ENDPOINT: %s", diagText(resp.Diagnostics))
	}
}

// Read must not panic when the provider was never configured — it must produce
// a diagnostic identifying the bug.
func TestRead_UnconfiguredClientProducesDiagnostic(t *testing.T) {
	ds := &employeesDataSource{}

	resp := &datasource.ReadResponse{}
	ds.Read(context.Background(), datasource.ReadRequest{}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want a diagnostic when the client is not configured")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "bug in the provider") {
		t.Errorf("diagnostic should identify this as a provider bug, got %q", resp.Diagnostics.Errors()[0].Detail())
	}
}

func TestResolveCredential_UnmappedEnvVarFallsBackToGenericWording(t *testing.T) {
	t.Setenv("KALA_UNKNOWN_THING", "")

	_, err := resolveCredential("", "KALA_UNKNOWN_THING")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "KALA_UNKNOWN_THING") {
		t.Errorf("error should still name the env var, got %q", err.Error())
	}
}

// --- company resolution ----------------------------------------------------

// company may come from the environment like every other setting. It governs
// which Kala tenant is written to, so a malformed value must stop configuration
// rather than be silently discarded and fall back to whichever company Kala
// lists first.
func TestConfigure_MalformedCompanyEnvVarIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pong":"pong"}`))
	}))
	defer srv.Close()

	t.Setenv("KALA_COMPANY", "not-a-number")

	resp := configureProvider(t, cfgOverrides{endpoint: srv.URL, apiKey: "k"})
	if !hasError(resp.Diagnostics) {
		t.Fatal("a non-numeric KALA_COMPANY must be rejected")
	}
	if !strings.Contains(diagText(resp.Diagnostics), "KALA_COMPANY") {
		t.Errorf("diagnostic should name the variable, got: %s", diagText(resp.Diagnostics))
	}
}

func TestConfigure_CompanyFromEnvironmentIsAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pong":"pong"}`))
	}))
	defer srv.Close()

	t.Setenv("KALA_COMPANY", "17221")

	resp := configureProvider(t, cfgOverrides{endpoint: srv.URL, apiKey: "k"})
	if hasError(resp.Diagnostics) {
		t.Errorf("a numeric KALA_COMPANY should be accepted: %s", diagText(resp.Diagnostics))
	}
}
