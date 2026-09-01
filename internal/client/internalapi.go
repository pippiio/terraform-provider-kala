package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// The internal app API is the surface Kala's own web client uses. It is
// undocumented and unversioned, and it is the ONLY place employee activation
// lives (ADR-002).
//
// Guardrail ARCH1.3 permits exactly one write here — SetValidated — and every
// write must be verified by a read-back (ARCH1.8). Nomenclature differs from
// webapiv2: this API says "worker" and keys on workerNr (ARCH1.4 keeps that
// vocabulary confined to this file).

const (
	// DefaultInternalEndpoint is the base for the app's own API.
	DefaultInternalEndpoint = "https://app.kala.dk"

	acceptHeader      = "application/json, text/plain, */*"
	contentTypeHeader = "application/json;charset=UTF-8"
)

// InternalConfig configures the session-authenticated internal API client.
type InternalConfig struct {
	Endpoint   string
	Username   string
	Password   string
	Timeout    time.Duration
	MaxRetries int

	HTTPClient *http.Client

	retryBaseDur time.Duration
}

func (c InternalConfig) withDefaults() InternalConfig {
	if c.Endpoint == "" {
		c.Endpoint = DefaultInternalEndpoint
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.retryBaseDur <= 0 {
		c.retryBaseDur = defaultRetryBase
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: c.Timeout}
	}
	return c
}

// Worker is the internal API's representation of a person.
//
// Deliberately distinct from Employee: the two APIs disagree on identifier and
// on which fields exist, and ARCH1.9 forbids translating between workerNr and
// employeeNumber until the mapping is confirmed against a live tenant.
type Worker struct {
	WorkerNr    int64
	WorkerID    int64
	Name        string
	Title       string
	Phone       string
	Department  string
	Initials    string
	IsValidated bool
}

// InternalClient is the session-authenticated read surface plus the single
// permitted write.
type InternalClient interface {
	// ListWorkers returns all workers visible to the authenticated session.
	ListWorkers(ctx context.Context) ([]Worker, error)

	// GetWorker returns one worker by workerNr.
	GetWorker(ctx context.Context, workerNr int64) (Worker, error)

	// SetWorkerValidated sets a worker's activation state and VERIFIES the
	// result by reading it back (ARCH1.8). An unconfirmed write is an error.
	SetWorkerValidated(ctx context.Context, workerNr int64, validated bool) error

	// GetWorkerInfo returns the detailed record for a worker.
	//
	// Enrichment only — never an existence check. /api/WorkerInfo answers a
	// missing workerNr with HTTP 500 (a .NET "Sequence contains no elements"
	// page), which the retry policy treats as transient. Establish existence
	// with GetWorker first.
	GetWorkerInfo(ctx context.Context, workerNr int64) (WorkerInfo, error)

	// CreateWorker registers a new employee via /Api/SignUp/.
	//
	// This is the ONLY way to create an employee in Kala — webapiv2 has no
	// equivalent. It is verified by read-back like every internal-API write.
	CreateWorker(ctx context.Context, in NewWorker) (Worker, error)
}

// WorkerInfo is the detailed worker record from /api/WorkerInfo.
//
// Several fields Kala models as loose strings rather than typed values —
// DateOfEmployment, FlexStartDate, and NormHours all arrive as strings, so they
// are passed through verbatim rather than parsed into times or numbers we would
// have to guess the format of.
type WorkerInfo struct {
	WorkerNr   int64
	WorkerID   int64
	Name       string
	Email      string
	Initials   string
	Title      string
	Department string

	Phone        string
	PrivatePhone string
	LicensePlate string

	DateOfEmployment string
	FlexStartDate    string
	NormHours        string

	IsValidated        bool
	IsLeader           bool
	IsPlanner          bool
	IsSuperUser        bool
	IsFinance          bool
	IsVisibleInPlanner bool
	AllowWeekView      bool

	LeaderNote string
}

