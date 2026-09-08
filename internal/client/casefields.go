// Story: Case field writes (internal app API)
//
// Input:  a case number, which field to set, and the value wanted.
// Process:
//   1. READ the case first. Every setter carries the value it expects to
//      overwrite, and Kala VALIDATES it (verified 2026-09-08). Terraform state
//      is stale after external drift and always after import, so the previous
//      value must come from a fresh read rather than from memory.
//   2. Skip the write when the value already matches -- a no-op write is a
//      request that can only fail.
//   3. Write, using this endpoint's own path, identifier key, and previous-key
//      spelling. There is no rule here, only a table.
//   4. Verify by reading the case back (ARCH1.8). HTTP 200 is a claim.
//
// Output: nothing on success; ErrConflict when the record changed underneath.
//
// Dependencies: internalAPI.authedRequest, GetCase.
// Side effects: mutates a real case.

package client

import "context"

// CaseField identifies a settable field on a case.
type CaseField string

const (
	CaseFieldName    CaseField = "name"
	CaseFieldAddress CaseField = "address"
	CaseFieldZip     CaseField = "zip"

	// CaseFieldContactPhone is the phone number for THIS CASE's contact --
	// the person to call about this job. It is NOT the customer's phone:
	// verified 2026-09-08 that writing it leaves the customer record
	// untouched. Every name involved says "customer" and every one misleads.
	CaseFieldContactPhone CaseField = "contact_phone"
)

// --- stubs (RED) -----------------------------------------------------------

func (c *internalAPI) SetCaseField(_ context.Context, _ string, _ CaseField, _ string) error {
	return nil
}
