// Package provider implements the Terraform provider surface for Kala.
//
// Layering: this package owns everything
// Terraform-facing — schemas, diagnostics, state mapping. It never constructs
// HTTP requests directly; all upstream access goes through internal/client.
package provider

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/pippiio/terraform-provider-kala/internal/client"
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
	Username                 types.String `tfsdk:"username"`
	Password                 types.String `tfsdk:"password"`
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
		MarkdownDescription: "Manages configuration in [Kala](https://kala.app), a Danish " +
			"work-management platform for construction and service trades. Employees are " +
			"managed as resources; their settings, customers, cases, and tasks are read-only.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Base URL of the Kala webapiv2 API. Defaults to `" + client.DefaultEndpoint + "`. May also be set via `KALA_ENDPOINT`.",
			},
			"api_key": schema.StringAttribute{
				Optional:  true,
				Sensitive: true, // keeps the credential out of plan output
				MarkdownDescription: "API key for the Kala webapiv2 API. Required only by the " +
					"`kala_employees` data source, the provider's one consumer of webapiv2; everything " +
					"else uses `username`/`password`. May also be set via the `KALA_API_KEY` " +
					"environment variable, which is preferred so the credential stays out of version control.",
			},
			"company": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "Kala company identifier. May also be set via `KALA_COMPANY`.\n\n" +
					"A Kala login can belong to several companies, and this chooses which one the " +
					"provider acts on — including which company's employees are created and " +
					"deactivated. It may be omitted when the login belongs to exactly one; when it " +
					"belongs to several, the provider refuses to guess and asks for this rather than " +
					"writing to whichever Kala happens to list first.",
			},
			"timeout_seconds": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Per-request timeout in seconds. Defaults to 30.",
			},
			"max_retries": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Maximum retries for transient failures. Only 5xx and transport errors are retried; 4xx never is. Defaults to 3.",
			},
			"username": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Username for Kala's internal app API. Required by every resource " +
					"and by every data source except `kala_employees`: the internal API owns employee " +
					"lifecycle, which webapiv2 does not expose, and is the only source of customers, " +
					"cases, and tasks. Must be set together with `password`. " +
					"May also be set via `KALA_USERNAME`.",
			},
			"password": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "Password for Kala's internal app API. Must be set together with " +
					"`username`. May also be set via `KALA_PASSWORD`, which is preferred so the " +
					"credential stays out of version control.",
			},
			"skip_credential_validation": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Skip the credential check performed during provider configuration. " +
					"Terraform runs provider configuration for `validate` and `plan` as well as `apply`, so the " +
					"default check requires network reachability for every command. Set this to `true` in " +
					"credential-less CI jobs that only validate configuration: it skips the check for both " +
					"APIs, and also lifts the requirement to supply any credential at all. Defaults to `false`.",
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

	// api_key is optional. It backs exactly one data source — kala_employees —
	// so requiring it from someone who only manages cases, customers and tasks
	// would force them to obtain a credential the provider never sends.
	apiKey, apiKeyErr := resolveCredential(config.APIKey.ValueString(), "KALA_API_KEY")

	// The internal API is likewise optional, and needs both halves: one without
	// the other is a configuration mistake rather than an opt-out.
	username, userErr := resolveCredential(config.Username.ValueString(), "KALA_USERNAME")
	password, passErr := resolveCredential(config.Password.ValueString(), "KALA_PASSWORD")

	hasWeb := apiKeyErr == nil
	hasInternal := userErr == nil && passErr == nil
	skipValidation := config.SkipCredentialValidation.ValueBool()

	// With neither credential the provider can serve nothing, so say so once and
	// name every source rather than singling out api_key.
	//
	// skip_credential_validation lifts this too: it advertises itself as the
	// switch for credential-less CI jobs that only run terraform validate, and
	// that is only true if a run with no credentials at all can configure.
	if !hasWeb && !hasInternal && !skipValidation {
		resp.Diagnostics.AddError(
			"No Kala credentials configured",
			"The provider needs at least one credential, and which one depends on what you "+
				"manage.\n\n"+
				"Set api_key, or export KALA_API_KEY: required only by the kala_employees data "+
				"source.\n\n"+
				"Set username and password, or export KALA_USERNAME and KALA_PASSWORD: required "+
				"by every other data source and by every resource.\n\n"+
				"Prefer the environment variables so credentials stay out of version control. "+
				"Set skip_credential_validation = true to configure with no credentials at all, "+
				"for a CI job that only runs terraform validate.",
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

	company := config.Company.ValueInt64()
	if config.Company.IsNull() {
		if v, err := resolveCredential("", "KALA_COMPANY"); err == nil {
			if n, convErr := strconv.ParseInt(v, 10, 64); convErr == nil {
				company = n
			} else {
				resp.Diagnostics.AddAttributeError(
					path.Root("company"),
					"KALA_COMPANY is not a number",
					fmt.Sprintf("KALA_COMPANY = %q, which is not a valid company identifier.", v),
				)
				return
			}
		}
	}

	cfg := client.Config{
		Endpoint:   endpoint,
		APIKey:     apiKey,
		Company:    company,
		Timeout:    time.Duration(config.TimeoutSeconds.ValueInt64()) * time.Second,
		MaxRetries: int(config.MaxRetries.ValueInt64()),
	}
	if config.MaxRetries.IsNull() {
		cfg.MaxRetries = 3
	}

	clients := &providerClients{}

	if hasWeb {
		clients.Web = client.New(cfg)
	} else {
		tflog.Debug(ctx, "webapiv2 not configured; the kala_employees data source is unavailable")
	}

	if hasInternal {
		clients.Internal = client.NewInternal(client.InternalConfig{
			Username: username,
			Password: password,
			// The internal API's sign-in can return several companies, and the
			// choice decides whose employees get written to. Passing company
			// through means one attribute governs both APIs; without it this
			// client silently took whichever Kala listed first.
			Company:    company,
			Timeout:    time.Duration(config.TimeoutSeconds.ValueInt64()) * time.Second,
			MaxRetries: cfg.MaxRetries,
		})
		tflog.Debug(ctx, "internal Kala API credentials configured")
	} else if userErr == nil || passErr == nil {
		// One without the other is a configuration mistake, not an opt-out.
		resp.Diagnostics.AddWarning(
			"Incomplete Kala internal API credentials",
			"Only one of username/password was supplied, so the internal app API is not configured. "+
				"Employee lifecycle resources will fail until both are set. Supply both, or neither.",
		)
	} else {
		tflog.Debug(ctx, "internal Kala API not configured; lifecycle resources unavailable")
	}

	// Fail fast on bad credentials — but only when asked to, and only for the
	// APIs actually configured.
	//
	// Terraform invokes Configure for validate and plan as well as apply, so an
	// unconditional check makes every command require network reachability and
	// breaks credential-less CI jobs that only validate. This mirrors the AWS
	// provider's skip_credentials_validation.
	if skipValidation {
		tflog.Debug(ctx, "Skipping Kala credential validation at user request")
	} else {
		if clients.Web != nil {
			if err := clients.Web.Ping(ctx); err != nil {
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
		}
		// The internal API was never validated here before, so a bad
		// username/password surfaced only on the first apply. Its Ping is the
		// sign-in handshake, whose session the first real call reuses.
		if clients.Internal != nil {
			if err := clients.Internal.Ping(ctx); err != nil {
				resp.Diagnostics.AddAttributeError(
					path.Root("username"),
					"Could not authenticate against the Kala internal API",
					"Kala rejected the supplied username and password, or was unreachable.\n\n"+
						"Error: "+err.Error()+"\n\n"+
						"Set skip_credential_validation = true to bypass this check, for example in a "+
						"credential-less CI job that only runs terraform validate.",
				)
				return
			}
		}
		tflog.Debug(ctx, "Kala credentials validated")
	}

	resp.DataSourceData = clients
	resp.ResourceData = clients
}

func (p *kalaProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewEmployeesDataSource,
		NewCustomersDataSource,
		NewCustomerDataSource,
		NewCasesDataSource,
		NewCaseDataSource,
		NewTasksDataSource,
		NewTaskDataSource,
	}
}

// Resources returns the provider's resources.
func (p *kalaProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewEmployeeResource,
		NewCustomerResource,
		NewCaseResource,
		NewTaskResource,
		NewTaskAssignmentResource,
	}
}
