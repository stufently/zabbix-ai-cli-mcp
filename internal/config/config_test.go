package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	return dir
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	withConfigDir(t)
	c := &Config{
		ActiveProfile: "production",
		Profiles: map[string]Profile{
			"production": {URL: "https://z.example.com", Scopes: []string{ScopeMaintenance}},
			"staging":    {URL: "https://stage.example.com"},
		},
	}
	if err := Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ActiveProfile != "production" || len(got.Profiles) != 2 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	if !got.Profiles["production"].HasScope(ScopeMaintenance) {
		t.Error("maintenance scope was lost")
	}
}

func TestConfigFileIsNotWorldReadable(t *testing.T) {
	dir := withConfigDir(t)
	if err := Save(&Config{Profiles: map[string]Profile{"p": {URL: "https://x"}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("config.toml mode = %04o", perm)
	}
}

func TestLoadMissingFileIsEmptyNotAnError(t *testing.T) {
	withConfigDir(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Profiles) != 0 {
		t.Errorf("profiles = %v", c.Profiles)
	}
}

func TestScopesDefaultToReadOnly(t *testing.T) {
	p := Profile{URL: "https://x"}
	if !p.HasScope(ScopeRead) {
		t.Error("read must always be granted")
	}
	for _, s := range []string{ScopeMaintenance, ScopeAcknowledge, ScopeConfiguration} {
		if p.HasScope(s) {
			t.Errorf("scope %q must not be granted by default", s)
		}
	}
}

func TestResolvePrecedence(t *testing.T) {
	withConfigDir(t)
	c := &Config{
		ActiveProfile: "production",
		Profiles: map[string]Profile{
			"production": {URL: "https://prod"},
			"staging":    {URL: "https://stage"},
		},
	}

	name, _, err := c.Resolve("")
	if err != nil || name != "production" {
		t.Fatalf("active profile: %q %v", name, err)
	}

	t.Setenv(EnvProfile, "staging")
	name, p, err := c.Resolve("")
	if err != nil || name != "staging" || p.URL != "https://stage" {
		t.Fatalf("environment profile: %q %+v %v", name, p, err)
	}

	name, p, err = c.Resolve("production")
	if err != nil || name != "production" || p.URL != "https://prod" {
		t.Fatalf("explicit profile must win: %q %+v %v", name, p, err)
	}

	t.Setenv(EnvURL, "https://override")
	_, p, err = c.Resolve("production")
	if err != nil || p.URL != "https://override" {
		t.Fatalf("environment URL must override the profile: %+v %v", p, err)
	}
}

func TestResolveWithoutConfigurationExplainsItself(t *testing.T) {
	withConfigDir(t)
	c := &Config{Profiles: map[string]Profile{}}
	_, _, err := c.Resolve("")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "no Zabbix profile") {
		t.Errorf("message = %q", err.Error())
	}
}

func TestValidateScopesNamesThePermittedSet(t *testing.T) {
	err := ValidateScopes([]string{"read", "delete-everything"})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range KnownScopes {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must list scope %q: %s", want, err)
		}
	}
}

func TestWritesAreAllowedUnlessSaidOtherwise(t *testing.T) {
	c := &Config{Profiles: map[string]Profile{}}
	allowed, err := c.WriteAllowed(Profile{URL: "https://x"})
	if err != nil {
		t.Fatalf("WriteAllowed: %v", err)
	}
	if !allowed {
		t.Error("a config that says nothing must allow writes")
	}
}

func TestWriteSettingPrecedence(t *testing.T) {
	root := &Config{AllowWrite: Bool(false), Profiles: map[string]Profile{}}
	silent := Profile{URL: "https://x"}
	loud := Profile{URL: "https://x", AllowWrite: Bool(true)}

	if allowed, _ := root.WriteAllowed(silent); allowed {
		t.Error("a profile that says nothing inherits the file-wide setting")
	}
	if allowed, _ := root.WriteAllowed(loud); !allowed {
		t.Error("a profile's own setting overrides the file-wide one")
	}

	// The environment is where a container is configured, and it wins over a
	// config file the container may not even own.
	t.Setenv(EnvAllowWrite, "false")
	if allowed, _ := root.WriteAllowed(loud); allowed {
		t.Error("the environment must override the profile")
	}
	t.Setenv(EnvAllowWrite, "yes")
	if allowed, _ := root.WriteAllowed(silent); !allowed {
		t.Error("the environment must override the file-wide setting")
	}
}

func TestUnreadableWriteSettingIsAUsageError(t *testing.T) {
	// Guessing at "maybe" would decide a security question by coin toss.
	t.Setenv(EnvAllowWrite, "maybe")
	c := &Config{Profiles: map[string]Profile{}}
	if _, err := c.WriteAllowed(Profile{URL: "https://x"}); err == nil {
		t.Fatal("an unreadable value must be refused, not interpreted")
	}
}

func TestUnnamedScopesFollowTheWriteSetting(t *testing.T) {
	silent := Profile{URL: "https://x"}
	if !GrantsScope(silent, true, ScopeMaintenance) {
		t.Error("a profile naming no scopes inherits them all while writing is allowed")
	}
	if GrantsScope(silent, false, ScopeMaintenance) {
		t.Error("a profile naming no scopes grants nothing beyond read when writing is off")
	}
	if !GrantsScope(silent, false, ScopeRead) {
		t.Error("read is always granted")
	}

	named := Profile{URL: "https://x", Scopes: []string{ScopeAcknowledge}}
	if GrantsScope(named, true, ScopeMaintenance) {
		t.Error("naming any scope narrows the profile to exactly those")
	}
	if !GrantsScope(named, true, ScopeAcknowledge) {
		t.Error("a named scope must be granted")
	}
}
