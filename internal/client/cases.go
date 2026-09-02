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

	// Financial and hour-registration data. Commercially sensitive and
	// transactional; the provider exposes these only behind an explicit opt-in
	// (FR7), but the client supplies them so that opt-in has something to show.
	Cost                 int
	Sales                int
	Result               int
	Invoiced             int
	Uninvoiced           int
	Realised             int
	RegisteredHoursTotal int
	BilledHours          int
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

// wireCase is the list record. Unexported (ARCH1.4, layering_test.go).
//
// isFinished is deliberately ABSENT: on this endpoint it tracks archived-ness,
// not completion, and the same case reads differently here and on the detail
// endpoint. Not decoding it is the cheapest way to guarantee it is never used.
type wireCase struct {
	CaseID             int64  `json:"caseId"`
	CaseNumber         string `json:"caseNumber"`
	CaseName           string `json:"caseName"`
	EconomyCaseNumber  string `json:"economyCaseNumber"`
	Address            string `json:"address"`
	Zip                string `json:"zip"`
	SubText            string `json:"subText"`
	CustomersName      string `json:"customersName"`
	CustomersCompany   string `json:"customersCompany"`
	CustomersEmail     string `json:"customersEmail"`
	CustomersTelephone string `json:"customersTelephone"`
	InternalProject    bool   `json:"internalProject"`
	Restricted         bool   `json:"restricted"`
	Favorite           bool   `json:"favorite"`
}

// toDomain takes archived from the CALLER, not from any response field.
func (w wireCase) toDomain(archived bool) Case {
	return Case{
		ID: w.CaseID, Number: w.CaseNumber, Name: w.CaseName,
		Archived:          archived,
		EconomyCaseNumber: w.EconomyCaseNumber,
		Address:           w.Address, Zip: w.Zip, SubText: w.SubText,
		CustomerName: w.CustomersName, CustomerCompany: w.CustomersCompany,
		CustomerEmail: w.CustomersEmail, CustomerPhone: w.CustomersTelephone,
		InternalProject: w.InternalProject, Restricted: w.Restricted, Favorite: w.Favorite,
	}
}

type wireCasesPage struct {
	Cases      []wireCase `json:"cases"`
	TotalCount int        `json:"totalCount"`
}

// wireCaseDetail is the 64-field per-case read. Only the fields this track
// exposes are decoded; financial and hour-registration fields are deliberately
// omitted until FR7's opt-in exists.
type wireCaseDetail struct {
	wireCase
	CustomerID              int64  `json:"customerId"`
	IsFinished              bool   `json:"isFinished"`
	ChecklistItemsTotal     int    `json:"checklistItemsTotal"`
	ChecklistItemsCompleted int    `json:"checklistItemsCompleted"`
	EconomySyncFailed       bool   `json:"economySyncFailed"`
	StartDate               string `json:"startDate"`
	EndDate                 string `json:"endDate"`
	Deadline                string `json:"deadline"`

	Cost                 int `json:"cost"`
	Sales                int `json:"sales"`
	Result               int `json:"result"`
	Invoiced             int `json:"invoiced"`
	Uninvoiced           int `json:"uninvoiced"`
	Realised             int `json:"realised"`
	RegisteredHoursTotal int `json:"registeredHoursTotal"`
	BilledHours          int `json:"billedHours"`
}

// listCasesBody is the request body. finishedJobs is pinned to false: it had no
// observable effect against the live tenant, and a parameter nobody can explain
// must not be exposed as a filter.
type listCasesBody struct {
	FinishedJobs     bool    `json:"finishedJobs"`
	ArchivedJobs     bool    `json:"archivedJobs"`
	SortByName       bool    `json:"sortByName"`
	SortByAddress    *string `json:"sortByAddress"`
	SortByCaseNumber *string `json:"sortByCaseNumber"`
	UnassignedTasks  bool    `json:"unassignedTasks"`
	Page             int     `json:"page"`
	Search           string  `json:"search"`
	Origin           string  `json:"origin"`
	IncludeClis      bool    `json:"includeClis"`
}

// dotNetZeroMillis is 0001-01-01T00:00:00Z as .NET serialises it -- the
// framework's "no value", which must map to nil rather than year 1.
const dotNetZeroMillis = -62135596800000

