// Package provider implements the Terraform provider surface for Kala.
//
// Layering (guardrails ARCH1.1/ARCH1.2): this package owns everything
// Terraform-facing — schemas, diagnostics, state mapping. It never constructs
// HTTP requests directly; all upstream access goes through internal/client.
package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

var _ provider.Provider = &kalaProvider{}

type kalaProvider struct {
	version string
}

// providerModel mirrors the provider block's schema.
type providerModel struct {
	Endpoint                 types.String `tfsdk:"endpoint"`
	APIKey                   types.String `tfsdk:"api_key"`
	Company                  types.Int64  `tfsdk:"company"`
	TimeoutSeconds           types.Int64  `tfsdk:"timeout_seconds"`
	MaxRetries               types.Int64  `tfsdk:"max_retries"`
	SkipCredentialValidation types.Bool   `tfsdk:"skip_credential_validation"`
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
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages configuration in [Kala](https://kala.app), a Danish work-management platform for construction and service trades.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Base URL of the Kala webapiv2 API. Defaults to `" + client.DefaultEndpoint + "`. May also be set via `KALA_ENDPOINT`.",
			},
			"api_key": schema.StringAttribute{
				Optional:  true,
				Sensitive: true, // SEC1.2 — keeps the credential out of plan output
				MarkdownDescription: "API key for the Kala webapiv2 API. May also be set via the `KALA_API_KEY` " +
					"environment variable, which is preferred so the credential stays out of version control.",
			},
			"company": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Kala company identifier. May also be set via `KALA_COMPANY`. Required only by endpoints that take a company parameter.",
			},
			"timeout_seconds": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Per-request timeout in seconds. Defaults to 30.",
			},
			"max_retries": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Maximum retries for transient failures. Only 5xx and transport errors are retried; 4xx never is. Defaults to 3.",
			},
			"skip_credential_validation": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Skip the credential check performed during provider configuration. " +
					"Terraform runs provider configuration for `validate` and `plan` as well as `apply`, so the " +
					"default check requires network reachability for every command. Set this to `true` in " +
					"credential-less CI jobs that only validate configuration. Defaults to `false`.",
			},
		},
	}
}

func (p *kalaProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Unknown values mean another resource must be applied first. Terraform will
	// call Configure again once they are known.
	if config.APIKey.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("api_key"),
			"Unknown Kala API key",
			"The api_key value is not known at plan time. Either set it statically, or supply it via the KALA_API_KEY environment variable.",
		)
		return
	}
	if config.Endpoint.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"Unknown Kala endpoint",
			"The endpoint value is not known at plan time. Either set it statically, or supply it via the KALA_ENDPOINT environment variable.",
		)
		return
	}

	apiKey, err := resolveCredential(config.APIKey.ValueString(), "KALA_API_KEY")
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			path.Root("api_key"),
			"Missing Kala API key",
			err.Error(),
		)
		return
	}

	// endpoint and company are optional; absence is not an error.
	endpoint := config.Endpoint.ValueString()
	if endpoint == "" {
		if v, err := resolveCredential("", "KALA_ENDPOINT"); err == nil {
			endpoint = v
		}
	}

	cfg := client.Config{
		Endpoint:   endpoint,
		APIKey:     apiKey,
		Company:    config.Company.ValueInt64(),
		Timeout:    time.Duration(config.TimeoutSeconds.ValueInt64()) * time.Second,
		MaxRetries: int(config.MaxRetries.ValueInt64()),
	}
	if config.MaxRetries.IsNull() {
		cfg.MaxRetries = 3
	}

	c := client.New(cfg)

	// Fail fast on bad credentials — but only when asked to.
	//
	// Terraform invokes Configure for validate and plan as well as apply, so an
	// unconditional check makes every command require network reachability and
	// breaks credential-less CI jobs that only validate. This mirrors the AWS
	// provider's skip_credentials_validation.
	if !config.SkipCredentialValidation.ValueBool() {
		if err := c.Ping(ctx); err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("api_key"),
				"Could not authenticate against Kala",
				"The Kala API rejected the supplied credentials or was unreachable.\n\n"+
					"Error: "+err.Error()+"\n\n"+
					"Set skip_credential_validation = true to bypass this check, for example in a "+
					"credential-less CI job that only runs terraform validate.",
			)
			return
		}
		tflog.Debug(ctx, "Kala credentials validated")
	} else {
		tflog.Debug(ctx, "Skipping Kala credential validation at user request")
	}

	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *kalaProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewEmployeesDataSource,
	}
}

// Resources returns the provider's resources.
//
// kala_employee (activation state) is deliberately absent: it requires the
// internal API's workerNr, whose relationship to webapiv2's employeeNumber is
// unverified. Guardrail ARCH1.9 blocks that translation until it is confirmed
// against a live tenant, because deactivating the wrong person is serious harm.
// See ADR-002.
func (p *kalaProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewEmployeeSettingResource,
	}
}
