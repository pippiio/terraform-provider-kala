package provider

import "github.com/pippiio/terraform-provider-kala/internal/client"

// providerClients carries both Kala API clients to resources and data sources.
//
// Kala's surface is split: webapiv2 (api_key) owns employee settings, while the
// internal app API (username/password session) owns employee lifecycle —
// creation via SignUp and activation via SetValidated. A resource may need
// either or both, so Configure hands out one struct rather than a bare client.
type providerClients struct {
	// Web is always present once the provider is configured.
	Web client.Client

	// Internal is nil unless username and password were supplied. Resources
	// that need it must say so with a clear diagnostic rather than panicking.
	Internal client.InternalClient
}

// requireInternal returns the internal client or a diagnostic explaining what
// the user must configure.
func (c *providerClients) requireInternal(diags interface{ AddError(string, string) }) (client.InternalClient, bool) {
	if c == nil || c.Internal == nil {
		diags.AddError(
			"Kala internal API credentials are required",
			"This resource manages employee lifecycle, which lives on Kala's internal app API "+
				"rather than webapiv2.\n\n"+
				"Set username and password in the provider block, or export KALA_USERNAME and "+
				"KALA_PASSWORD. The api_key alone is not sufficient: webapiv2 has no endpoint for "+
				"creating employees or changing their activation state.",
		)
		return nil, false
	}
	return c.Internal, true
}

// requireWeb returns the webapiv2 client or a diagnostic explaining what the
// user must configure.
//
// A nil receiver and a nil Web are different failures: the first means
// Configure never ran, which is a provider bug, while the second is a user
// who simply has no api_key — legitimate, since api_key backs only the
// kala_employees data source.
func (c *providerClients) requireWeb(diags interface{ AddError(string, string) }) (client.Client, bool) {
	if c == nil {
		diags.AddError(
			"Kala client not configured",
			"The provider was not configured before this data source was read. This is a bug in the provider.",
		)
		return nil, false
	}
	if c.Web == nil {
		diags.AddError(
			"Kala API key is required",
			"The kala_employees data source reads Kala's webapiv2, which authenticates with an "+
				"API key.\n\n"+
				"Set api_key in the provider block, or export KALA_API_KEY.\n\n"+
				"username and password cannot substitute here. The internal app API reports "+
				"neither an employee's image nor their settings, and it sees deactivated workers "+
				"that webapiv2 hides, so serving this data source from it would silently change "+
				"what the data source returns.",
		)
		return nil, false
	}
	return c.Web, true
}
