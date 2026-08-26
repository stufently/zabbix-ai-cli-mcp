package mcp

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
)

// ServeStdio runs the server over stdio, which is the primary transport.
//
// A user-scope HTTP endpoint is not visible to every context this tool runs
// in; a subprocess speaking stdio always is, and it never puts a listening
// socket on the machine.
func ServeStdio(ctx context.Context, server *sdk.Server) error {
	return server.Run(ctx, &sdk.StdioTransport{})
}

// HTTPOptions configure the optional network transport.
type HTTPOptions struct {
	// Addr is the listen address. It must resolve to a loopback interface
	// unless AllowNonLoopback is set.
	Addr string
	// BearerToken authenticates MCP clients. It is unrelated to the Zabbix
	// token, which never leaves this process.
	BearerToken string
	// AllowNonLoopback permits binding a routable address. Doing so exposes
	// Zabbix to anything that can reach the port.
	AllowNonLoopback bool
	// TrustedOrigins are browser origins permitted to call the endpoint.
	TrustedOrigins []string
	Log            io.Writer
}

// maxRequestBody bounds a single MCP request.
const maxRequestBody = 4 << 20

// ServeHTTP runs the server over streamable HTTP.
//
// The SDK applies no cross-origin protection of its own, so the handler is
// wrapped here. Authentication is required whenever the address is not
// loopback, and offered even when it is.
func ServeHTTP(ctx context.Context, server *sdk.Server, opts HTTPOptions) error {
	host, _, err := net.SplitHostPort(opts.Addr)
	if err != nil {
		return errs.Usage("--http must be host:port, for example 127.0.0.1:8000")
	}
	loopback := isLoopback(host)
	if !loopback && !opts.AllowNonLoopback {
		what := opts.Addr
		if host == "" {
			what = opts.Addr + " (an empty host means every interface, not localhost)"
		}
		return errs.Usage("refusing to listen on %s, which is not a loopback address", what).
			WithSuggestion("bind 127.0.0.1, or pass --allow-remote together with --bearer-token if you really mean to expose it")
	}
	if !loopback && opts.BearerToken == "" {
		return errs.Usage("a bearer token is required when listening on a routable address").
			WithSuggestion("set --bearer-token, or bind 127.0.0.1 instead")
	}

	handler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{
			Stateless:      true,
			JSONResponse:   true,
			SessionTimeout: 10 * time.Minute,
		},
	)

	protection := http.NewCrossOriginProtection()
	for _, origin := range opts.TrustedOrigins {
		if err := protection.AddTrustedOrigin(origin); err != nil {
			return errs.Usage("invalid trusted origin %q: %v", origin, err)
		}
	}

	var root http.Handler = handler
	root = limitBody(root)
	if opts.BearerToken != "" {
		root = requireBearer(root, opts.BearerToken)
	}
	root = protection.Handler(root)

	srv := &http.Server{
		Addr:              opts.Addr,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 16,
	}
	listener, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", opts.Addr, err)
	}
	if opts.Log != nil {
		fmt.Fprintf(opts.Log, "mcp: listening on http://%s\n", listener.Addr())
		if opts.BearerToken == "" {
			fmt.Fprintf(opts.Log, "mcp: no bearer token set; any local process can use this endpoint\n")
		}
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// isLoopback reports whether a listen host reaches only this machine.
//
// An empty host is NOT loopback: net/http reads ":8000" as every interface, so
// treating it as local would hand out an unauthenticated port on a routable
// address. A name is loopback only when every address it resolves to is, so a
// "localhost" pointed elsewhere in /etc/hosts cannot smuggle past the check.
func isLoopback(host string) bool {
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	addrs, err := net.LookupIP(host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, ip := range addrs {
		if !ip.IsLoopback() {
			return false
		}
	}
	return true
}

// requireBearer authenticates a client with a constant-time comparison, so the
// endpoint does not leak the token one byte at a time through response timing.
func requireBearer(next http.Handler, token string) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeEq(int32(len(got)), int32(len(want))) != 1 ||
			subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="zabbix-ai-cli-mcp"`)
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		next.ServeHTTP(w, r)
	})
}
