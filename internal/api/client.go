// Package api implements a thin Zabbix JSON-RPC 2.0 client.
//
// The client deliberately exposes a single generic Call method rather than a
// typed method per Zabbix object: the API surface is large, version-dependent
// and better absorbed by the service layer above.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

// DefaultMaxResponseBytes bounds a single API response. Zabbix will happily
// return tens of megabytes of history; refusing early keeps memory and agent
// context predictable.
const DefaultMaxResponseBytes = 32 << 20

// Client talks to a single Zabbix API endpoint.
type Client struct {
	endpoint  string
	token     string
	userAgent string
	http      *http.Client

	maxResponseBytes int64
	maxRetries       int
	logf             func(format string, args ...any)
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the underlying HTTP client. Used by tests and by the
// TLS configuration path.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.http = hc } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// WithMaxResponseBytes bounds the accepted response size.
func WithMaxResponseBytes(n int64) Option { return func(c *Client) { c.maxResponseBytes = n } }

// WithLogger installs a debug logger. The logger never receives credentials:
// request bodies are redacted before they reach it.
func WithLogger(logf func(format string, args ...any)) Option {
	return func(c *Client) { c.logf = logf }
}

// WithMaxRetries bounds retries of idempotent calls.
func WithMaxRetries(n int) Option { return func(c *Client) { c.maxRetries = n } }

// New returns a Client for the given base URL. The URL may point either at the
// Zabbix frontend root or directly at api_jsonrpc.php.
func New(baseURL, token string, opts ...Option) *Client {
	c := &Client{
		endpoint:         normaliseEndpoint(baseURL),
		token:            token,
		userAgent:        "zabbix-ai-cli-mcp",
		http:             &http.Client{Timeout: 30 * time.Second},
		maxResponseBytes: DefaultMaxResponseBytes,
		maxRetries:       2,
	}
	for _, o := range opts {
		o(c)
	}
	// Options are internal today, but keeping invalid values from turning into
	// panics or silently disabling the response bound makes the client safe to
	// reuse from future front ends.
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	if c.maxResponseBytes <= 0 {
		c.maxResponseBytes = DefaultMaxResponseBytes
	}
	if c.maxRetries < 0 {
		c.maxRetries = 0
	}
	return c
}

// Endpoint returns the resolved JSON-RPC endpoint.
func (c *Client) Endpoint() string { return c.endpoint }

func normaliseEndpoint(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, ".php") {
		return base
	}
	return strings.TrimRight(base, "/") + "/api_jsonrpc.php"
}

