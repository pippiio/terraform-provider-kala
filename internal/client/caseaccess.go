// Story: Case access — who is granted access to a case, and who is assigned to it
//
// Kala keeps TWO lists per case, and they are different things:
//
//   - GRANTED: employees allowed to access the case -- Kala's own access list,
//     GET /api/GrantedWorkers/. This is who may register time on a restricted
//     case.
//   - ASSIGNED: employees assigned to at least one of the case's tasks
//     (checklistItems[].workersAssigned in the case detail).
//
// On a restricted case every assigned employee should also be granted, or they
// cannot see the tasks they are assigned to. Case KA-2 was found live on
// 2026-09-29 with that broken -- employee 23 assigned, access revoked -- after
// an earlier version of this file had reported the ASSIGNED set as access and
// so kept showing 23 as having it.
//
// Input:  a case NUMBER (string, e.g. "KA-1").
// Process:
//   1. GET /api/GetJobDetailsAdvanced/?caseNr= -- caseId, restricted,
//      checklistItemsTotal, and checklistItems[].workersAssigned[]. This read
//      also establishes that the case exists; if it fails, stop.
//   2. Decode it with a NARROW wire type: those four things only. The wide
//      wireCaseDetail carries commercial figures, and the worker element carries
//      names and phone numbers; neither is needed, so neither is decoded. It
//      also made this read immune to the decimal decode bug that broke GetCase
//      and ListTasks until 2026-09-27.
//   3. Verify len(checklistItems) against checklistItemsTotal. A mismatch is an
//      error naming both numbers, never a short set.
//   4. GET /api/GrantedWorkers/?caseNr= -- {"grantedWorkers":[<workerNr>...]}.
//      Plain numbers: no personal data on this path at all.
//   5. An unreadable access list is an ERROR, never an empty list: on a
//      restricted case an empty list says nobody has access.
// Output: CaseAccess{CaseID, Restricted, Assigned, Granted} -- each list unique
//         and sorted.
//
// Dependencies: internalAPI.authedRequest, ErrNotFound, ErrServer, ErrDecode.
// Side effects: outbound HTTPS reads only. Nothing is written. (Kala's write for
//               this list, POST /api/GrantAccess/, is not used here.)

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
)

// CaseAccess reports, for one case, who is granted access and who is assigned.
//
// Assigned carries `workerNr` values, which the provider surface spells
// `employee_number` -- medarbejderNr, workerNr and webapiv2's employeeNumber are
// one value (ARCH1.9). Identifiers only: the upstream collection also holds
// names, phone numbers, titles and image URLs, none of which cross this
// boundary.
type CaseAccess struct {
	CaseID     int64
	Restricted bool

	// Assigned is deduplicated and sorted ascending. Sorted in the
	// client rather than left to the caller so the contract is deterministic:
	// the provider layer turns it into an unordered set, but a client returning
	// arbitrary order would make its own tests flaky.
	Assigned []int64

	// Granted is Kala's own access list for the case, from
	// GET /api/GrantedWorkers/. Deduplicated and sorted ascending.
	Granted []int64
}

// wireCaseAccess is the NARROW decode of /api/GetJobDetailsAdvanced/.
//
// It declares four things out of the endpoint's 64 fields, and the omissions are
// deliberate. This read needs identifiers and a count, so it decodes nothing
// else: no commercial figure, and -- via wireCaseAccessWorker -- no personal
// field.
//
// Kala returns DECIMALS for hours and money. Until 2026-09-27 wireCaseDetail
// declared them int and failed outright on such a case; this type, declaring
// none of them, was immune. That is fixed, but the rule stands: DO NOT add a
// numeric field here that is not an identifier or a count, and if you must,
// make it float64.
type wireCaseAccess struct {
	CaseID              int64                `json:"caseId"`
	Restricted          bool                 `json:"restricted"`
	ChecklistItemsTotal int                  `json:"checklistItemsTotal"`
	ChecklistItems      []wireCaseAccessItem `json:"checklistItems"`
}

type wireCaseAccessItem struct {
	WorkersAssigned []wireCaseAccessWorker `json:"workersAssigned"`
}

// wireCaseAccessWorker takes the identifier and nothing else. Upstream this
// element also carries name, phone, title, initials and workerImage; not
// declaring them is what stops personal data crossing the boundary (SEC1.5).
type wireCaseAccessWorker struct {
	WorkerNr int64 `json:"workerNr"`
}

// wireGrantedWorkers is GET /api/GrantedWorkers/, observed 2026-09-29 as
// {"grantedWorkers":[<workerNr>...],"rolesEnabled":<bool>}.
//
// A POINTER, so an absent key is distinguishable from an empty list. If Kala
// ever renamed the key, a plain slice would decode to empty and report that
// nobody has access; this way it is an error. rolesEnabled is not declared:
// nothing here uses it.
type wireGrantedWorkers struct {
	GrantedWorkers *[]int64 `json:"grantedWorkers"`
}

