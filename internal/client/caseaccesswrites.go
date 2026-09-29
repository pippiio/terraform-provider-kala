// Story: Granting and revoking case access
//
// Input:  a case NUMBER, an employee's workerNr, and whether they should be
//         granted access.
// Process:
//   1. POST /api/GrantAccess/ with {"workerNumber","caseNr","access","role":[]}
//      -- the body the Kala UI sends, captured 2026-09-29. role is always an
//      empty array: roles are not managed by this provider.
//   2. A refused write answers HTTP 200 with {"status":"Error",...}; the shared
//      request path turns that into an error.
//   3. VERIFY by read-back through GET /api/GrantedWorkers/ (ARCH1.8): the
//      employee must be present after a grant and absent after a revoke. The
//      response to GrantAccess itself has not been characterised and is not
//      trusted.
// Output: nil, or an error naming what could not be confirmed.
//
// Dependencies: internalAPI.authedRequest, grantedWorkers.
// Side effects: CHANGES WHO MAY ACCESS A REAL CASE.

package client

import "context"

// SetCaseAccess grants or revokes one employee's access to one case, and
// verifies the result by reading Kala's access list back.
func (c *internalAPI) SetCaseAccess(ctx context.Context, caseNumber string, workerNr int64, granted bool) error {
	_, _, _, _ = ctx, caseNumber, workerNr, granted
	return nil // TODO(task 1.2)
}
