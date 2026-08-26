package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func TestCallSendsBearerAndDecodesResult(t *testing.T) {
	var gotAuth, gotCT, gotUA string
	var gotBody []byte
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotUA = r.Header.Get("User-Agent")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","result":["a","b"],"id":1}`)
	})

	c := New(s.URL, "sekrit", WithUserAgent("zabbix-ai-cli-mcp/test"))
	var out []string
	if err := c.Call(context.Background(), "host.get", map[string]any{"output": "extend"}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(out) != 2 || out[0] != "a" {
		t.Fatalf("result = %v", out)
	}
	if gotAuth != "Bearer sekrit" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotCT != "application/json-rpc" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if gotUA != "zabbix-ai-cli-mcp/test" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	var req map[string]any
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if req["jsonrpc"] != "2.0" || req["method"] != "host.get" {
		t.Errorf("request = %v", req)
	}
	if _, ok := req["auth"]; ok {
		t.Error("request must not carry a legacy auth parameter on 6.4+")
	}
}

func TestEndpointNormalisation(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://z.example.com", "https://z.example.com/api_jsonrpc.php"},
		{"https://z.example.com/", "https://z.example.com/api_jsonrpc.php"},
		{"https://z.example.com/zabbix", "https://z.example.com/zabbix/api_jsonrpc.php"},
		{"https://z.example.com/api_jsonrpc.php", "https://z.example.com/api_jsonrpc.php"},
	} {
		if got := New(tc.in, "").Endpoint(); got != tc.want {
			t.Errorf("New(%q).Endpoint() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAPIErrorClassification(t *testing.T) {
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","error":{"code":-32602,"message":"Invalid params.","data":"Not authorised."},"id":1}`)
	})
	c := New(s.URL, "t")
	err := c.Call(context.Background(), "host.get", nil, nil)
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if !ae.Authentication() {
		t.Error("Not authorised must classify as an authentication failure")
	}
	if ae.Permission() {
		t.Error("must not classify as a permission failure")
	}
}

func TestCallDoesNotRetryWrites(t *testing.T) {
	var calls atomic.Int32
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	})
	c := New(s.URL, "t", WithMaxRetries(3))
	if err := c.Call(context.Background(), "maintenance.create", nil, nil); err == nil {
		t.Fatal("want error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("a write was attempted %d times; it must be attempted once", got)
	}
}

func TestCallIdempotentRetriesServerErrors(t *testing.T) {
	var calls atomic.Int32
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","result":[],"id":1}`)
	})
	c := New(s.URL, "t", WithMaxRetries(3))
	var out []any
	if err := c.CallIdempotent(context.Background(), "host.get", nil, &out); err != nil {
		t.Fatalf("CallIdempotent: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestResponseSizeIsBounded(t *testing.T) {
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","result":"`+strings.Repeat("x", 5000)+`","id":1}`)
	})
	c := New(s.URL, "t", WithMaxResponseBytes(1000))
	err := c.Call(context.Background(), "history.get", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want a size-limit error, got %v", err)
	}
}

func TestContextCancellationIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","result":[],"id":1}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	c := New(s.URL, "t", WithMaxRetries(3))
	if err := c.CallIdempotent(ctx, "host.get", nil, nil); err == nil {
		t.Fatal("want a timeout error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("a cancelled call was retried %d times", got)
	}
}

func TestMalformedSuccessWithoutResultIsRejected(t *testing.T) {
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1}`)
	})
	c := New(s.URL, "token", WithMaxRetries(0))
	if err := c.Call(context.Background(), "host.get", nil, nil); err == nil {
		t.Fatal("response without result or error was accepted")
	}
}

func TestInvalidOptionsFallBackToSafeDefaults(t *testing.T) {
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","result":[],"id":1}`)
	})
	c := New(s.URL, "token", WithHTTPClient(nil), WithMaxResponseBytes(0), WithMaxRetries(-1))
	// A nil client is repaired rather than panicking. Point it at the local
	// test transport after construction so the assertion does not use DNS.
	c.http = s.Client()
	var out []any
	if err := c.CallIdempotent(context.Background(), "host.get", nil, &out); err != nil {
		t.Fatalf("safe defaults failed: %v", err)
	}
}

func TestDebugLogNeverCarriesCredentials(t *testing.T) {
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","result":[],"id":1}`)
	})
	var log strings.Builder
	c := New(s.URL, "super-secret-token", WithLogger(func(f string, a ...any) {
		log.WriteString(strings.TrimSpace(sprintf(f, a...)) + "\n")
	}))
	params := map[string]any{
		"user":     "admin",
		"password": "hunter2",
		"nested":   map[string]any{"api_token": "abc123", "output": "extend"},
	}
	if err := c.Call(context.Background(), "user.login", params, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	got := log.String()
	for _, forbidden := range []string{"super-secret-token", "hunter2", "abc123"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("debug log leaked %q:\n%s", forbidden, got)
		}
	}
	if !strings.Contains(got, Redacted) {
		t.Errorf("debug log did not mark redaction:\n%s", got)
	}
	if !strings.Contains(got, "extend") {
		t.Errorf("redaction removed non-sensitive fields:\n%s", got)
	}
}

func TestRedactBodyHandlesUnparsableInput(t *testing.T) {
	if got := redactBody([]byte("not json")); !strings.Contains(got, "omitted") {
		t.Errorf("redactBody(garbage) = %q", got)
	}
}
