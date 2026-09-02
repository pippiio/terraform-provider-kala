package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// customerMock serves the internal API's SignIn -> SelectCompany handshake plus
// GET /api/GetCustomersPaged2/, paging a fixed roster.
//
// Response shapes are copied from a real payload observed 2026-09-01; values are
// fictional. Note number is a STRING here -- webapiv2 spells the same field as an
// int, and the two are not known to agree (notes.md section 4).
type customerMock struct {
	srv       *httptest.Server
	roster    []map[string]any
	totalLie  int  // when non-zero, totalCount reports this instead of len(roster)
	emptyBody bool // return a 200 with no body at all
	pageHits  []int
}

func newCustomerMock(t *testing.T, n int) *customerMock {
	t.Helper()
	m := &customerMock{}
	for i := 1; i <= n; i++ {
		m.roster = append(m.roster, map[string]any{
			"id":        i,
			"number":    fmt.Sprintf("K-%03d", i),
			"firstName": "Frodo",
			"lastName":  fmt.Sprintf("Baggins%d", i),
			"company":   "Bag End Ltd",
			"cvr":       "12345678",
			"email":     fmt.Sprintf("frodo%d@example.com", i),
			"phone":     "+45 00 00 00 00",
			"address":   "Bagshot Row 1",
			"zip":       "1000",
			"city":      nil,
			"ean":       "",
			"caseCount": 2,
		})
	}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				GlobalUserID: 9001, SecureLoginToken: "secure-login-token",
				Companies: []wireCompany{{ID: 4242, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{
				GlobalCompanyName: "Rivendell", Token: "kauth-token",
			})
		case strings.HasSuffix(r.URL.Path, "/api/GetCustomersPaged2/"):
			if m.emptyBody {
				w.WriteHeader(http.StatusOK)
				return
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
			m.pageHits = append(m.pageHits, page)
			total := len(m.roster)
			if m.totalLie != 0 {
				total = m.totalLie
			}
			lo := page * size
			hi := lo + size
			if lo > len(m.roster) {
				lo = len(m.roster)
			}
			if hi > len(m.roster) {
				hi = len(m.roster)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"customers":  m.roster[lo:hi],
				"totalCount": total,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *customerMock) client() InternalClient {
	return NewInternal(InternalConfig{
		Endpoint: m.srv.URL, Username: "user@example.com", Password: "hunter2",
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond,
	})
}

func TestListCustomers_MapsWireToDomain(t *testing.T) {
	m := newCustomerMock(t, 1)
	scan, err := m.client().ListCustomers(t.Context(), CustomerQuery{})
	if err != nil {
		t.Fatalf("ListCustomers: %v", err)
	}
	if len(scan.Customers) != 1 {
		t.Fatalf("got %d customers, want 1", len(scan.Customers))
	}
	c := scan.Customers[0]
	if c.ID != 1 {
		t.Errorf("ID = %d, want 1", c.ID)
	}
	if c.Number != "K-001" {
		t.Errorf("Number = %q, want %q -- the internal API spells number as a string", c.Number, "K-001")
	}
	if c.FirstName != "Frodo" || c.LastName != "Baggins1" {
		t.Errorf("name = %q %q, want Frodo Baggins1", c.FirstName, c.LastName)
	}
	if c.Company != "Bag End Ltd" || c.CVR != "12345678" {
		t.Errorf("company/cvr = %q/%q", c.Company, c.CVR)
	}
	if c.Email != "frodo1@example.com" || c.Phone != "+45 00 00 00 00" {
		t.Errorf("contact = %q/%q", c.Email, c.Phone)
	}
	if c.Address != "Bagshot Row 1" || c.Zip != "1000" {
		t.Errorf("address/zip = %q/%q", c.Address, c.Zip)
	}
	if c.City != "" {
		t.Errorf("City = %q, want empty -- upstream sends null", c.City)
	}
	if c.CaseCount != 2 {
		t.Errorf("CaseCount = %d, want 2", c.CaseCount)
	}
}

func TestListCustomers_EmptyBodyIsZeroRecordsNotAnError(t *testing.T) {
	m := newCustomerMock(t, 0)
	m.emptyBody = true
	scan, err := m.client().ListCustomers(t.Context(), CustomerQuery{})
	if err != nil {
		t.Fatalf("empty 200 on a LIST read must mean zero records, not an error: %v", err)
	}
	if len(scan.Customers) != 0 {
		t.Errorf("got %d customers, want 0", len(scan.Customers))
	}
	if !scan.Complete() {
		t.Error("an empty account was fully read; Complete() must be true")
	}
}

func TestListCustomers_PagesUntilTotalReached(t *testing.T) {
	m := newCustomerMock(t, 7)
	scan, err := m.client().ListCustomers(t.Context(), CustomerQuery{PageSize: 3})
	if err != nil {
		t.Fatalf("ListCustomers: %v", err)
	}
	if len(scan.Customers) != 7 {
		t.Fatalf("got %d customers, want 7", len(scan.Customers))
	}
	if scan.Total != 7 || scan.Fetched != 7 {
		t.Errorf("Total/Fetched = %d/%d, want 7/7", scan.Total, scan.Fetched)
	}
	if !scan.Complete() {
		t.Error("all 7 of 7 fetched; Complete() must be true")
	}
	if len(m.pageHits) != 3 {
		t.Errorf("page requests = %v, want 3 pages for 7 records at pageSize 3", m.pageHits)
	}
}

// The failure this exists to prevent: a capped read that reports success while
// having seen only part of the account. Fixed for the setting-key survey in
// fe21eae; it must not reappear here.
func TestListCustomers_CappedReadReportsItselfIncomplete(t *testing.T) {
	m := newCustomerMock(t, 10)
	scan, err := m.client().ListCustomers(t.Context(), CustomerQuery{PageSize: 2, MaxPages: 2})
	if err != nil {
		t.Fatalf("ListCustomers: %v", err)
	}
	if scan.Fetched != 4 {
		t.Fatalf("Fetched = %d, want 4 (2 pages x 2)", scan.Fetched)
	}
	if scan.Total != 10 {
		t.Errorf("Total = %d, want 10 -- the account size upstream reported", scan.Total)
	}
	if scan.Complete() {
		t.Error("read stopped at the page cap having seen 4 of 10; Complete() must be false")
	}
}

// A totalCount larger than the records actually served must not be silently
// smoothed over: the loop terminates on a short page, and honesty about coverage
// is preserved.
func TestListCustomers_ShortPageTerminatesAndReportsIncomplete(t *testing.T) {
	m := newCustomerMock(t, 3)
	m.totalLie = 99
	scan, err := m.client().ListCustomers(t.Context(), CustomerQuery{PageSize: 5})
	if err != nil {
		t.Fatalf("ListCustomers: %v", err)
	}
	if scan.Fetched != 3 {
		t.Fatalf("Fetched = %d, want 3", scan.Fetched)
	}
	if scan.Complete() {
		t.Error("upstream claimed 99 records and served 3; Complete() must be false")
	}
}

func TestListCustomers_SearchIsPassedUpstream(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 1, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})
		default:
			gotQuery = r.URL.Query().Get("query")
			_ = json.NewEncoder(w).Encode(map[string]any{"customers": []any{}, "totalCount": 0})
		}
	}))
	t.Cleanup(srv.Close)
	c := NewInternal(InternalConfig{
		Endpoint: srv.URL, Username: "u", Password: "p",
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond,
	})
	if _, err := c.ListCustomers(t.Context(), CustomerQuery{Search: "baggins"}); err != nil {
		t.Fatalf("ListCustomers: %v", err)
	}
	if gotQuery != "baggins" {
		t.Errorf("upstream query = %q, want %q", gotQuery, "baggins")
	}
}

