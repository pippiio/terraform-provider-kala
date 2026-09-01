// Package client talks to Kala. It is deliberately ignorant of Terraform:
// nothing here imports terraform-plugin-framework, and no type in this package
// knows what a diagnostic or a types.String is (guardrail ARCH1.1).
//
// The interface below is expressed in DOMAIN terms, not endpoint names. That is
// the whole point of it. Kala exposes two disjoint APIs — the documented
// webapiv2 (api_key auth, "employee") and the app's internal session API
// (kauthtoken auth, "worker") — with different shapes for the same concept. An
// interface named after webapiv2's endpoints would be a rename rather than an
// abstraction, and the second implementation could not satisfy it without the
// provider layer type-switching on which client it holds. See ADR-adjacent
// discussion in the track spec, risk R7.
package client

import "context"

// Client is the domain-shaped contract every Kala backend must satisfy.
//
// Implementations map their own wire format into the domain types below. Wire
// structs stay unexported inside their implementation file so they cannot leak
// into the provider layer.
type Client interface {
	// Ping verifies the configured credentials. It returns an error satisfying
	// errors.Is(err, ErrUnauthorized) when the credentials are rejected.
	Ping(ctx context.Context) error

	// ListEmployees returns active employees, following pagination to
	// completion subject to the options given.
	ListEmployees(ctx context.Context, opts ListOptions) ([]Employee, error)

	// GetEmployee returns a single employee by their Kala employee number.
	// It returns an error satisfying errors.Is(err, ErrNotFound) when no such
	// employee exists.
	GetEmployee(ctx context.Context, number int64) (Employee, error)

	// ApplyEmployeeSetting creates or updates one setting on one employee.
	//
	// There is no corresponding delete: Kala offers no way to remove a setting,
	// only to overwrite its value. Callers should validate the key first.
	ApplyEmployeeSetting(ctx context.Context, employeeNumber int64, s Setting, meta SettingMetadata) error

	// ScanSettingKeys surveys the account for every setting key in use,
	// sorted and deduplicated. It backs the unknown-key guard.
	//
	// The survey is bounded, so the result reports its own completeness
	// rather than only its findings — see SettingKeyScan.
	ScanSettingKeys(ctx context.Context) (SettingKeyScan, error)
}

// Employee is the provider-owned representation of a person in Kala.
//
// Field names are ours, not the API's. webapiv2 calls the identifier "number"
// while the internal API calls it "workerId"/"workerNr"; both map onto Number.
type Employee struct {
	Number   int64
	Name     string
	Title    string
	Phone    string
	Image    string
	IsAdmin  bool
	IsLeader bool
	Settings []Setting
}

// Setting is a single key/value configuration entry on an employee.
//
// Only Key and Value are populated from reads: both Kala read endpoints return
// {key, value} and nothing more. FriendlyName and Type exist on the WRITE side
// only (SetEmployeeSetting requires friendlyName), which makes them structurally
// write-only — they can be sent but never read back. They are deliberately
// absent from this struct so that no read path can imply it knows them.
type Setting struct {
	Key   string
	Value string
}

// ListOptions controls pagination and ordering for list calls.
type ListOptions struct {
	// Order is "asc" or "desc". Empty means the API default ("asc").
	Order string

	// PageSize is the number of records per request. Zero means the client
	// default, which is deliberately far below the API's documented default of
	// 5000 to bound response size (risk R4).
	PageSize int

	// MaxPages caps how many pages will be fetched, guaranteeing termination
	// even if the API never returns a short page (guardrail GO1.6).
	MaxPages int
}