// parseDotNetDate converts ASP.NET's /Date(milliseconds)/ wire format.
//
// Returns nil for null, empty, and the .NET zero date, which all mean "unset"
// upstream. An unrecognised non-empty value is an error rather than a silent
// nil: writing an unparsed /Date(...)/ into state is a defect every user sees.
func parseDotNetDate(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if !strings.HasPrefix(raw, "/Date(") || !strings.HasSuffix(raw, ")/") {
		return nil, fmt.Errorf("%w: %q is not a .NET /Date(ms)/ value", ErrDecode, raw)
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(raw, "/Date("), ")/")
	// Offsets such as /Date(1788333543793+0200)/ occur in .NET output; the
	// epoch is already UTC, so the offset is presentation only.
	if i := strings.IndexAny(inner[1:], "+-"); i >= 0 {
		inner = inner[:i+1]
	}
	ms, err := strconv.ParseInt(inner, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: %q has an unparseable epoch: %v", ErrDecode, raw, err)
	}
	if ms == dotNetZeroMillis {
		return nil, nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t, nil
}

// ListCases reads one set of cases -- archived or not, per q.Archived.
func (c *internalAPI) ListCases(ctx context.Context, q CaseQuery) (CaseScan, error) {
	pageSize := q.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	maxPages := q.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}

	scan := CaseScan{Cases: make([]Case, 0, pageSize)}

	for page := 0; page < maxPages; page++ {
		body, err := json.Marshal(listCasesBody{
			FinishedJobs: false,
			ArchivedJobs: q.Archived,
			Page:         page,
			Search:       q.Search,
			Origin:       "Other",
		})
		if err != nil {
			return CaseScan{}, fmt.Errorf("kala: building case list request: %w", err)
		}

		raw, err := c.authedRequest(
			ctx, http.MethodPost, "/api/GetAllJobsSimplePaged/", body, contentTypeHeader,
		)
		if err != nil {
			return CaseScan{}, err
		}
		scan.Pages++

		if len(bytes.TrimSpace(raw)) == 0 {
			return scan, nil
		}

		var envelope wireCasesPage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return CaseScan{}, fmt.Errorf("%w: cases response: %v", ErrDecode, err)
		}
		scan.Total = envelope.TotalCount

		for _, wc := range envelope.Cases {
			scan.Cases = append(scan.Cases, wc.toDomain(q.Archived))
		}
		scan.Fetched = len(scan.Cases)

		if len(envelope.Cases) == 0 || scan.Fetched >= scan.Total {
			return scan, nil
		}
	}

	return scan, nil
}

// GetCase reads one case by its case NUMBER (a string such as "KA-1"), not by
// the int caseId that tasks use to link to it.
//
// WARNING: an unknown caseNr returns HTTP 500, so a consistent server error is
// reported as ErrNotFound. That is safe for a data source, which only reads.
// It would NOT be safe for a resource: under TF1.2 a not-found on Read means
// RemoveResource, so a genuine outage would silently drop a live case from
// state. Revisit this mapping before any kala_case resource is built.
//
// The retry policy is what makes it defensible: 5xx is retried, so a transient
// failure recovers on its own and only a CONSISTENT 500 reaches this mapping.
func (c *internalAPI) GetCase(ctx context.Context, caseNumber string) (CaseDetail, error) {
	params := url.Values{}
	params.Set("caseNr", caseNumber)

	raw, err := c.authedRequest(
		ctx, http.MethodGet, "/api/GetJobDetailsAdvanced/?"+params.Encode(), nil, contentTypeHeader,
	)
	if err != nil {
		if errors.Is(err, ErrServer) {
			return CaseDetail{}, fmt.Errorf("%w: no case numbered %q (upstream answers 500, not 404, for an unknown case number)",
				ErrNotFound, caseNumber)
		}
		return CaseDetail{}, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return CaseDetail{}, fmt.Errorf("%w: no case numbered %q", ErrNotFound, caseNumber)
	}

	var wd wireCaseDetail
	if err := json.Unmarshal(raw, &wd); err != nil {
		return CaseDetail{}, fmt.Errorf("%w: case detail response: %v", ErrDecode, err)
	}

	// A case reached through the detail endpoint was addressed directly, so its
	// archive state is not implied by the call. Left false; callers that need it
	// learn it from ListCases.
	detail := CaseDetail{
		Case:                    wd.wireCase.toDomain(false),
		CustomerID:              wd.CustomerID,
		IsFinished:              wd.IsFinished,
		ChecklistItemsTotal:     wd.ChecklistItemsTotal,
		ChecklistItemsCompleted: wd.ChecklistItemsCompleted,
		EconomySyncFailed:       wd.EconomySyncFailed,

		Cost:                 wd.Cost,
		Sales:                wd.Sales,
		Result:               wd.Result,
		Invoiced:             wd.Invoiced,
		Uninvoiced:           wd.Uninvoiced,
		Realised:             wd.Realised,
		RegisteredHoursTotal: wd.RegisteredHoursTotal,
		BilledHours:          wd.BilledHours,
	}

	for _, f := range []struct {
		raw  string
		dst  **time.Time
		name string
	}{
		{wd.StartDate, &detail.StartDate, "startDate"},
		{wd.EndDate, &detail.EndDate, "endDate"},
		{wd.Deadline, &detail.Deadline, "deadline"},
	} {
		parsed, err := parseDotNetDate(f.raw)
		if err != nil {
			return CaseDetail{}, fmt.Errorf("case %q %s: %w", caseNumber, f.name, err)
		}
		*f.dst = parsed
	}

	return detail, nil
}
