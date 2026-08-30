// Package config loads and stores non-secret configuration: the profile list,
// each profile's Zabbix URL and the scopes it is allowed to use.
//
// Credentials never appear here. They are resolved separately by
// internal/auth, so that a config file can be read, diffed and committed
// without leaking anything.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
)

// Scopes a profile can grant. Read is implicit and always present.
const (
	ScopeRead          = "read"
	ScopeAcknowledge   = "acknowledge"
	ScopeMaintenance   = "maintenance"
	ScopeConfiguration = "configuration"
)

// KnownScopes lists every scope the risk registry recognises.
var KnownScopes = []string{ScopeRead, ScopeAcknowledge, ScopeMaintenance, ScopeConfiguration}

// Profile describes one Zabbix installation.
type Profile struct {
	URL string `toml:"url"`
	// Scopes limits which risk classes may be planned against this profile.
	// Absent means read-only.
	Scopes []string `toml:"scopes,omitempty"`
	// Insecure disables TLS verification. Opt-in, never a default.
	Insecure bool `toml:"insecure,omitempty"`
	// CAFile supplies a custom certificate authority, preferred over Insecure.
	CAFile string `toml:"ca_file,omitempty"`
	// TimeoutSeconds bounds a single API call.
	TimeoutSeconds int `toml:"timeout_seconds,omitempty"`
	// TokenFile names a file holding the API token, for headless deployments.
	TokenFile string `toml:"token_file,omitempty"`
	// Keyring stores the token in the OS keyring when true.
	Keyring bool `toml:"keyring,omitempty"`
	// AllowWrite overrides the file-wide setting for this profile. Nil means
	// "not stated here", which is why it is a pointer: a plain bool cannot
	// tell an absent key from an explicit false.
	AllowWrite *bool `toml:"allow_write,omitempty"`
}

