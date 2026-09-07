// Story: Customer write path (internal app API)
//
// Input:  a CustomerInput describing the desired record, plus an id for edits.
// Process:
//  1. Create via POST /api/AddCustomer/, which allocates and RETURNS
//     {"status":"Success","customerId":N}. Returning the id is what makes a
//     partial create recoverable: the caller can record state before any
//     follow-up call runs (FR5).
//  2. Send the FULL configured field set on create. Which fields create
//     accepts is not trustworthy by inference -- an earlier claim that
//     AddCustomer rejects cvr was drawn from one captured payload's omission
//     and was wrong (ADR-003 constraint 4).
//  3. Verify by read-back (ARCH1.8) and, where a configured value did not
//     persist, converge with EditCustomer.
//  4. Update via POST /api/EditCustomer/, which is a FULL-RECORD REPLACE:
//     every field travels on every call, so an omitted field is BLANKED.
//     Callers must read-modify-write rather than send only what they manage.
//  5. Read one customer by selecting from the list read -- Kala exposes no
//     by-id customer endpoint, so absence from an INCOMPLETE read proves
//     nothing and must not be reported as not-found.
//
// Output: the persisted Customer, carrying the identity Kala allocated.
//
// Dependencies: internalAPI.authedRequest, ListCustomers, ErrDecode, ErrNotFound.
// Side effects: CREATES AND MUTATES REAL RECORDS. Kala has no delete, so every
//               create here is permanent (ADR-003).

package client

import "context"

// CustomerInput is the writable surface of a customer.
//
// City, Number, ID, and CaseCount are absent deliberately: none appears in
// either write body, so all four are read-only.
type CustomerInput struct {
	FirstName   string
	LastName    string
	Company     string
	Phone       string
	Address     string
	Zip         string
	Email       string
	CVR         string
	Description string
	EAN         string
}

// --- stubs (RED) -----------------------------------------------------------

func (c *internalAPI) AddCustomer(_ context.Context, _ CustomerInput) (Customer, error) {
	return Customer{}, nil
}

func (c *internalAPI) EditCustomer(_ context.Context, _ int64, _ CustomerInput) (Customer, error) {
	return Customer{}, nil
}

func (c *internalAPI) GetCustomer(_ context.Context, _ int64) (Customer, error) {
	return Customer{}, nil
}
