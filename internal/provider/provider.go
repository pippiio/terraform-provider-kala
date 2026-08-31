// Package provider implements the Terraform provider surface for Kala.
//
// Layering (guardrail ARCH1.1/ARCH1.2): this package owns everything
// Terraform-facing — schemas, diagnostics, state mapping. It never constructs
// HTTP requests directly; all upstream access goes through internal/client.
package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// Ensure kalaProvider satisfies the framework's provider interface.
var _ provider.Provider = &kalaProvider{}

type kalaProvider struct {
	version string
}

// New returns a provider factory for the given version string.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &kalaProvider{version: version}
	}
}

func (p *kalaProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "kala"
	resp.Version = p.version
}

func (p *kalaProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	// Populated in Phase 3 (FR3) under TDD.
	resp.Schema = schema.Schema{}
}

func (p *kalaProvider) Configure(_ context.Context, _ provider.ConfigureRequest, _ *provider.ConfigureResponse) {
	// Populated in Phase 3 (FR4, FR5) under TDD.
}

func (p *kalaProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	// kala_employees is registered in Phase 3 (FR11).
	return nil
}

// Resources returns no resources. The Kala API has no DELETE endpoints, so
// resource design is deliberately deferred — see ADR-001 and the employee
// resource track.
func (p *kalaProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}
