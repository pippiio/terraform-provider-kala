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
//   4. Verify by reading the case back. HTTP 200 is a claim.
//
// Output: nothing on success; ErrConflict when the record changed underneath.
//
// Dependencies: internalAPI.authedRequest, GetCase.
// Side effects: mutates a real case.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

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

// caseFieldSpec describes one case field-setting endpoint.
//
// Every property here was observed rather than predicted. Three endpoints
// spell the previous-value key "previous<Thing>"; RenameCaseCustomerPhoneNumber
// spells it "oldPhoneNumber". All four key the case on the STRING caseNr,
// while the read path keys on the integer caseId. Each difference fails
// silently, which is why they are a table rather than a rule.
type caseFieldSpec struct {
	endpoint string
	prevKey  string
	newKey   string

	// current returns the field's value from a freshly read case, used both as
	// the previous value on the write and as the read-back check afterwards.
	current func(CaseDetail) string
}

var caseFieldSpecs = map[CaseField]caseFieldSpec{
	CaseFieldName: {
		endpoint: "/api/RenameCase/", prevKey: "previousCaseName", newKey: "newCaseName",
		current: func(c CaseDetail) string { return c.Name },
	},
	CaseFieldAddress: {
		endpoint: "/api/ChangeCaseAddress/", prevKey: "previousAddress", newKey: "newAddress",
		current: func(c CaseDetail) string { return c.Address },
	},
	CaseFieldZip: {
		endpoint: "/api/ChangeCaseZip/", prevKey: "previousZip", newKey: "newZip",
		current: func(c CaseDetail) string { return c.Zip },
	},
	CaseFieldContactPhone: {
		endpoint: "/api/RenameCaseCustomerPhoneNumber/",
		// "old", not "previous" -- alone among the four.
		prevKey: "oldPhoneNumber", newKey: "newPhoneNumber",
		current: func(c CaseDetail) string { return c.CustomerPhone },
	},
}

// SetCaseField sets one field on a case and verifies the result.
func (c *internalAPI) SetCaseField(
	ctx context.Context, caseNumber string, field CaseField, value string,
) error {
	spec, ok := caseFieldSpecs[field]
	if !ok {
		return fmt.Errorf("kala: %q is not a settable case field", field)
	}

	// Read first. Kala validates the previous value, so it must be current --
	// a value remembered from Terraform state is stale after external drift
	// and always after import, which is exactly when it would be refused.
	before, err := c.GetCase(ctx, caseNumber)
	if err != nil {
		return fmt.Errorf("kala: case %s could not be read before writing %s: %w",
			caseNumber, field, err)
	}

	if spec.current(before) == value {
		// Nothing to do. Against a compare-and-swap endpoint a no-op write is
		// not merely wasted -- it is a race that can only lose.
		return nil
	}

	body, err := json.Marshal(map[string]string{
		"caseNr":     caseNumber,
		spec.prevKey: spec.current(before),
		spec.newKey:  value,
	})
	if err != nil {
		return fmt.Errorf("kala: encoding %s for case %s: %w", field, caseNumber, err)
	}

	if _, err := c.authedRequest(
		ctx, http.MethodPost, spec.endpoint, body, contentTypeHeader,
	); err != nil {
		// ErrConflict travels up unwrapped enough for errors.Is: the caller
		// needs to tell "someone else changed this" from "the server broke".
		return fmt.Errorf("kala: setting %s on case %s: %w", field, caseNumber, err)
	}

	after, err := c.GetCase(ctx, caseNumber)
	if err != nil {
		return fmt.Errorf("kala: case %s could not be read back after writing %s: %w",
			caseNumber, field, err)
	}
	if got := spec.current(after); got != value {
		return fmt.Errorf(
			"kala: case %s reported a successful %s change but still holds %q, not %q; "+
				"the write was not applied", caseNumber, field, got, value)
	}
	return nil
}
