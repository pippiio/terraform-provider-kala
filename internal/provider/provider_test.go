package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

func newTestProvider(t *testing.T) provider.Provider {
	t.Helper()
	return New("test")()
}

func TestProvider_Metadata(t *testing.T) {
	p := newTestProvider(t)

	resp := &provider.MetadataResponse{}
	p.Metadata(context.Background(), provider.MetadataRequest{}, resp)

	if resp.TypeName != "kala" {
		t.Errorf("TypeName = %q, want kala", resp.TypeName)
	}
	if resp.Version != "test" {
		t.Errorf("Version = %q, want test", resp.Version)
	}
}

// FR3: every configurable attribute must exist, and the credential must be
// marked Sensitive so Terraform redacts it in plan output (SEC1.2).
func TestProvider_SchemaAttributes(t *testing.T) {
	p := newTestProvider(t)

	resp := &provider.SchemaResponse{}
	p.Schema(context.Background(), provider.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}

	want := []string{
		"endpoint",
		"api_key",
		"company",
		"timeout_seconds",
		"max_retries",
		"skip_credential_validation",
	}
	for _, name := range want {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Errorf("missing attribute %q", name)
			continue
		}
		if attr.IsRequired() {
			t.Errorf("attribute %q is Required; all provider attributes must be Optional so environment fallback works (SEC1.4)", name)
		}
	}

	apiKey, ok := resp.Schema.Attributes["api_key"]
	if !ok {
		t.Fatal("api_key attribute missing")
	}
	if !apiKey.IsSensitive() {
		t.Error("api_key must be Sensitive (SEC1.2) — otherwise the credential appears in plan output")
	}
}

func TestProvider_SchemaIsValid(t *testing.T) {
	// providerserver validates the schema shape; an invalid schema fails here
	// rather than at runtime in Terraform.
	srv, err := providerserver.NewProtocol6WithError(newTestProvider(t))()
	if err != nil {
		t.Fatalf("creating provider server: %v", err)
	}

	resp, err := srv.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("schema error: %s — %s", d.Summary, d.Detail)
		}
	}
	if resp.Provider == nil {
		t.Fatal("provider schema is nil")
	}
}

// FR11: the data source must be registered, or no configuration can use it.
func TestProvider_RegistersEmployeesDataSource(t *testing.T) {
	p := newTestProvider(t)

	factories := p.DataSources(context.Background())
	if len(factories) == 0 {
		t.Fatal("no data sources registered")
	}

	var names []string
	for _, f := range factories {
		resp := &datasource.MetadataResponse{}
		f().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "kala"}, resp)
		names = append(names, resp.TypeName)
	}

	if !contains(names, "kala_employees") {
		t.Errorf("kala_employees not registered; got %v", names)
	}
}

// Documents exactly which resources exist — and, more importantly, which one
// deliberately does not.
func TestProvider_RegistersExpectedResources(t *testing.T) {
	p := newTestProvider(t)

	var names []string
	for _, f := range p.Resources(context.Background()) {
		resp := &resource.MetadataResponse{}
		f().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "kala"}, resp)
		names = append(names, resp.TypeName)
	}

	// ARCH1.9 was resolved on 2026-09-01 — medarbejderNr, workerNr, and
	// employeeNumber are confirmed to be one value — so kala_employee is now
	// registered.
	if !contains(names, "kala_employee") {
		t.Errorf("kala_employee not registered; got %v", names)
	}
}

func TestResolveCredential_ConfigWinsOverEnvironment(t *testing.T) {
	t.Setenv("KALA_API_KEY", "from-env")

	got, err := resolveCredential("config-value", "KALA_API_KEY")
	if err != nil {
		t.Fatalf("resolveCredential: %v", err)
	}
	if got != "config-value" {
		t.Errorf("got %q, want the config value to win", got)
	}
}

func TestResolveCredential_FallsBackToEnvironment(t *testing.T) {
	t.Setenv("KALA_API_KEY", "from-env")

	got, err := resolveCredential("", "KALA_API_KEY")
	if err != nil {
		t.Fatalf("resolveCredential: %v", err)
	}
	if got != "from-env" {
		t.Errorf("got %q, want from-env", got)
	}
}

// The diagnostic must name both places a user could set the value — naming only
// one sends half of them looking in the wrong place.
func TestResolveCredential_AbsentFromBothNamesAttributeAndEnvVar(t *testing.T) {
	t.Setenv("KALA_API_KEY", "")

	_, err := resolveCredential("", "KALA_API_KEY")
	if err == nil {
		t.Fatal("want an error when the credential is absent from both sources")
	}
	msg := err.Error()
	if !strings.Contains(msg, "KALA_API_KEY") {
		t.Errorf("error must name the environment variable, got %q", msg)
	}
	if !strings.Contains(msg, "api_key") {
		t.Errorf("error must name the config attribute, got %q", msg)
	}
}

// --- helpers -------------------------------------------------------------

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
