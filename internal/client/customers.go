// Story: Customer read path (internal app API)
//
// Input:  ctx, CustomerQuery{Search string, PageSize int, MaxPages int}
//
// Process:
//   1. Request one page of customers from the internal app API:
//      GET /api/GetCustomersPaged2/?page=N&pageSize=M&query=S
//      Session auth and the kacompany header are already handled by
//      authedRequest, so nothing here touches credentials.
//   2. Treat an empty body as zero customers, not as an error. An empty 200
//      means "no records" on a LIST read; it means "not found" only on a
//      single-record read. Conflating the two is the bug decodeEmployee and
//      decodeEmployeeList exist separately to avoid (observed 2026-09-01).
//   3. Decode the {customers[], totalCount} envelope into unexported wire
//      types, then map each record onto a domain Customer. Upstream naming
//      stops at this file: firstName, lastName, and address never appear above
//      it (ARCH1.4).
//   4. Carry BOTH identifiers as the API gives them: id is an int, number is a
//      string. webapiv2 spells number as an int for what may or may not be the
//      same value. Neither is parsed into the other and neither is derived,
//      because their equality is unverified -- the same discipline ARCH1.9
//      needed empirical work to relax for employees.
//   5. Keep requesting pages until as many records are collected as totalCount
//      promised, or the page cap is reached -- whichever comes first. The cap
//      guarantees termination (GO1.6).
//   6. Report what the read actually covered alongside what it found. A read
//      that stopped at the cap has not seen the account; a caller treating it
//      as though it had would be acting on a wrong answer, not a partial one.
//
// Output: CustomerScan{Customers, Total, Fetched, Pages}, whose Complete()
//         reports Fetched >= Total. Modelled on SettingKeyScan, for the same
//         reason: the bound travels with the findings instead of being hidden.
//
// Dependencies: internalAPI.authedRequest (session + kacompany), ErrDecode,
//               sanitize for any error carrying a URL.
// Side effects: outbound HTTPS GETs only. No mutation, no cache, no state.

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Customer is the provider-owned representation of a customer in Kala.
//
// Customers are imported from e-conomic, which owns them. Nothing here writes.
type Customer struct {
	// ID and Number are BOTH carried as upstream sends them. The internal API
	// spells Number as a string; webapiv2 spells it as an int, and the two are
	// not known to hold the same value. Neither is derived from the other --
	// ARCH1.9 needed an experiment to relax exactly this assumption for
	// employees, and no such experiment has been run for customers.
	ID     int64
	Number string

	FirstName string
	LastName  string
	Company   string
	CVR       string
	Email     string
	Phone     string
	Address   string
	Zip       string
	City      string
	EAN       string
	CaseCount int
}

// CustomerQuery controls a customer list read.
type CustomerQuery struct {
	// Search is passed upstream as the query parameter. Empty means unfiltered.
	Search string

	// PageSize is records per request; zero means the client default.
	PageSize int

	// MaxPages caps how many pages are fetched, guaranteeing termination
	// even if upstream never serves a short page (GO1.6).
	MaxPages int
}

// CustomerScan is a customer list read together with how much of the account it
// actually covered.
//
// Coverage travels with the findings for the same reason it does on
// SettingKeyScan: a read that stopped at its page cap has not seen the account,
// and a caller that treats it as though it had is acting on a wrong answer
// rather than a partial one.
type CustomerScan struct {
	Customers []Customer

	// Total is the record count upstream reported; Fetched is how many were
	// actually collected; Pages is how many requests were issued.
	Total   int
	Fetched int
	Pages   int
}

// Complete reports whether the read covered everything upstream claimed to hold.
func (s CustomerScan) Complete() bool { return s.Fetched >= s.Total }

// wireCustomer is the internal API's customer record. Unexported: upstream
// naming stops here (ARCH1.4, and layering_test.go fails the build otherwise).
//
// Observed 2026-09-01 against the real API. Note number is a string on this
// surface; webapiv2 sends an int for the same field name.
type wireCustomer struct {
	ID        int64  `json:"id"`
	Number    string `json:"number"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Company   string `json:"company"`
	CVR       string `json:"cvr"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
	Address   string `json:"address"`
	Zip       string `json:"zip"`
	City      string `json:"city"` // arrives as null in practice
	EAN       string `json:"ean"`
	CaseCount int    `json:"caseCount"`
}

func (w wireCustomer) toDomain() Customer {
	return Customer{
		ID:        w.ID,
		Number:    w.Number,
		FirstName: w.FirstName,
		LastName:  w.LastName,
		Company:   w.Company,
		CVR:       w.CVR,
		Email:     w.Email,
		Phone:     w.Phone,
		Address:   w.Address,
		Zip:       w.Zip,
		City:      w.City,
		EAN:       w.EAN,
		CaseCount: w.CaseCount,
	}
}

// wireCustomersPage is the paged envelope. totalCount is what makes the
// completeness signal a direct comparison rather than an inference.
type wireCustomersPage struct {
	Customers  []wireCustomer `json:"customers"`
	TotalCount int            `json:"totalCount"`
}

// ListCustomers reads customers from the internal app API, following pages
// until the account is covered or the page cap is reached.
//
// The cap is what makes CustomerScan.Complete meaningful: termination is
// guaranteed (GO1.6), so a caller must be told whether termination came from
// reaching the end or from hitting the bound.
func (c *internalAPI) ListCustomers(ctx context.Context, q CustomerQuery) (CustomerScan, error) {
	pageSize := q.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	maxPages := q.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}

	scan := CustomerScan{Customers: make([]Customer, 0, pageSize)}

	for page := 0; page < maxPages; page++ {
		params := url.Values{}
		params.Set("page", strconv.Itoa(page))
		params.Set("pageSize", strconv.Itoa(pageSize))
		params.Set("query", q.Search)

		raw, err := c.authedRequest(
			ctx, http.MethodGet, "/api/GetCustomersPaged2/?"+params.Encode(), nil, contentTypeHeader,
		)
		if err != nil {
			return CustomerScan{}, err
		}
		scan.Pages++

		// An empty 200 on a LIST read means "no records". It means "not found"
		// only on a single-record read -- the distinction decodeEmployee and
		// decodeEmployeeList exist separately to preserve.
		if len(bytes.TrimSpace(raw)) == 0 {
			return scan, nil
		}

		var envelope wireCustomersPage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return CustomerScan{}, fmt.Errorf("%w: customers response: %v", ErrDecode, err)
		}
		scan.Total = envelope.TotalCount

		for _, wc := range envelope.Customers {
			scan.Customers = append(scan.Customers, wc.toDomain())
		}
		scan.Fetched = len(scan.Customers)

		// A short page is the end of the data regardless of what totalCount
		// claimed. Trusting totalCount over the records actually served would
		// loop forever against an upstream that over-reports.
		if len(envelope.Customers) < pageSize {
			return scan, nil
		}
		if scan.Fetched >= scan.Total {
			return scan, nil
		}
	}

	return scan, nil
}