// GetCaseAccess reads, for one case by its case NUMBER, who is granted access and
// who is assigned to its tasks.
func (c *internalAPI) GetCaseAccess(ctx context.Context, caseNumber string) (CaseAccess, error) {
	params := url.Values{}
	params.Set("caseNr", caseNumber)

	raw, err := c.authedRequest(
		ctx, http.MethodGet, "/api/GetJobDetailsAdvanced/?"+params.Encode(), nil, contentTypeHeader)
	if err != nil {
		// An unknown caseNr answers 500, so absence and an outage are
		// indistinguishable here. Reporting not-found is the useful default --
		// a typo is far likelier than a sustained fault, and the retry policy
		// has already absorbed the transient case -- but the MESSAGE must not
		// state absence as fact, or an outage sends the operator hunting for a
		// typo that is not there.
		if errors.Is(err, ErrServer) {
			return CaseAccess{}, fmt.Errorf(
				"%w: no case numbered %q, or Kala is failing: this endpoint answers 500 for an "+
					"unknown case number, so the two cannot be told apart from the response alone",
				ErrNotFound, caseNumber)
		}
		return CaseAccess{}, err
	}

	// Kala answers some unknown identifiers with HTTP 200 and no body at all.
	// That is absence, not a malformed response, and calling it a decode error
	// would report a bug where there is only a wrong case number.
	if len(bytes.TrimSpace(raw)) == 0 {
		return CaseAccess{}, fmt.Errorf("%w: no case numbered %q", ErrNotFound, caseNumber)
	}

	var wire wireCaseAccess
	if err := json.Unmarshal(raw, &wire); err != nil {
		return CaseAccess{}, fmt.Errorf("%w: case access response: %v", ErrDecode, err)
	}

	// The only incompleteness this read can detect. The payload states how many
	// items the case has, so a truncated checklistItems is observable -- and it
	// must be an error, because a short grant is indistinguishable from a case
	// fewer people are assigned to.
	//
	// This is NOT the limitation described at the top of this file. That one --
	// an employee granted access with no task at all -- leaves checklistItems
	// complete and the grant wrong, and nothing here can see it.
	if got, want := len(wire.ChecklistItems), wire.ChecklistItemsTotal; got != want {
		return CaseAccess{}, fmt.Errorf(
			"kala: case %q returned %d of %d checklist items, so which employees may register "+
				"time on it cannot be established; refusing to report a partial grant",
			caseNumber, got, want)
	}

	// A job link is shared per (worker, case), so an employee assigned to five
	// of the case's items appears five times here.
	var assigned []int64
	for _, item := range wire.ChecklistItems {
		for _, w := range item.WorkersAssigned {
			assigned = append(assigned, w.WorkerNr)
		}
	}

	// Only now, with the case known to exist, ask for its access list.
	granted, err := c.grantedWorkers(ctx, caseNumber)
	if err != nil {
		return CaseAccess{}, err
	}

	return CaseAccess{
		CaseID:     wire.CaseID,
		Restricted: wire.Restricted,
		Assigned:   uniqueSorted(assigned),
		Granted:    uniqueSorted(granted),
	}, nil
}

// grantedWorkers reads Kala's access list for a case that is already known to
// exist. Every way of failing to read it is an error, and none is not-found: an
// empty list would state that nobody has access.
func (c *internalAPI) grantedWorkers(ctx context.Context, caseNumber string) ([]int64, error) {
	params := url.Values{}
	params.Set("caseNr", caseNumber)

	raw, err := c.authedRequest(
		ctx, http.MethodGet, "/api/GrantedWorkers/?"+params.Encode(), nil, contentTypeHeader)
	if err != nil {
		// Deliberately NOT translated to ErrNotFound, unlike the detail read:
		// the case exists, so a 500 here is Kala failing, not absence.
		return nil, fmt.Errorf("kala: reading the access list of case %q: %w", caseNumber, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%w: case %q returned an empty access-list response; "+
			"refusing to report that nobody has access", ErrDecode, caseNumber)
	}

	var wire wireGrantedWorkers
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("%w: access list of case %q: %v", ErrDecode, caseNumber, err)
	}
	if wire.GrantedWorkers == nil {
		return nil, fmt.Errorf("%w: access list of case %q has no grantedWorkers key; "+
			"refusing to report that nobody has access", ErrDecode, caseNumber)
	}
	return *wire.GrantedWorkers, nil
}

// uniqueSorted deduplicates and sorts ascending. Sorted in the client rather
// than left to the caller so its contract is deterministic: the provider turns
// these into unordered sets, but arbitrary order would make tests flaky.
func uniqueSorted(in []int64) []int64 {
	seen := make(map[int64]struct{}, len(in))
	out := make([]int64, 0, len(in))
	for _, v := range in {
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