// NewWorker is the input for creating an employee.
type NewWorker struct {
	// Number is sent as medarbejderNr. The caller chooses it; Kala does not
	// allocate one.
	Number int64
	Email  string
	Name   string
}

// --- wire types (unexported; ARCH1.4) ------------------------------------

type wireSignInRequest struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	GaTrackingID string `json:"gaTrackingId"`
	AppType      string `json:"appType"`
	Token        string `json:"token"`
}

type wireCompany struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type wireSignInResponse struct {
	GlobalUserID     int64         `json:"globalUserId"`
	SecureLoginToken string        `json:"secureLoginToken"`
	Companies        []wireCompany `json:"companies"`
}

type wireSelectCompanyRequest struct {
	GlobalCompanyID  int64  `json:"globalCompanyId"`
	SecureLoginToken string `json:"secureLoginToken"`
	AppType          string `json:"appType"`
}

type wireSelectCompanyResponse struct {
	GlobalCompanyName string `json:"globalCompanyName"`
	Token             string `json:"token"`
}

type wireWorker struct {
	WorkerNr    *int64 `json:"workerNr"`
	WorkerID    int64  `json:"workerId"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Phone       string `json:"phone"`
	Department  string `json:"department"`
	Initials    string `json:"initials"`
	IsValidated bool   `json:"isValidated"`
}

func (w wireWorker) toDomain() (Worker, error) {
	if w.WorkerNr == nil || *w.WorkerNr == 0 {
		return Worker{}, fmt.Errorf("%w: worker record has no 'workerNr' identity", ErrDecode)
	}
	return Worker{
		WorkerNr:    *w.WorkerNr,
		WorkerID:    w.WorkerID,
		Name:        w.Name,
		Title:       w.Title,
		Phone:       w.Phone,
		Department:  w.Department,
		Initials:    w.Initials,
		IsValidated: w.IsValidated,
	}, nil
}

type wireWorkerInfo struct {
	WorkerNr   *int64 `json:"workerNr"`
	WorkerID   int64  `json:"workerId"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	Initials   string `json:"initials"`
	Title      string `json:"title"`
	Department string `json:"department"`

	Phone        string `json:"phone"`
	PrivatePhone string `json:"privatePhone"`
	LicensePlate string `json:"licensePlate"`

	// Strings upstream, not dates or numbers. Observed 2026-09-01.
	DateOfEmployment string `json:"dateOfEmployment"`
	FlexStartDate    string `json:"flexStartDate"`
	NormHours        string `json:"normHours"`

	IsValidated        bool `json:"isValidated"`
	IsLeader           bool `json:"isLeader"`
	IsPlanner          bool `json:"isPlanner"`
	IsSuperUser        bool `json:"isSuperUser"`
	IsFinance          bool `json:"isFinance"`
	IsVisibleInPlanner bool `json:"isVisibleInPlanner"`
	AllowWeekView      bool `json:"allowWeekView"`

	LeaderNote string `json:"leaderNote"`
}

func (w wireWorkerInfo) toDomain() (WorkerInfo, error) {
	if w.WorkerNr == nil || *w.WorkerNr == 0 {
		return WorkerInfo{}, fmt.Errorf("%w: worker info has no 'workerNr' identity", ErrDecode)
	}
	return WorkerInfo{
		WorkerNr: *w.WorkerNr, WorkerID: w.WorkerID, Name: w.Name, Email: w.Email,
		Initials: w.Initials, Title: w.Title, Department: w.Department,
		Phone: w.Phone, PrivatePhone: w.PrivatePhone, LicensePlate: w.LicensePlate,
		DateOfEmployment: w.DateOfEmployment, FlexStartDate: w.FlexStartDate, NormHours: w.NormHours,
		IsValidated: w.IsValidated, IsLeader: w.IsLeader, IsPlanner: w.IsPlanner,
		IsSuperUser: w.IsSuperUser, IsFinance: w.IsFinance,
		IsVisibleInPlanner: w.IsVisibleInPlanner, AllowWeekView: w.AllowWeekView,
		LeaderNote: w.LeaderNote,
	}, nil
}

