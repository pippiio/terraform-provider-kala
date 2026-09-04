package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The internal app API is the surface Kala's own web client uses. It is
// undocumented and unversioned, and it is the ONLY place employee activation
// lives.
//
// Guardrail ARCH1.3 enumerates every write permitted here; see that list before
// adding another. Every write must be verified by a read-back (ARCH1.8). Nomenclature differs from webapiv2:
// this API says "worker" and keys on workerNr (ARCH1.4 keeps that vocabulary
// confined to this file).

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

	// SetWorkerEmail changes a worker's email via /api/SetEmailNew/ and verifies
	// the result by reading it back (ARCH1.8).
	SetWorkerEmail(ctx context.Context, workerNr int64, email string) error

	// SetWorkerField sets one string-valued field (phone, title, initials,
	// license plate, department, leader note) and verifies it by read-back.
	SetWorkerField(ctx context.Context, workerNr int64, field WorkerField, value string) error

	// SetWorkerRole sets one boolean role (leader, finance, planner) and
	// verifies it by read-back.
	SetWorkerRole(ctx context.Context, workerNr int64, role WorkerRole, value bool) error

	// SetWorkerDateOfEmployment sets the employment start date from a plain
	// YYYY-MM-DD date. The endpoint's timestamp/offset encoding is handled
	// internally so the value round-trips.
	SetWorkerDateOfEmployment(ctx context.Context, workerNr int64, date string) error

	// SetWorkerBoss sets which employee an employee reports to — Kala's "first
	// boss" — and verifies the result by reading it back (ARCH1.8).
	SetWorkerBoss(ctx context.Context, workerNr, bossNr int64) error

	// SendWelcomeEmail sends Kala's onboarding email to an address.
	//
	// Unlike every other write here, this has no persistent effect to read
	// back — the only confirmation available is the endpoint's own status
	// field, so ARCH1.8's read-back rule cannot apply.
	SendWelcomeEmail(ctx context.Context, email string) error

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

	// BossNumber is the employee number of this employee's boss, or 0 when they
	// have none. BossName is that person's name, read-only — ChangeBoss takes
	// the number.
	BossNumber int64
	BossName   string
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

	// firstBoss is a nested object, not a scalar, and is absent for a worker
	// with no boss — hence the pointer. Observed 2026-09-04:
	// {"name": "...", "workerNr": 4}.
	FirstBoss *wireFirstBoss `json:"firstBoss"`
}

// wireFirstBoss is the nested shape carrying an employee's boss.
type wireFirstBoss struct {
	Name     string `json:"name"`
	WorkerNr int64  `json:"workerNr"`
}

