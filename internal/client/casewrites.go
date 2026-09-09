// Story: Case creation, archival, and customer reassignment
//
// Input:  a NewCase, or a case number plus the change wanted.
// Process:
//   1. Create via POST /Api/CreateCase/ -- note the CAPITAL Api, unlike every
//      other case endpoint. Returns caseId and caseNumber, so a partial create
//      is recoverable.
//   2. Build the body by VARIANT, not by blanking: an internal project omits
//      the customer block entirely rather than sending it empty.
//   3. Never send newCustomer:true or updateCustomerAddress:true. Both make a
//      write change something the operator did not declare.
//   4. Archive via GET /api/ArchiveCase/ -- a write performed by GET -- and
//      verify by set membership, since `archived` is not a response field.
//
// Output: the created case, or nothing on success for the mutations.
//
// Dependencies: internalAPI.authedRequest, ListCases, GetCase.
// Side effects: CREATES AND MUTATES REAL CASES. Kala has no delete.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// NewCase describes a case to create.
//
// CustomerNumber is empty exactly when InternalProject is true; the two request
// shapes are built separately rather than by nulling fields in one struct.
type NewCase struct {
	Name            string
	WorkerNr        int64
	CustomerNumber  string
	InternalProject bool
	Address         string
	Zip             string
}

// CreateCase creates a case and returns it with the identity Kala allocated.
func (c *internalAPI) CreateCase(ctx context.Context, in NewCase) (CaseDetail, error) {
	// Built as a map rather than a struct because the two shapes differ by
	// OMISSION: an internal project carries no customer block at all, and
	// sending it empty is not the same request. omitempty could not express
	// this -- an empty customerNr on a customer-facing case is still a
	// different thing from no customerNr.
	body := map[string]any{
		"caseName":         in.Name,
		"workerNr":         in.WorkerNr,
		"internalProject":  in.InternalProject,
		"caseAddress":      in.Address,
		"mandatoryRoles":   "[]",
		"metadata":         nil,
		"customerMetadata": nil,

		// NEVER true. With newCustomer:true this endpoint creates a customer as
		// a side effect of creating a case -- a record no configuration
		// declared and that Kala cannot delete.
		"newCustomer": false,
	}
	if !in.InternalProject {
		body["customerNr"] = in.CustomerNumber
		body["caseZip"] = in.Zip
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return CaseDetail{}, fmt.Errorf("kala: encoding case %q: %w", in.Name, err)
	}

	// CAPITAL Api, alone among the case endpoints.
	resp, err := c.authedRequest(ctx, http.MethodPost, "/Api/CreateCase/", raw, contentTypeHeader)
	if err != nil {
		return CaseDetail{}, err
	}

	// Only the identity is taken from the create response. The full record then
	// comes from GetCase, which both avoids duplicating that mapping and
	// verifies the write by read-back.
	var allocated struct {
		CaseID     int64  `json:"caseId"`
		CaseNumber string `json:"caseNumber"`
	}
	if err := json.Unmarshal(resp, &allocated); err != nil {
		return CaseDetail{}, fmt.Errorf("%w: CreateCase response: %v", ErrDecode, err)
	}
	if allocated.CaseNumber == "" {
		return CaseDetail{}, fmt.Errorf(
			"kala: CreateCase reported success but returned no case number, so the new case " +
				"cannot be identified")
	}

	detail, err := c.GetCase(ctx, allocated.CaseNumber)
	if err != nil {
		// The case exists. Kala has no delete, so the identity travels with the
		// error rather than being discarded -- exactly as AddCustomer
		// returns its id on a failed read-back.
		return CaseDetail{Case: Case{ID: allocated.CaseID, Number: allocated.CaseNumber}},
			fmt.Errorf("kala: case %s was created but could not be read back: %w",
				allocated.CaseNumber, err)
	}
	return detail, nil
}

// SetCaseArchived archives or unarchives a case.
//
// A WRITE PERFORMED BY GET, with query parameters -- the verb carries no safety
// meaning on this API.
func (c *internalAPI) SetCaseArchived(ctx context.Context, caseNumber string, archived bool) error {
	q := url.Values{}
	q.Set("archive", strconv.FormatBool(archived))
	q.Set("caseNr", caseNumber)

	if _, err := c.authedRequest(
		ctx, http.MethodGet, "/api/ArchiveCase/?"+q.Encode(), nil, contentTypeHeader,
	); err != nil {
		return fmt.Errorf("kala: setting archived=%t on case %s: %w", archived, caseNumber, err)
	}

	// `archived` is not a response field: it is derived from WHICH SET a case
	// appears in, and the two sets are disjoint. Verification therefore means
	// confirming the case moved.
	scan, err := c.ListCases(ctx, CaseQuery{Archived: archived})
	if err != nil {
		return fmt.Errorf("kala: case %s could not be read back after archiving: %w", caseNumber, err)
	}
	for _, k := range scan.Cases {
		if k.Number == caseNumber {
			return nil
		}
	}
	return fmt.Errorf(
		"kala: case %s reported a successful archive change but does not appear in the %s set; "+
			"the write was not applied", caseNumber, archivedSetName(archived))
}

func archivedSetName(archived bool) string {
	if archived {
		return "archived"
	}
	return "active"
}

// SetCaseCustomer reassigns a case's customer, or converts it to an internal
// project.
//
// The identifier key changes with DIRECTION: newCustomerId when assigning a
// customer, oldCustomerId when converting to internal. Same endpoint, same
// relationship, two names -- encoded, not derived.
func (c *internalAPI) SetCaseCustomer(
	ctx context.Context, caseNumber string, customerID int64, internalProject bool,
) error {
	body := map[string]any{
		"caseNr":          caseNumber,
		"internalProject": internalProject,

		// NEVER true. It would overwrite the case address as a side effect of
		// changing the customer -- a change the plan never mentioned, which
		// would then surface as drift. A write must never change something the
		// operator did not declare.
		"updateCustomerAddress": false,
	}
	if internalProject {
		body["oldCustomerId"] = customerID
	} else {
		body["newCustomerId"] = customerID
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("kala: encoding customer change for case %s: %w", caseNumber, err)
	}
	if _, err := c.authedRequest(
		ctx, http.MethodPost, "/api/ChangeCaseCustomer/", raw, contentTypeHeader,
	); err != nil {
		return fmt.Errorf("kala: changing the customer on case %s: %w", caseNumber, err)
	}
	return nil
}
