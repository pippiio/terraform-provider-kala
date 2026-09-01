package provider

import "github.com/techchapter/terraform-provider-kala/internal/client"

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