func (w wireWorkerInfo) toDomain() (WorkerInfo, error) {
	if w.WorkerNr == nil || *w.WorkerNr == 0 {
		return WorkerInfo{}, fmt.Errorf("%w: worker info has no 'workerNr' identity", ErrDecode)
	}

	// A worker with no boss has no firstBoss object at all; zero means "none"
	// rather than "employee 0", which cannot exist.
	var bossNumber int64
	var bossName string
	if w.FirstBoss != nil {
		bossNumber, bossName = w.FirstBoss.WorkerNr, w.FirstBoss.Name
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
		BossNumber: bossNumber, BossName: bossName,
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
type wireSetEmailRequest struct {
	WorkerNr int64  `json:"workerNr"`
	Email    string `json:"email"`
}

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

// invalidateSession clears the cached token so the next call re-authenticates.
//
// Only clears the generation it was told about, so a token another goroutine
// already refreshed is not thrown away.
func (c *internalAPI) invalidateSession(staleToken string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == staleToken {
		c.token = ""
	}
}

// authedRequest performs a request carrying the session headers, renewing the
// session once if the server rejects the token.
//
// Kala does not document a session lifetime. Without renewal a long apply would
// fail every resource after the token expires — the exact case a
// ten-employee run would hit. Renewal is attempted at most once, so genuinely
// bad credentials fail fast instead of looping.
func (c *internalAPI) authedRequest(
	ctx context.Context, method, path string, body []byte, contentType string,
) ([]byte, error) {
	token, companyID, err := c.session(ctx)
	if err != nil {
		return nil, err
	}

	headers := func(tok string, cid int64) map[string]string {
		return map[string]string{
			"kauthtoken": tok,
			"kacompany":  strconv.FormatInt(cid, 10),
		}
	}

	raw, err := c.requestWithContentType(ctx, method, path, body, contentType, headers(token, companyID))
	if err == nil || !errors.Is(err, ErrUnauthorized) {
		return raw, errorEnvelope(raw, err)
	}

	// The token was rejected: drop it and try once with a fresh session.
	c.invalidateSession(token)

	token, companyID, err2 := c.session(ctx)
	if err2 != nil {
		// Report the original rejection; the re-login failure is the symptom.
		return nil, err
	}

	raw, err = c.requestWithContentType(ctx, method, path, body, contentType, headers(token, companyID))
	return raw, errorEnvelope(raw, err)
}

// wireStatusEnvelope is the failure shape the internal API returns WITH HTTP 200.
type wireStatusEnvelope struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// errorEnvelope turns Kala's in-body failure report into a real error.
//
// OBSERVED 2026-09-04: the internal API answers a refused write with HTTP 200
// and {"status":"Error","message":"..."} — for example, refusing to change the
// email of a user attached to more than one company. Every write here read the
// status line, saw 200, and discarded the body, so the refusal was invisible
// and only the read-back verification noticed something was wrong. That made a
// precise, translated explanation from Kala surface as a generic mismatch.
//
// Successful writes answer either {"status":"Success"} or a small data object
// with no status field at all, so only an explicit "Error" is treated as a
// failure. Anything that does not decode as a JSON object — the Workers list is
// an array — is passed through untouched.
func errorEnvelope(raw []byte, err error) error {
	if err != nil {
		return err
	}

	var env wireStatusEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return nil
	}
	if !strings.EqualFold(env.Status, "Error") {
		return nil
	}

	msg := env.Message
	if msg == "" {
		msg = "the API reported an error without a message"
	}
	// Kala's messages are in Danish and are the most specific explanation
	// available, so they are surfaced verbatim rather than paraphrased.
	return fmt.Errorf("kala rejected the request: %s", msg)
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
	return c.requestWithContentType(ctx, method, path, body, contentTypeHeader, headers)
}

// requestWithContentType is request() with an explicit Content-Type, because
// SendWorkerWelcomeEmail takes a form-encoded body while everything else on
// this API takes JSON.
func (c *internalAPI) requestWithContentType(
	ctx context.Context, method, path string, body []byte, contentType string, headers map[string]string,
) ([]byte, error) {
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
		req.Header.Set("Content-Type", contentType)
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
	raw, err := c.authedRequest(ctx, http.MethodGet, "/api/Workers/", nil, contentTypeHeader)
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
	body, err := json.Marshal(wireSetValidatedRequest{WorkerNr: workerNr, IsValidated: validated})
	if err != nil {
		return fmt.Errorf("kala: building SetValidated request: %w", err)
	}

	if _, err := c.authedRequest(ctx, http.MethodPost, "/api/SetValidated/", body, contentTypeHeader); err != nil {
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

	body, err := json.Marshal(wireSignUpRequest{
		MedarbejderNr: in.Number,
		Email:         in.Email,
		Name:          in.Name,
	})
	if err != nil {
		return Worker{}, fmt.Errorf("kala: building sign-up request: %w", err)
	}

	if _, err := c.authedRequest(ctx, http.MethodPost, "/Api/SignUp/", body, contentTypeHeader); err != nil {
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
	raw, err := c.authedRequest(ctx, http.MethodGet,
		"/api/WorkerInfo/?workerNr="+strconv.FormatInt(workerNr, 10), nil, contentTypeHeader)
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

// SetWorkerEmail changes a worker's email address.
//
// Verified by read-back like every internal-API write (ARCH1.8): the endpoint
// returning 200 is its claim, not proof. WorkerInfo is the confirmation, and it
// is also the only place email is readable at all.
func (c *internalAPI) SetWorkerEmail(ctx context.Context, workerNr int64, email string) error {
	if email == "" {
		return fmt.Errorf("email must not be empty")
	}

	body, err := json.Marshal(wireSetEmailRequest{WorkerNr: workerNr, Email: email})
	if err != nil {
		return fmt.Errorf("kala: building SetEmail request: %w", err)
	}

	if _, err := c.authedRequest(ctx, http.MethodPost, "/api/SetEmailNew/", body, contentTypeHeader); err != nil {
		return fmt.Errorf("kala: setting email for worker %d: %w", workerNr, err)
	}

	info, err := c.GetWorkerInfo(ctx, workerNr)
	if err != nil {
		return fmt.Errorf("kala: could not verify the email change for worker %d: %w", workerNr, err)
	}
	if !strings.EqualFold(info.Email, email) {
		// This is now the backstop rather than the first line of defence.
		// SetEmailNew reports a refusal in the response body — see
		// errorEnvelope — so a caller normally gets Kala's own explanation,
		// such as being unable to change the email of a user attached to more
		// than one company. Reaching here means the write was accepted, was not
		// reported as an error, and still did not take effect.
		return fmt.Errorf(
			"kala: SetEmail for worker %d reported success but the address reads back as %q, "+
				"expected %q, and Kala gave no reason",
			workerNr, info.Email, email)
	}

	return nil
}

// wireStatusResponse is the {"status": bool} shape SendWorkerWelcomeEmail returns.
type wireStatusResponse struct {
	Status *bool `json:"status"`
}

// SendWelcomeEmail sends Kala's onboarding email.
//
// Two things make this endpoint unlike the others: the body is
// form-encoded rather than JSON, and it is keyed on the email address rather
// than on workerNr.
//
// It also cannot be verified by read-back — sending an email leaves nothing to
// re-read — so the endpoint's own {"status": true} is the only confirmation
// available, and a false status is treated as failure.
func (c *internalAPI) SendWelcomeEmail(ctx context.Context, email string) error {
	if strings.TrimSpace(email) == "" {
		return fmt.Errorf("email must not be empty")
	}

	form := url.Values{}
	form.Set("email", email)

	raw, err := c.authedRequest(ctx,
		http.MethodPost, "/api/SendWorkerWelcomeEmail",
		[]byte(form.Encode()),
		"application/x-www-form-urlencoded;charset=UTF-8")
	if err != nil {
		return fmt.Errorf("kala: sending the welcome email: %w", err)
	}

	var resp wireStatusResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("%w: welcome-email response: %v", ErrDecode, err)
	}
	if resp.Status != nil && !*resp.Status {
		return fmt.Errorf("kala: the welcome email was not sent (the API reported status false)")
	}

	return nil
}
