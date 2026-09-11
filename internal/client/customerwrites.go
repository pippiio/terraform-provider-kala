// Story: Customer write path (internal app API)
//
// Input:  a CustomerInput describing the desired record, plus an id for edits.
// Process:
//  1. Create via POST /api/AddCustomer/, which allocates and RETURNS
//     {"status":"Success","customerId":N}. Returning the id is what makes a
//     partial create recoverable: the caller can record state before any
//     follow-up call runs.
//  2. Send the FULL configured field set on create. Which fields create
//     accepts is not trustworthy by inference -- an earlier claim that
//     AddCustomer rejects cvr was drawn from one captured payload's omission
//     and was wrong.
//  3. Verify by read-back and, where a configured value did not
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
//               create here is permanent.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

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

// wireCustomerWrite is the body both write endpoints take.
//
// Every field is present on every call and none is `omitempty`. That is
// deliberate: EditCustomer is a FULL-RECORD REPLACE, so a field dropped from
// the JSON is blanked upstream rather than left alone. omitempty here would
// silently delete any value the operator cleared.
//
// CustomerID is the one exception -- AddCustomer allocates it, so it is
// omitted on create.
type wireCustomerWrite struct {
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	Company     string `json:"company"`
	Phone       string `json:"phone"`
	Address     string `json:"address"`
	Zip         string `json:"zip"`
	Email       string `json:"email"`
	CVR         string `json:"cvr"`
	Description string `json:"description"`
	EAN         string `json:"ean"`

	// AdditionalFields is Kala's own provenance stamp. It is echoed back
	// unchanged rather than synthesised: the web app preserves it on edit, and
	// overwriting it would discard how a record was originally created.
	AdditionalFields any `json:"additionalFields"`

	CustomerID *int64 `json:"customerId,omitempty"`
}

func writeBodyFor(in CustomerInput, id *int64) wireCustomerWrite {
	return wireCustomerWrite{ //nolint:staticcheck // S1016: explicit mapping is intentional at the wire/domain boundary
		FirstName: in.FirstName, LastName: in.LastName, Company: in.Company,
		Phone: in.Phone, Address: in.Address, Zip: in.Zip, Email: in.Email,
		CVR: in.CVR, Description: in.Description, EAN: in.EAN,
		CustomerID: id,
	}
}

// matches reports whether a persisted record carries everything that was asked
// for. Only non-empty inputs are compared: a caller clearing a field cannot be
// distinguished from one leaving it unset at this layer, and treating an empty
// input as a required blank would fail verification against records the
// endpoint legitimately defaulted.
func (in CustomerInput) matches(got Customer) bool {
	for _, p := range [][2]string{
		{in.FirstName, got.FirstName}, {in.LastName, got.LastName},
		{in.Company, got.Company}, {in.Phone, got.Phone},
		{in.Address, got.Address}, {in.Zip, got.Zip},
		{in.Email, got.Email}, {in.CVR, got.CVR},
		{in.Description, got.Description}, {in.EAN, got.EAN},
	} {
		if p[0] != "" && p[0] != p[1] {
			return false
		}
	}
	return true
}

// AddCustomer creates a customer and returns it as Kala persisted it.
//
// On a read-back failure the returned Customer still carries the allocated ID,
// alongside the error. That is not defensive tidiness: Kala has no delete, so a
// caller that discards the id has stranded a real record permanently.
func (c *internalAPI) AddCustomer(ctx context.Context, in CustomerInput) (Customer, error) {
	body, err := json.Marshal(writeBodyFor(in, nil))
	if err != nil {
		return Customer{}, fmt.Errorf("kala: encoding customer: %w", err)
	}

	raw, err := c.authedRequest(ctx, http.MethodPost, "/api/AddCustomer/", body, contentTypeHeader)
	if err != nil {
		return Customer{}, err
	}

	var resp struct {
		CustomerID int64 `json:"customerId"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Customer{}, fmt.Errorf("%w: AddCustomer response: %v", ErrDecode, err)
	}
	if resp.CustomerID == 0 {
		return Customer{}, fmt.Errorf(
			"kala: AddCustomer reported success but returned no customerId, so the new record cannot be identified")
	}

	got, err := c.GetCustomer(ctx, resp.CustomerID)
	if err != nil {
		return Customer{ID: resp.CustomerID}, fmt.Errorf(
			"kala: customer %d was created but could not be read back: %w", resp.CustomerID, err)
	}

	// Which fields create accepts is not trustworthy by inference, so anything
	// that did not land is converged by an edit
	// rather than assumed unsupported or reported as a failure.
	if !in.matches(got) {
		return c.EditCustomer(ctx, resp.CustomerID, in)
	}
	return got, nil
}

// EditCustomer replaces a customer record and verifies the result.
//
// FULL-RECORD REPLACE: callers must pass everything they intend the record to
// hold, not just what changed.
func (c *internalAPI) EditCustomer(ctx context.Context, id int64, in CustomerInput) (Customer, error) {
	body, err := json.Marshal(writeBodyFor(in, &id))
	if err != nil {
		return Customer{}, fmt.Errorf("kala: encoding customer %d: %w", id, err)
	}

	if _, err := c.authedRequest(
		ctx, http.MethodPost, "/api/EditCustomer/", body, contentTypeHeader,
	); err != nil {
		return Customer{}, err
	}

	got, err := c.GetCustomer(ctx, id)
	if err != nil {
		return Customer{ID: id}, fmt.Errorf("kala: customer %d could not be read back: %w", id, err)
	}
	if !in.matches(got) {
		return Customer{ID: id}, fmt.Errorf(
			"kala: customer %d reported a successful update but the record did not change; "+
				"the write was not applied", id)
	}
	return got, nil
}

// GetCustomer reads one customer by id.
//
// Kala exposes no by-id customer endpoint, so this selects from a list read.
// The distinction that matters: a miss inside an INCOMPLETE read means the
// lookup could not be completed, NOT that the record is absent. Reporting
// ErrNotFound there would tell a caller a customer does not exist on the
// strength of a read that never reached the end of the data.
func (c *internalAPI) GetCustomer(ctx context.Context, id int64) (Customer, error) {
	scan, err := c.ListCustomers(ctx, CustomerQuery{})
	if err != nil {
		return Customer{}, err
	}
	for _, cust := range scan.Customers {
		if cust.ID == id {
			return cust, nil
		}
	}
	if !scan.Complete() {
		return Customer{}, fmt.Errorf(
			"kala: customer %d was not in a read covering %d of %d records, so its absence is "+
				"unproven; raise page_size and retry", id, scan.Fetched, scan.Total)
	}
	return Customer{}, fmt.Errorf("kala: customer %d: %w", id, ErrNotFound)
}