// unauthenticatedMethods must be called with no authorization header.
var unauthenticatedMethods = map[string]bool{"apiinfo.version": true}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
	ID      int    `json:"id"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *APIError       `json:"error"`
	ID      int             `json:"id"`
}

// APIError is an error reported by Zabbix itself.
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

func (e *APIError) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("zabbix api error %d: %s %s", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("zabbix api error %d: %s", e.Code, e.Message)
}

// Authentication reports whether the error means the token was rejected.
func (e *APIError) Authentication() bool {
	if e.Code == -32602 && strings.Contains(strings.ToLower(e.Data), "re-login") {
		return true
	}
	l := strings.ToLower(e.Message + " " + e.Data)
	return strings.Contains(l, "not authorised") ||
		strings.Contains(l, "not authorized") ||
		strings.Contains(l, "authentication failed") ||
		strings.Contains(l, "session terminated")
}

// Permission reports whether the error means the token lacks permission.
func (e *APIError) Permission() bool {
	l := strings.ToLower(e.Message + " " + e.Data)
	return strings.Contains(l, "no permissions") || strings.Contains(l, "permission denied")
}

// TransportError wraps a network or protocol level failure.
type TransportError struct {
	Op  string
	Err error
}

func (e *TransportError) Error() string { return e.Op + ": " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// Call performs a single request. It never retries: the caller cannot know
// whether a write reached Zabbix before the connection failed.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	return c.do(ctx, method, params, result, 0)
}

// CallIdempotent performs a request, retrying transport failures and HTTP 5xx
// with jittered backoff. Only safe for reads.
func (c *Client) CallIdempotent(ctx context.Context, method string, params, result any) error {
	return c.do(ctx, method, params, result, c.maxRetries)
}

func (c *Client) do(ctx context.Context, method string, params, result any, retries int) error {
	if c.endpoint == "" {
		return &TransportError{Op: "call " + method, Err: errors.New("no Zabbix URL configured")}
	}
	if params == nil {
		params = map[string]any{}
	}
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	if err != nil {
		return &TransportError{Op: "encode " + method, Err: err}
	}
	c.debugf("-> %s %s", method, redactBody(body))

	var lastErr error
	for attempt := 0; ; attempt++ {
		err := c.attempt(ctx, method, body, result)
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt >= retries || !retryable(err) {
			return lastErr
		}
		delay := backoff(attempt)
		select {
		case <-ctx.Done():
			return &TransportError{Op: "call " + method, Err: ctx.Err()}
		case <-time.After(delay):
		}
	}
}

func (c *Client) attempt(ctx context.Context, method string, body []byte, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return &TransportError{Op: "build request " + method, Err: err}
	}
	req.Header.Set("Content-Type", "application/json-rpc")
	req.Header.Set("User-Agent", c.userAgent)
	// Zabbix rejects apiinfo.version outright when an authorization header is
	// present: "must be called without authorization header". It is the one
	// method that has to go out unauthenticated, which also makes it a clean
	// connectivity probe.
	if c.token != "" && !unauthenticatedMethods[method] {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return &TransportError{Op: "call " + method, Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return &TransportError{Op: "read response " + method, Err: err}
	}
	if int64(len(raw)) > c.maxResponseBytes {
		return &TransportError{
			Op:  "read response " + method,
			Err: fmt.Errorf("response exceeds %d bytes; narrow the query", c.maxResponseBytes),
		}
	}
	if resp.StatusCode != http.StatusOK {
		return &httpStatusError{Status: resp.StatusCode, Method: method, Body: snippet(raw)}
	}

	var rr rpcResponse
	if err := json.Unmarshal(raw, &rr); err != nil {
		return &TransportError{
			Op:  "decode response " + method,
			Err: fmt.Errorf("%w (body: %s)", err, snippet(raw)),
		}
	}
	if rr.Error != nil {
		c.debugf("<- %s error %d %s", method, rr.Error.Code, rr.Error.Message)
		return rr.Error
	}
	if len(rr.Result) == 0 {
		return &TransportError{
			Op:  "decode response " + method,
			Err: errors.New("JSON-RPC response contains neither result nor error"),
		}
	}
	c.debugf("<- %s ok (%d bytes)", method, len(rr.Result))
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(rr.Result, result); err != nil {
		return &TransportError{Op: "decode result " + method, Err: err}
	}
	return nil
}

type httpStatusError struct {
	Status int
	Method string
	Body   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("call %s: HTTP %d: %s", e.Method, e.Status, e.Body)
}

// HTTPStatus reports the HTTP status carried by err, or 0.
func HTTPStatus(err error) int {
	var he *httpStatusError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

func retryable(err error) bool {
	var te *TransportError
	if errors.As(err, &te) {
		return !errors.Is(te.Err, context.Canceled) && !errors.Is(te.Err, context.DeadlineExceeded)
	}
	if s := HTTPStatus(err); s >= 500 || s == http.StatusTooManyRequests {
		return true
	}
	return false
}

func backoff(attempt int) time.Duration {
	base := time.Duration(200<<attempt) * time.Millisecond
	if base > 3*time.Second {
		base = 3 * time.Second
	}
	return base + time.Duration(rand.N(int64(base/2)+1))
}

func snippet(b []byte) string {
	const max = 200
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

func (c *Client) debugf(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}