type wireSetValidatedRequest struct {
	WorkerNr    int64 `json:"workerNr"`
	IsValidated bool  `json:"isValidated"`
}

// wireSignUpRequest creates an employee.
//
// medarbejderNr is Danish for "employee number". Whether it is the same value
// as workerNr (returned by /api/Workers) and as webapiv2's employeeNumber is
// UNVERIFIED — see ARCH1.9. Creating an employee with a known medarbejderNr and
// observing which workerNr appears is the experiment that would settle it.
type wireSignUpRequest struct {
	MedarbejderNr int64  `json:"medarbejderNr"`
	Email         string `json:"email"`
	Name          string `json:"name"`
}

// --- implementation -------------------------------------------------------

type internalAPI struct {
	cfg InternalConfig

	// session state, guarded because Terraform calls providers concurrently
	mu        sync.Mutex
	token     string
	companyID int64
}

var _ InternalClient = (*internalAPI)(nil)

// NewInternal returns a client for the internal app API.
//
// Authentication is lazy: the SignIn/SelectCompany handshake runs on first use
// rather than at construction, so building a client never performs I/O.
func NewInternal(cfg InternalConfig) InternalClient {
	return &internalAPI{cfg: cfg.withDefaults()}
}

// session returns a valid kauthtoken, performing the two-step handshake once.
//
// SignIn yields a secureLoginToken and the account's companies; SelectCompany
// exchanges those for the kauthtoken that authorises API calls.
func (c *internalAPI) session(ctx context.Context) (token string, companyID int64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" {
		return c.token, c.companyID, nil
	}

	if c.cfg.Username == "" || c.cfg.Password == "" {
		return "", 0, fmt.Errorf("internal API requires both username and password: " +
			"set them in the provider block or via KALA_USERNAME and KALA_PASSWORD")
	}

	signIn, err := c.signIn(ctx)
	if err != nil {
		return "", 0, err
	}
	if len(signIn.Companies) == 0 {
		return "", 0, fmt.Errorf("kala: sign-in succeeded but returned no companies")
	}

	company := signIn.Companies[0]
	selected, err := c.selectCompany(ctx, company.ID, signIn.SecureLoginToken)
	if err != nil {
		return "", 0, err
	}
	if selected.Token == "" {
		return "", 0, fmt.Errorf("kala: SelectCompany returned an empty token")
	}

	c.token, c.companyID = selected.Token, company.ID
	return c.token, c.companyID, nil
}

func (c *internalAPI) signIn(ctx context.Context) (wireSignInResponse, error) {
	body, err := json.Marshal(wireSignInRequest{
		Username: c.cfg.Username,
		Password: c.cfg.Password,
		AppType:  "web",
	})
	if err != nil {
		return wireSignInResponse{}, fmt.Errorf("kala: building sign-in request: %w", err)
	}

	raw, err := c.request(ctx, http.MethodPost, "/Auth/SignIn/", body, nil)
	if err != nil {
		return wireSignInResponse{}, fmt.Errorf("kala: sign-in failed: %w", err)
	}

	var out wireSignInResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return wireSignInResponse{}, fmt.Errorf("%w: sign-in response: %v", ErrDecode, err)
	}
	return out, nil
}

func (c *internalAPI) selectCompany(ctx context.Context, companyID int64, secureLoginToken string) (wireSelectCompanyResponse, error) {
	body, err := json.Marshal(wireSelectCompanyRequest{
		GlobalCompanyID:  companyID,
		SecureLoginToken: secureLoginToken,
		AppType:          "web",
	})
	if err != nil {
		return wireSelectCompanyResponse{}, fmt.Errorf("kala: building select-company request: %w", err)
	}

	raw, err := c.request(ctx, http.MethodPost, "/Auth/SelectCompany/", body, nil)
	if err != nil {
		return wireSelectCompanyResponse{}, fmt.Errorf("kala: select-company failed: %w", err)
	}

	var out wireSelectCompanyResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return wireSelectCompanyResponse{}, fmt.Errorf("%w: select-company response: %v", ErrDecode, err)
	}
	return out, nil
}

