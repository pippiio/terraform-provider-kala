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

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
)

// wireGrantAccessRequest is the body the Kala UI sends to /api/GrantAccess/.
// Role is always empty and must marshal as [] rather than null.
type wireGrantAccessRequest struct {
	WorkerNumber int64    `json:"workerNumber"`
	CaseNr       string   `json:"caseNr"`
	Access       bool     `json:"access"`
	Role         []string `json:"role"`
}

// SetCaseAccess grants or revokes one employee's access to one case, and
// verifies the result by reading Kala's access list back.
func (c *internalAPI) SetCaseAccess(ctx context.Context, caseNumber string, workerNr int64, granted bool) error {
	verb := "revoking"
	if granted {
		verb = "granting"
	}

	body, err := json.Marshal(wireGrantAccessRequest{
		WorkerNumber: workerNr, CaseNr: caseNumber, Access: granted, Role: []string{},
	})
	if err != nil {
		return fmt.Errorf("kala: encoding the access change for employee %d on case %q: %w", workerNr, caseNumber, err)
	}

	// A refusal (HTTP 200 + {"status":"Error"}) is already an error here: the
	// shared request path applies errorEnvelope. Nothing is read back after
	// one, so a stale list cannot make a refused write look confirmed.
	if _, err := c.authedRequest(ctx, http.MethodPost, "/api/GrantAccess/", body, contentTypeHeader); err != nil {
		return fmt.Errorf("kala: %s employee %d access to case %q: %w", verb, workerNr, caseNumber, err)
	}

	// ARCH1.8: the response to GrantAccess has not been characterised, so only
	// the access list is evidence that the change landed.
	list, err := c.grantedWorkers(ctx, caseNumber)
	if err != nil {
		return fmt.Errorf("kala: %s employee %d access to case %q was sent but could not be verified: %w",
			verb, workerNr, caseNumber, err)
	}
	if slices.Contains(list, workerNr) != granted {
		return fmt.Errorf("kala: %s employee %d access to case %q was not confirmed: "+
			"the access list reads %v after the write", verb, workerNr, caseNumber, list)
	}
	return nil
}