// FR10 regression: kacompany has always been sent by authedRequest. The new
// endpoint depends on it, so assert it rather than assume it.
func TestListCustomers_SendsCompanyHeader(t *testing.T) {
	var gotCompany, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 4242, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})
		default:
			gotCompany = r.Header.Get("kacompany")
			gotToken = r.Header.Get("kauthtoken")
			_ = json.NewEncoder(w).Encode(map[string]any{"customers": []any{}, "totalCount": 0})
		}
	}))
	t.Cleanup(srv.Close)
	c := NewInternal(InternalConfig{
		Endpoint: srv.URL, Username: "u", Password: "p",
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond,
	})
	if _, err := c.ListCustomers(t.Context(), CustomerQuery{}); err != nil {
		t.Fatalf("ListCustomers: %v", err)
	}
	if gotCompany != "4242" {
		t.Errorf("kacompany header = %q, want %q", gotCompany, "4242")
	}
	if gotToken != "kauth-token" {
		t.Errorf("kauthtoken header = %q", gotToken)
	}
}

func TestListCustomers_UndecodableBodyIsADecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 1, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})
		default:
			_, _ = w.Write([]byte(`{"customers": "not-an-array"`))
		}
	}))
	t.Cleanup(srv.Close)
	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p",
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond})
	if _, err := c.ListCustomers(t.Context(), CustomerQuery{}); !errors.Is(err, ErrDecode) {
		t.Fatalf("err = %v, want ErrDecode", err)
	}
}

func TestListCustomers_UpstreamErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_ = json.NewEncoder(w).Encode(wireSignInResponse{
				SecureLoginToken: "t", Companies: []wireCompany{{ID: 1, Name: "Rivendell"}},
			})
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_ = json.NewEncoder(w).Encode(wireSelectCompanyResponse{Token: "kauth-token"})
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewInternal(InternalConfig{Endpoint: srv.URL, Username: "u", Password: "p",
		MaxRetries: 1, Timeout: 5 * time.Second, retryBaseDur: time.Microsecond})
	if _, err := c.ListCustomers(t.Context(), CustomerQuery{}); !errors.Is(err, ErrClientRequest) {
		t.Fatalf("err = %v, want ErrClientRequest", err)
	}
}
