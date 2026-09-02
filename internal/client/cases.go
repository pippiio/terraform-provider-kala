// Story: Case read path (internal app API)
//
// Input:  ctx, CaseQuery{Archived, Search, PageSize, MaxPages}
//
// Process:
//   1. List via POST /api/GetAllJobsSimplePaged/, whose body carries the
//      filters. archivedJobs is a MODE SWITCH, not an inclusion flag: false
//      returns non-archived cases only, true returns archived only, and the
//      two sets are disjoint (verified 2026-09-02 by archiving a case and
//      diffing the four flag combinations). There is no single-call "everything".
//   2. Do NOT expose the list's isFinished field. The same case reads true from
//      the list while archived and false from the detail endpoint, because the
//      list variant tracks archived-ness rather than completion. Archived is
//      instead derived from the query mode, which is knowledge we already hold
//      and upstream cannot contradict.
//   3. finishedJobs is sent as false and never varied. It had no observable
//      effect in any combination, and an unexplained parameter must not become
//      a feature.
//   4. Page until Fetched >= Total or the cap, reporting coverage -- as
//      ListCustomers does, for the same reason.
//   5. Fetch one case by number via GET /api/GetJobDetailsAdvanced/?caseNr=,
//      which returns 64 fields against the list's ~25 (risk R1 confirmed).
//   6. An unknown caseNr returns HTTP 500, not 404. Map it to ErrNotFound only
//      after retries are exhausted: the retry policy already covers 5xx, so a
//      transient server error recovers on its own and only a CONSISTENT 500
//      becomes not-found. See the warning on GetCase before reusing this.
//   7. Parse .NET /Date(milliseconds)/ strings into time.Time. Passing them
//      through would write /Date(1788333586263)/ into Terraform state.
//
// Output: CaseScan{Cases, Total, Fetched, Pages} with Complete(), and CaseDetail.
//
// Dependencies: internalAPI.authedRequest, ErrDecode, ErrNotFound, ErrServer.
// Side effects: outbound HTTPS only. No mutation -- ArchiveCase and CreateCase
//               exist and work, but neither is called from provider code and
//               neither is on ARCH1.3's permitted-write list.

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Case is the provider-owned representation of a Kala case.
//
// Kala calls this entity three things: "project" on webapiv2, "job" in internal
// endpoint names, and "case" in internal response payloads and the /Case/
// controller. One provider term, fixed here (ARCH1.4).
type Case struct {
	ID     int64
	Number string
	Name   string

	// Archived is derived from the query mode, NOT from any response field.
	// The list's isFinished cannot be trusted for this -- see the Story.
	Archived bool

	EconomyCaseNumber string
	Address           string
	Zip               string
	SubText           string

	CustomerName    string
	CustomerCompany string
	CustomerEmail   string
	CustomerPhone   string

	InternalProject bool
	Restricted      bool
	Favorite        bool
}

// CaseDetail is the richer per-case read. IsFinished here is completion, unlike
// the list field of the same name (FR13).
type CaseDetail struct {
	Case

	CustomerID              int64
	IsFinished              bool
	ChecklistItemsTotal     int
	ChecklistItemsCompleted int
	EconomySyncFailed       bool

	StartDate *time.Time
	EndDate   *time.Time
	Deadline  *time.Time
}

// CaseQuery controls a case list read.
type CaseQuery struct {
	// Archived selects WHICH SET to return, and the sets are disjoint:
	// false yields non-archived cases, true yields archived ones.
	Archived bool

	Search   string
	PageSize int
	MaxPages int
}

// CaseScan is a case list read with the coverage it achieved.
type CaseScan struct {
	Cases   []Case
	Total   int
	Fetched int
	Pages   int
}

// Complete reports whether the read covered everything upstream claimed.
func (s CaseScan) Complete() bool { return s.Fetched >= s.Total }

// parseDotNetDate converts ASP.NET's /Date(milliseconds)/ wire format.
//
// Returns nil for null, empty, and the .NET zero date, which all mean "unset"
// upstream. An unrecognised non-empty value is an error rather than a silent
// nil: writing an unparsed /Date(...)/ into state is a defect every user sees.
func parseDotNetDate(raw string) (*time.Time, error) {
	return nil, nil // stub: RED
}

// ListCases reads one set of cases -- archived or not, per q.Archived.
func (c *internalAPI) ListCases(ctx context.Context, q CaseQuery) (CaseScan, error) {
	return CaseScan{}, nil // stub: RED
}

// GetCase reads one case by its case NUMBER (a string such as "KA-1"), not by
// the int caseId that tasks use to link to it.
//
// WARNING: an unknown caseNr returns HTTP 500, so a consistent server error is
// reported as ErrNotFound. That is safe for a data source, which only reads.
// It would NOT be safe for a resource: under TF1.2 a not-found on Read means
// RemoveResource, so a genuine outage would silently drop a live case from
// state. Revisit this mapping before any kala_case resource is built.
func (c *internalAPI) GetCase(ctx context.Context, caseNumber string) (CaseDetail, error) {
	return CaseDetail{}, nil // stub: RED
}

var (
	_ = bytes.TrimSpace
	_ = json.Unmarshal
	_ = errors.Is
	_ = fmt.Errorf
	_ = http.MethodGet
	_ = url.Values{}
	_ = strconv.Itoa
	_ = strings.TrimSpace
)