// request performs one HTTP call with retries, mirroring the webapiv2 policy:
// retry 5xx and transport failures, never 4xx.
func (c *internalAPI) request(ctx context.Context, method, path string, body []byte, headers map[string]string) ([]byte, error) {
	target := c.cfg.Endpoint + path

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var reader *bytes.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		} else {
			reader = bytes.NewReader(nil)
		}

		req, err := http.NewRequestWithContext(ctx, method, target, reader) // GO1.5
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTransport, sanitizeError(err))
		}
		req.Header.Set("Accept", acceptHeader)
		req.Header.Set("Content-Type", contentTypeHeader)
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		out, err := c.attempt(req)
		if err == nil {
			return out, nil
		}
		lastErr = err

		if !isRetryable(err) {
			return nil, err
		}
		if attempt == c.cfg.MaxRetries {
			break
		}
		if err := sleepWithContext(ctx, backoffFor(attempt, c.cfg.retryBaseDur)); err != nil {
			return nil, err
		}
	}

	return nil, lastErr
}

func (c *internalAPI) attempt(req *http.Request) ([]byte, error) {
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: %v", ErrTransport, sanitizeError(err))
	}
	defer func() { _ = resp.Body.Close() }() // GO1.1

	return readBody(resp)
}

// ListWorkers returns all workers visible to the session.
func (c *internalAPI) ListWorkers(ctx context.Context) ([]Worker, error) {
	token, _, err := c.session(ctx)
	if err != nil {
		return nil, err
	}

	raw, err := c.request(ctx, http.MethodGet, "/api/Workers/", nil, map[string]string{
		"kauthtoken": token,
	})
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return []Worker{}, nil
	}

	var ws []wireWorker
	if err := json.Unmarshal(raw, &ws); err != nil {
		return nil, fmt.Errorf("%w: workers response: %v", ErrDecode, err)
	}

	out := make([]Worker, 0, len(ws))
	for i, w := range ws {
		worker, err := w.toDomain()
		if err != nil {
			return nil, fmt.Errorf("%w (record at index %d)", err, i)
		}
		out = append(out, worker)
	}
	return out, nil
}

// GetWorker returns one worker by workerNr.
//
// The internal API has a WorkerInfo endpoint, but it is only used to enrich —
// identity and activation state both come from the list, which is the surface
// the production Terraform relies on.
func (c *internalAPI) GetWorker(ctx context.Context, workerNr int64) (Worker, error) {
	workers, err := c.ListWorkers(ctx)
	if err != nil {
		return Worker{}, err
	}
	for _, w := range workers {
		if w.WorkerNr == workerNr {
			return w, nil
		}
	}
	return Worker{}, fmt.Errorf("%w: no worker with workerNr %d", ErrNotFound, workerNr)
}

// SetWorkerValidated sets activation state and confirms it by reading back.
//
// This is the ONLY write permitted against the internal API (ARCH1.3). HTTP 200
// is not proof: the old data "http" approach asserted only status_code == 200,
// and offboarding is precisely where a silent no-op is most damaging. ARCH1.5
// explicitly excludes this path from graceful degradation — a failure here must
// fail the apply.
func (c *internalAPI) SetWorkerValidated(ctx context.Context, workerNr int64, validated bool) error {
	token, companyID, err := c.session(ctx)
	if err != nil {
		return err
	}

	body, err := json.Marshal(wireSetValidatedRequest{WorkerNr: workerNr, IsValidated: validated})
	if err != nil {
		return fmt.Errorf("kala: building SetValidated request: %w", err)
	}

	if _, err := c.request(ctx, http.MethodPost, "/api/SetValidated/", body, map[string]string{
		"kauthtoken": token,
		"kacompany":  strconv.FormatInt(companyID, 10),
	}); err != nil {
		return fmt.Errorf("kala: SetValidated for worker %d: %w", workerNr, err)
	}

	// Read-back verification (ARCH1.8).
	worker, err := c.GetWorker(ctx, workerNr)
	if err != nil {
		return fmt.Errorf("kala: could not verify SetValidated for worker %d: %w", workerNr, err)
	}
	if worker.IsValidated != validated {
		return fmt.Errorf(
			"kala: SetValidated for worker %d reported success but isValidated reads back as %t, expected %t",
			workerNr, worker.IsValidated, validated)
	}

	return nil
}

