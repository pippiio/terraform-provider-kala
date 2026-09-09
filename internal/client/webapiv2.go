package client

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Defaults chosen conservatively: the API documents no rate limits, so the
// client must not be the reason one is discovered.
const (
	DefaultEndpoint    = "https://app.kala.dk/webapiv2"
	defaultTimeout     = 30 * time.Second
	defaultMaxRetries  = 3
	defaultRetryBase   = 200 * time.Millisecond
	maxRetryBackoff    = 10 * time.Second
	defaultPageSize    = 500 // well below the API's documented default of 5000
	defaultMaxPages    = 100 // guarantees termination
	maxErrorBodySample = 512 // bounded: an error body of unknown shape is never fully buffered
)

// Config configures a webapiv2 client. Zero values fall back to the defaults
// above.
type Config struct {
	Endpoint   string
	APIKey     string
	Company    int64
	Timeout    time.Duration
	MaxRetries int

	// HTTPClient allows tests to inject a transport. Nil means a client built
	// from Timeout.
	HTTPClient *http.Client

	// retryBaseDur is unexported so only tests within this package can shrink
	// the backoff. Callers get the production value.
	retryBaseDur time.Duration
}

func (c Config) withDefaults() Config {
	if c.Endpoint == "" {
		c.Endpoint = DefaultEndpoint
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

// webAPIv2 is the documented public API implementation. It carries every write
// path the provider will ever use; the internal app API is read-only by policy
// .
type webAPIv2 struct {
	cfg Config
}

// Compile-time proof that the implementation satisfies the domain interface.
var _ Client = (*webAPIv2)(nil)

func newWebAPIv2(cfg Config) *webAPIv2 {
	return &webAPIv2{cfg: cfg.withDefaults()}
}

// New returns a Client backed by the documented webapiv2 API.
func New(cfg Config) Client {
	return newWebAPIv2(cfg)
}

// authParam returns the query-parameter name carrying the API key for a given
// endpoint.
//
// The API is inconsistent with itself: Index spells it "apikey" while every
// other endpoint spells it "api_key". Hardcoding one spelling is a defect.
func authParam(endpoint string) string {
	if endpoint == "Index" {
		return "apikey"
	}
	return "api_key"
}

// get performs a GET against an endpoint with bounded retries, returning the
// raw body on success.
//
// Retries use exponential backoff and are honoured only for server and
// transport failures. The body is read fully only on 2xx; on any other status
// the body is sampled for context but never decoded.
func (c *webAPIv2) get(ctx context.Context, endpoint string, params url.Values) ([]byte, error) {
	return c.do(ctx, http.MethodGet, endpoint, params)
}

func (c *webAPIv2) do(ctx context.Context, method, endpoint string, params url.Values) ([]byte, error) {
	if params == nil {
		params = url.Values{}
	}
	params.Set(authParam(endpoint), c.cfg.APIKey)

	target := c.cfg.Endpoint + "/" + endpoint + "?" + params.Encode()

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		// Check cancellation before every attempt so an already-cancelled
		// context issues no request at all.
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return nil, fmt.Errorf("%w (after %d attempts): %w", err, attempt, lastErr)
			}
			return nil, err
		}

		body, err := c.attempt(ctx, method, target)
		if err == nil {
			return body, nil
		}
		lastErr = err

		if !isRetryable(err) {
			return nil, err
		}
		if attempt == c.cfg.MaxRetries {
			break
		}
		if err := sleepWithContext(ctx, backoffFor(attempt, c.cfg.retryBaseDur)); err != nil {
			return nil, fmt.Errorf("%w (after %d attempts): %w", err, attempt+1, lastErr)
		}
	}

	return nil, lastErr
}

// attempt performs exactly one HTTP request.
func (c *webAPIv2) attempt(ctx context.Context, method, target string) ([]byte, error) {
	// Context-carrying request so Terraform cancellation propagates.
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransport, sanitizeError(err))
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		// A cancelled context must surface as cancellation, not as a generic
		// transport failure, so callers can distinguish the two.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: %v", ErrTransport, sanitizeError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	return readBody(resp)
}

// readBody classifies the response and returns the body on success.
//
// Shared by both API clients. A non-2xx body is never decoded — its shape is
// undocumented — but a bounded, sanitized sample is carried as error context
// .
func readBody(resp *http.Response) ([]byte, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		sample, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySample))
		return nil, classifyStatus(resp.StatusCode, redactSensitiveValues(string(sample)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: reading response body: %v", ErrTransport, sanitizeError(err))
	}
	return body, nil
}

// backoffFor returns an exponentially increasing delay, capped.
func backoffFor(attempt int, base time.Duration) time.Duration {
	d := time.Duration(math.Pow(2, float64(attempt))) * base
	if d > maxRetryBackoff {
		return maxRetryBackoff
	}
	return d
}

// sleepWithContext waits for d, aborting early if ctx is cancelled.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Ping verifies the configured credentials.
func (c *webAPIv2) Ping(ctx context.Context) error {
	_, err := c.get(ctx, "Ping", nil)
	return err
}

// GetEmployee returns a single employee by number.
func (c *webAPIv2) GetEmployee(ctx context.Context, number int64) (Employee, error) {
	params := url.Values{}
	params.Set("employeeNumber", strconv.FormatInt(number, 10))

	body, err := c.get(ctx, "ActiveEmployee", params)
	if err != nil {
		return Employee{}, err
	}
	return decodeEmployee(body)
}

// ListEmployees returns active employees, following pagination.
//
// Termination is guaranteed three ways: a short page ends the loop, an empty
// page ends the loop, and MaxPages caps the total regardless.
func (c *webAPIv2) ListEmployees(ctx context.Context, opts ListOptions) ([]Employee, error) {
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	maxPages := opts.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}
	order := opts.Order
	if order == "" {
		order = "asc"
	}

	all := make([]Employee, 0, pageSize)
	for page := 0; page < maxPages; page++ {
		params := url.Values{}
		params.Set("order", order)
		params.Set("page", strconv.Itoa(page))
		params.Set("page_size", strconv.Itoa(pageSize))

		body, err := c.get(ctx, "ActiveEmployeesList", params)
		if err != nil {
			return nil, err
		}

		batch, err := decodeEmployeeList(body)
		if err != nil {
			return nil, err
		}

		all = append(all, batch...)

		if len(batch) < pageSize {
			return all, nil
		}
	}

	return all, fmt.Errorf("page cap of %d reached before the employee list was exhausted; raise MaxPages or narrow the query", maxPages)
}
