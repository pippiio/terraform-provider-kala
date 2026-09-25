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
// Output: CaseAccess{CaseID, Restricted, EmployeeNumbers} -- unique and sorted.
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
	"context"
)

// CaseAccess reports which employees are granted access to one case.
//
// EmployeeNumbers carries `workerNr` values, which the provider surface spells
// `employee_number` -- medarbejderNr, workerNr and webapiv2's employeeNumber are
// one value (ARCH1.9). Identifiers only: the upstream collection also holds
// names, phone numbers, titles and image URLs, none of which cross this
// boundary.
type CaseAccess struct {
	CaseID     int64
	Restricted bool

	// EmployeeNumbers is deduplicated and sorted ascending. Sorted in the
	// client rather than left to the caller so the contract is deterministic:
	// the provider layer turns it into an unordered set, but a client returning
	// arbitrary order would make its own tests flaky.
	EmployeeNumbers []int64
}

// GetCaseAccess reads the access grant for one case by its case NUMBER.
func (c *internalAPI) GetCaseAccess(ctx context.Context, caseNumber string) (CaseAccess, error) {
	_ = ctx
	_ = caseNumber
	return CaseAccess{}, nil // TODO(task 2.3): implement
}