// HasScope reports whether the profile grants scope. Read is always granted.
func (p Profile) HasScope(scope string) bool {
	if scope == "" || scope == ScopeRead {
		return true
	}
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// EffectiveScopes reports the scopes a profile grants once direct writing is
// taken into account.
//
// A profile that names its scopes gets exactly those, whether or not writing
// is allowed. A profile that names none inherits every scope while writing is
// allowed, because the alternative is a default of "allowed" that permits
// nothing and reads as broken.
func EffectiveScopes(p Profile, writeAllowed bool) []string {
	if len(p.Scopes) > 0 {
		// Read is implicit and carries no permission, so a profile listing
		// only "read" is a profile that has been narrowed to nothing. That is
		// how a profile says "explicitly none" in a file where an absent key
		// means "unstated".
		out := make([]string, 0, len(p.Scopes))
		for _, s := range p.Scopes {
			if s != ScopeRead {
				out = append(out, s)
			}
		}
		return out
	}
	if !writeAllowed {
		return nil
	}
	return WriteScopes()
}

// WriteScopes lists every scope that permits a change.
func WriteScopes() []string {
	return []string{ScopeAcknowledge, ScopeMaintenance, ScopeConfiguration}
}

// GrantsScope reports whether a profile may act in scope, given whether direct
// writing is allowed. Read is always granted.
func GrantsScope(p Profile, writeAllowed bool, scope string) bool {
	if scope == "" || scope == ScopeRead {
		return true
	}
	for _, s := range EffectiveScopes(p, writeAllowed) {
		if s == scope {
			return true
		}
	}
	return false
}

// Config is the whole configuration file.
type Config struct {
	ActiveProfile string `toml:"active_profile"`
	// AllowWrite decides whether a change may be applied directly, without a
	// stored plan being approved at a terminal. Absent means allowed: the
	// tool is useful out of the box, and an installation that wants the
	// stricter posture says so.
	AllowWrite *bool              `toml:"allow_write,omitempty"`
	Profiles   map[string]Profile `toml:"profiles"`

	path string
}

// Path reports where the config was loaded from.
func (c *Config) Path() string { return c.path }

// Names returns profile names in a stable order.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Resolve returns the profile to use. An explicit name wins over the
// ZABBIX_AI_CLI_MCP_PROFILE environment variable, which wins over active_profile.
func (c *Config) Resolve(explicit string) (string, Profile, error) {
	name := explicit
	if name == "" {
		name = os.Getenv(EnvProfile)
	}
	if name == "" {
		name = c.ActiveProfile
	}
	if name == "" {
		// A URL supplied purely through the environment needs no profile.
		if os.Getenv(EnvURL) != "" {
			return "env", Profile{URL: os.Getenv(EnvURL)}, nil
		}
		return "", Profile{}, errs.New(errs.CodeNoProfile, errs.ExitAuth,
			"no Zabbix profile is configured").
			WithSuggestion("run 'zabbix-ai-cli-mcp login' to create one, or set %s and %s", EnvURL, EnvToken)
	}
	p, ok := c.Profiles[name]
	if !ok {
		if os.Getenv(EnvURL) != "" {
			return name, Profile{URL: os.Getenv(EnvURL)}, nil
		}
		return "", Profile{}, errs.New(errs.CodeNoProfile, errs.ExitAuth,
			"profile %q does not exist", name).
			WithSuggestion("known profiles: %s", strings.Join(c.Names(), ", "))
	}
	if u := os.Getenv(EnvURL); u != "" {
		p.URL = u
	}
	return name, p, nil
}

// WriteAllowed reports whether changes may be applied directly against this
// profile, without a stored plan being approved at a terminal.
//
// The environment wins over the profile, which wins over the file-wide
// setting, which defaults to allowed. A container is configured through its
// environment and a config file it may not even own, so the environment has to
// be able to say "not here" without the file agreeing.
func (c *Config) WriteAllowed(p Profile) (bool, error) {
	if v, ok := os.LookupEnv(EnvAllowWrite); ok {
		b, err := parseBoolSetting(v)
		if err != nil {
			return false, errs.Usage("%s is set to %q, which is not a true/false value", EnvAllowWrite, v)
		}
		return b, nil
	}
	if p.AllowWrite != nil {
		return *p.AllowWrite, nil
	}
	if c.AllowWrite != nil {
		return *c.AllowWrite, nil
	}
	return true, nil
}

// parseBoolSetting accepts the spellings a person actually types in a shell
// rather than only the ones Go's strconv knows.
func parseBoolSetting(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "y", "yes", "on":
		return true, nil
	case "0", "f", "false", "n", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("not a boolean")
}

// Bool boxes a literal for the pointer-valued settings.
func Bool(v bool) *bool { return &v }

// Environment variables. Documented in docs/authentication.md.
const (
	EnvURL        = "ZABBIX_AI_CLI_MCP_URL"
	EnvToken      = "ZABBIX_AI_CLI_MCP_TOKEN"
	EnvTokenFile  = "ZABBIX_AI_CLI_MCP_TOKEN_FILE"
	EnvProfile    = "ZABBIX_AI_CLI_MCP_PROFILE"
	EnvConfigDir  = "ZABBIX_AI_CLI_MCP_CONFIG_DIR"
	EnvStateDir   = "ZABBIX_AI_CLI_MCP_STATE_DIR"
	EnvAllowWrite = "ZABBIX_AI_CLI_MCP_ALLOW_WRITE"
)

const appName = "zabbix-ai-cli-mcp"

// Dir returns the configuration directory, honouring XDG on Unix and the
// platform convention elsewhere.
func Dir() (string, error) {
	if d := os.Getenv(EnvConfigDir); d != "" {
		return d, nil
	}
	if runtime.GOOS != "windows" {
		if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
			return filepath.Join(x, appName), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", appName), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName), nil
}

// StateDir returns the directory for plans and the audit log.
func StateDir() (string, error) {
	if d := os.Getenv(EnvStateDir); d != "" {
		return d, nil
	}
	if runtime.GOOS != "windows" {
		if x := os.Getenv("XDG_STATE_HOME"); x != "" {
			return filepath.Join(x, appName), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "state", appName), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName, "state"), nil
}

// Load reads the configuration, returning an empty one if the file is absent.
func Load() (*Config, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.toml")
	c := &Config{Profiles: map[string]Profile{}, path: path}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, c); err != nil {
		return nil, errs.Usage("config file %s is not valid TOML: %v", path, err)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	c.path = path
	return c, nil
}

// Save writes the configuration atomically with 0600 permissions. Although it
// holds no secrets, the URL list is still worth keeping private.
func Save(c *Config) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, "config.toml")
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(c); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	c.path = path
	return nil
}

// ValidateScopes rejects unknown scope names early, where the message can name
// the permitted set.
func ValidateScopes(scopes []string) error {
	for _, s := range scopes {
		known := false
		for _, k := range KnownScopes {
			if s == k {
				known = true
				break
			}
		}
		if !known {
			return errs.Usage("unknown scope %q; permitted scopes are %s", s, strings.Join(KnownScopes, ", "))
		}
	}
	return nil
}
