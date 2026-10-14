package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/config"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
)

// emptyInstall points the configuration at directories holding nothing, with
// every connection setting cleared, which is what the container image looks
// like when it is started with no environment.
func emptyInstall(t *testing.T) *globals {
	t.Helper()
	t.Setenv(config.EnvConfigDir, t.TempDir())
	t.Setenv(config.EnvStateDir, t.TempDir())
	for _, k := range []string{config.EnvURL, config.EnvToken, config.EnvTokenFile,
		config.EnvProfile, config.EnvAllowWrite, EnvIntrospection} {
		// Setenv first so the test restores the variable, then unset it:
		// the write setting tells an empty value from an absent one.
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	return &globals{stderr: &bytes.Buffer{}}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *errs.E
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func TestIntrospectionServesAnEmptyInstallOverStdio(t *testing.T) {
	g := emptyInstall(t)
	t.Setenv(EnvIntrospection, "1")

	allowWrite, err := g.mcpStartup(context.Background(), true)
	if err != nil {
		t.Fatalf("startup: %v", err)
	}
	if !allowWrite {
		t.Error("an empty install defaults to writes allowed, so the full tool list is shown")
	}
	if !strings.Contains(g.stderr.(*bytes.Buffer).String(), "tool list only") {
		t.Errorf("stderr should say the server is not connected: %q", g.stderr)
	}
	// The tool calls still build their environment and fail on it.
	_, err = g.buildEnv(context.Background())
	wantCode(t, err, errs.CodeNoProfile)
}

func TestIntrospectionHonoursAllowWrite(t *testing.T) {
	g := emptyInstall(t)
	t.Setenv(EnvIntrospection, "1")
	t.Setenv(config.EnvAllowWrite, "false")

	allowWrite, err := g.mcpStartup(context.Background(), true)
	if err != nil || allowWrite {
		t.Fatalf("allowWrite = %v, err = %v; want false, nil", allowWrite, err)
	}
}

func TestEmptyInstallStillFailsAtStartup(t *testing.T) {
	cases := map[string]struct {
		stdio bool
		setup func(t *testing.T, g *globals)
		code  string
	}{
		"without the setting": {stdio: true, code: errs.CodeNoProfile,
			setup: func(t *testing.T, _ *globals) {}},
		"over HTTP": {stdio: false, code: errs.CodeNoProfile,
			setup: func(t *testing.T, _ *globals) { t.Setenv(EnvIntrospection, "1") }},
		"with a value other than 1": {stdio: true, code: errs.CodeNoProfile,
			setup: func(t *testing.T, _ *globals) { t.Setenv(EnvIntrospection, "yes") }},
		"with a URL but no token": {stdio: true, code: errs.CodeAuth,
			setup: func(t *testing.T, _ *globals) {
				t.Setenv(EnvIntrospection, "1")
				t.Setenv(config.EnvURL, "https://zabbix.example.com")
			}},
		"with a missing token file": {stdio: true, code: errs.CodeAuth,
			setup: func(t *testing.T, _ *globals) {
				t.Setenv(EnvIntrospection, "1")
				t.Setenv(config.EnvURL, "https://zabbix.example.com")
				t.Setenv(config.EnvTokenFile, "/nonexistent/token")
			}},
		"with a profile named that does not exist": {stdio: true, code: errs.CodeNoProfile,
			setup: func(t *testing.T, g *globals) {
				t.Setenv(EnvIntrospection, "1")
				g.profile = "prod"
			}},
		"with the profile variable naming a missing profile": {stdio: true, code: errs.CodeNoProfile,
			setup: func(t *testing.T, _ *globals) {
				t.Setenv(EnvIntrospection, "1")
				t.Setenv(config.EnvProfile, "prod")
			}},
		"with a token but no URL": {stdio: true, code: errs.CodeNoProfile,
			setup: func(t *testing.T, _ *globals) {
				t.Setenv(EnvIntrospection, "1")
				t.Setenv(config.EnvToken, "not-a-key")
			}},
		"with a token file but no URL": {stdio: true, code: errs.CodeNoProfile,
			setup: func(t *testing.T, _ *globals) {
				t.Setenv(EnvIntrospection, "1")
				t.Setenv(config.EnvTokenFile, "/run/secrets/zabbix")
			}},
		"with profiles but none active": {stdio: true, code: errs.CodeNoProfile,
			setup: func(t *testing.T, _ *globals) {
				t.Setenv(EnvIntrospection, "1")
				if err := config.Save(&config.Config{Profiles: map[string]config.Profile{
					"prod": {URL: "https://zabbix.example.com"}}}); err != nil {
					t.Fatal(err)
				}
			}},
		"with an active profile that does not exist": {stdio: true, code: errs.CodeNoProfile,
			setup: func(t *testing.T, _ *globals) {
				t.Setenv(EnvIntrospection, "1")
				if err := config.Save(&config.Config{ActiveProfile: "prod",
					Profiles: map[string]config.Profile{}}); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := emptyInstall(t)
			tc.setup(t, g)
			_, err := g.mcpStartup(context.Background(), tc.stdio)
			wantCode(t, err, tc.code)
		})
	}
}
