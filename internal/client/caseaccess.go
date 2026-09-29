// Story: Case access — who may register time on a case
//
// Input:  a case NUMBER (string, e.g. "KA-1") — the identifier every case write
//         keys on and the one kala_case exposes as `number`.
// Process:
//   1. GET /api/GetJobDetailsAdvanced/?caseNr= ONCE. That single response
//      carries everything needed: caseId, restricted, checklistItemsTotal, and
//      checklistItems[] where each item carries workersAssigned[].
//   2. Decode with a NARROW wire type declaring only those four things.
//      Deliberately NOT wireCaseDetail and NOT ListTasks: both declare Kala
//      numerics as int, and Kala returns decimals -- registeredHoursTotal was
//      observed as 0.25 -- so both are broken decodes for such a case.
//      encoding/json ignores absent fields, which makes a narrow type immune.
//      Verified against the failing case before this file existed.
//   3. Verify completeness: len(checklistItems) against checklistItemsTotal.
//      A mismatch is an error naming BOTH numbers, never a short set. This is
//      the only incompleteness that can be detected at all.
//   4. Union workersAssigned[].workerNr across items into a deduplicated,
//      sorted set. A worker on five items appears five times upstream.
//   5. Carry identifiers ONLY. The upstream collection also holds name, phone,
//      title and image; they are dropped HERE, at the boundary, so they cannot
//      reach Terraform state or a log line (SEC1.5).
//
// Output: CaseAccess{CaseID, Restricted, Assigned} -- unique and sorted.
//
// WHAT THIS CANNOT SEE, and it is not a defect awaiting a fix:
//
//	Kala exposes no endpoint reporting who may register time on a case. The
//	grant is observable only through task assignment, so an employee granted
//	access with no task on the case is INVISIBLE, and a case with no tasks
//	reports empty however many people hold access. Two of the four cases in the
//	development tenant have zero tasks, so this is the common case, not a
//	corner. No assertion can detect it. The entire mitigation lives in the
//	provider layer's attribute descriptions -- read the appendix in
//	draft/tracks/case-time-registration-access/spike-findings.md before changing
//	any of that wording.
//
// Dependencies: internalAPI.authedRequest, ErrNotFound, ErrServer, ErrDecode.
// Side effects: outbound HTTPS read only. Nothing is written.

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

// CaseAccess reports which employees are granted access to one case.
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
}

// wireCaseAccess is the NARROW decode of /api/GetJobDetailsAdvanced/.
//
// It declares four things out of the endpoint's 64 fields, and the omissions are
// load-bearing rather than tidiness. wireCaseDetail declares Kala's numerics as
// Go int -- registeredHoursTotal, billedHours, cost, sales and the rest -- while
// Kala returns decimals: 0.25 was observed on registeredHoursTotal. Decoding
// into an int field is a hard failure, so wireCaseDetail cannot read such a
// case at all.
//
// encoding/json ignores keys absent from the target struct, so declaring none of
// those fields makes this type immune. Verified against the failing case before
// this file existed. DO NOT add a numeric field here without making it float64.
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

// GetCaseAccess reads the access grant for one case by its case NUMBER.
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

	// A job link is shared per (worker, case), so a worker assigned to five of
	// the case's items appears five times here. Dedupe, then sort: the provider
	// layer turns this into an unordered set, but an arbitrary order would make
	// this package's own tests flaky.
	seen := make(map[int64]struct{}, len(wire.ChecklistItems))
	for _, item := range wire.ChecklistItems {
		for _, w := range item.WorkersAssigned {
			seen[w.WorkerNr] = struct{}{}
		}
	}
	numbers := make([]int64, 0, len(seen))
	for nr := range seen {
		numbers = append(numbers, nr)
	}
	sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })

	return CaseAccess{
		CaseID:     wire.CaseID,
		Restricted: wire.Restricted,
		Assigned:   numbers,
	}, nil
}