// CreateWorker registers a new employee.
//
// Endpoint path casing is deliberate: /Api/SignUp/ capitalises "Api" while
// /api/Workers/ does not. The API is inconsistent and both spellings are
// reproduced exactly as observed.
func (c *internalAPI) CreateWorker(ctx context.Context, in NewWorker) (Worker, error) {
	if in.Number == 0 {
		return Worker{}, fmt.Errorf("employee number is required: Kala does not allocate one, the caller chooses it")
	}
	if in.Name == "" {
		return Worker{}, fmt.Errorf("name is required to create an employee")
	}
	if in.Email == "" {
		return Worker{}, fmt.Errorf("email is required to create an employee")
	}

	token, companyID, err := c.session(ctx)
	if err != nil {
		return Worker{}, err
	}

	body, err := json.Marshal(wireSignUpRequest{
		MedarbejderNr: in.Number,
		Email:         in.Email,
		Name:          in.Name,
	})
	if err != nil {
		return Worker{}, fmt.Errorf("kala: building sign-up request: %w", err)
	}

	if _, err := c.request(ctx, http.MethodPost, "/Api/SignUp/", body, map[string]string{
		"kauthtoken": token,
		"kacompany":  strconv.FormatInt(companyID, 10),
	}); err != nil {
		return Worker{}, fmt.Errorf("kala: creating employee %d: %w", in.Number, err)
	}

	// Read-back verification (ARCH1.8): confirm the employee now exists under
	// the number we supplied. This is also the check that would reveal any
	// mismatch between medarbejderNr and workerNr.
	worker, err := c.GetWorker(ctx, in.Number)
	if err != nil {
		return Worker{}, fmt.Errorf(
			"kala: employee %d was submitted but could not be found afterwards — "+
				"this may mean medarbejderNr and workerNr are not the same value: %w", in.Number, err)
	}

	return worker, nil
}

// GetWorkerInfo returns the detailed worker record.
//
// Enrichment only. A missing workerNr produces HTTP 500 rather than 404, which
// the retry policy treats as transient — so callers must establish existence
// with GetWorker before calling this, or they will pay three pointless retries
// and receive ErrServer instead of ErrNotFound.
func (c *internalAPI) GetWorkerInfo(ctx context.Context, workerNr int64) (WorkerInfo, error) {
	token, companyID, err := c.session(ctx)
	if err != nil {
		return WorkerInfo{}, err
	}

	raw, err := c.request(ctx, http.MethodGet,
		"/api/WorkerInfo/?workerNr="+strconv.FormatInt(workerNr, 10), nil, map[string]string{
			"kauthtoken": token,
			"kacompany":  strconv.FormatInt(companyID, 10),
		})
	if err != nil {
		return WorkerInfo{}, fmt.Errorf("kala: reading worker info for %d: %w", workerNr, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return WorkerInfo{}, fmt.Errorf("%w: empty worker info for %d", ErrNotFound, workerNr)
	}

	var w wireWorkerInfo
	if err := json.Unmarshal(raw, &w); err != nil {
		return WorkerInfo{}, fmt.Errorf("%w: worker info response: %v", ErrDecode, err)
	}
	return w.toDomain()
}
