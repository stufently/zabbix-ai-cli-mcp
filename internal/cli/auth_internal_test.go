package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/auth"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/config"
)

func TestPersistLoginMigratesBackendsAndRemovesOldToken(t *testing.T) {
	existing := config.Profile{URL: "https://old"}
	next := config.Profile{URL: "https://new", Keyring: true}
	cfg := &config.Config{ActiveProfile: "prod", Profiles: map[string]config.Profile{"prod": existing}}
	var stored, deleted []bool
	p := loginPersistence{
		store: func(_ string, profile config.Profile, _ string) (auth.Source, error) {
			stored = append(stored, profile.Keyring)
			return auth.SourceKeyring, nil
		},
		lookup: func(string, config.Profile) (string, bool, error) { return "", false, nil },
		delete: func(_ string, profile config.Profile) error {
			deleted = append(deleted, profile.Keyring)
			return nil
		},
		save: func(*config.Config) error { return nil },
	}

	if _, err := persistLogin(cfg, "prod", existing, next, "token", p); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || !stored[0] {
		t.Fatalf("stored backends = %v, want keyring", stored)
	}
	if len(deleted) != 1 || deleted[0] {
		t.Fatalf("deleted backends = %v, want old file backend", deleted)
	}
	if got := cfg.Profiles["prod"]; got.URL != next.URL || got.Keyring != next.Keyring {
		t.Fatalf("saved profile = %+v, want %+v", got, next)
	}
}

func TestPersistLoginRemovesNewTokenWhenConfigSaveFails(t *testing.T) {
	existing := config.Profile{URL: "https://old"}
	next := config.Profile{URL: "https://new", Keyring: true}
	cfg := &config.Config{ActiveProfile: "prod", Profiles: map[string]config.Profile{"prod": existing}}
	deletedNew := false
	p := loginPersistence{
		store:  func(string, config.Profile, string) (auth.Source, error) { return auth.SourceKeyring, nil },
		lookup: func(string, config.Profile) (string, bool, error) { return "", false, nil },
		delete: func(_ string, profile config.Profile) error {
			deletedNew = profile.Keyring
			return nil
		},
		save: func(*config.Config) error { return errors.New("disk full") },
	}

	if _, err := persistLogin(cfg, "prod", existing, next, "token", p); err == nil {
		t.Fatal("save failure was ignored")
	}
	if !deletedNew {
		t.Fatal("credential written to the new backend was not removed")
	}
	if got := cfg.Profiles["prod"]; got.URL != existing.URL || got.Keyring != existing.Keyring {
		t.Fatalf("profile after rollback = %+v, want %+v", got, existing)
	}
}

func TestPersistLoginRollsBackWhenOldBackendCannotBeCleaned(t *testing.T) {
	existing := config.Profile{URL: "https://old"}
	next := config.Profile{URL: "https://new", Keyring: true}
	cfg := &config.Config{ActiveProfile: "prod", Profiles: map[string]config.Profile{"prod": existing}}
	saves := 0
	deletedNew := false
	p := loginPersistence{
		store:  func(string, config.Profile, string) (auth.Source, error) { return auth.SourceKeyring, nil },
		lookup: func(string, config.Profile) (string, bool, error) { return "", false, nil },
		delete: func(_ string, profile config.Profile) error {
			if !profile.Keyring {
				return errors.New("old backend unavailable")
			}
			deletedNew = true
			return nil
		},
		save: func(*config.Config) error {
			saves++
			return nil
		},
	}

	_, err := persistLogin(cfg, "prod", existing, next, "token", p)
	if err == nil || !strings.Contains(err.Error(), "old backend unavailable") {
		t.Fatalf("cleanup error = %v", err)
	}
	if saves != 2 {
		t.Fatalf("config was saved %d times, want migration and rollback", saves)
	}
	if !deletedNew {
		t.Fatal("new backend was not cleaned during rollback")
	}
	if got := cfg.Profiles["prod"]; got.URL != existing.URL || got.Keyring != existing.Keyring {
		t.Fatalf("profile after rollback = %+v, want %+v", got, existing)
	}
}

func TestPersistLoginRestoresTokenWhenSameBackendConfigSaveFails(t *testing.T) {
	existing := config.Profile{URL: "https://old"}
	next := config.Profile{URL: "https://new"}
	cfg := &config.Config{ActiveProfile: "prod", Profiles: map[string]config.Profile{"prod": existing}}
	storedTokens := make([]string, 0, 2)
	p := loginPersistence{
		store: func(_ string, _ config.Profile, token string) (auth.Source, error) {
			storedTokens = append(storedTokens, token)
			return auth.SourceFile, nil
		},
		lookup: func(string, config.Profile) (string, bool, error) { return "old-token", true, nil },
		delete: func(string, config.Profile) error { return nil },
		save:   func(*config.Config) error { return errors.New("disk full") },
	}

	if _, err := persistLogin(cfg, "prod", existing, next, "new-token", p); err == nil {
		t.Fatal("save failure was ignored")
	}
	if len(storedTokens) != 2 || storedTokens[0] != "new-token" || storedTokens[1] != "old-token" {
		t.Fatalf("stored tokens = %v, want new token followed by rollback to old token", storedTokens)
	}
	if got := cfg.Profiles["prod"]; got.URL != existing.URL || got.Keyring != existing.Keyring {
		t.Fatalf("profile after rollback = %+v, want %+v", got, existing)
	}
}
