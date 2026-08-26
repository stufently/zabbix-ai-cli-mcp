package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/config"
)

func withConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	t.Setenv(config.EnvToken, "")
	t.Setenv(config.EnvTokenFile, "")
	return dir
}

func TestResolvePrecedence(t *testing.T) {
	dir := withConfigDir(t)
	p := config.Profile{URL: "https://z"}
	if _, err := Store("prod", p, "from-file"); err != nil {
		t.Fatalf("Store: %v", err)
	}

	tok, err := Resolve("prod", p, "")
	if err != nil || tok.Value != "from-file" || tok.Source != SourceFile {
		t.Fatalf("credentials file: %+v %v", tok, err)
	}

	tokenFile := filepath.Join(dir, "tok")
	if err := os.WriteFile(tokenFile, []byte("from-token-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvTokenFile, tokenFile)
	tok, err = Resolve("prod", p, "")
	if err != nil || tok.Value != "from-token-file" {
		t.Fatalf("token file must beat the credentials file: %+v %v", tok, err)
	}

	t.Setenv(config.EnvToken, "from-env")
	tok, err = Resolve("prod", p, "")
	if err != nil || tok.Value != "from-env" {
		t.Fatalf("environment must beat the token file: %+v %v", tok, err)
	}

	tok, err = Resolve("prod", p, "from-stdin")
	if err != nil || tok.Value != "from-stdin" || tok.Source != SourceStdin {
		t.Fatalf("stdin must win: %+v %v", tok, err)
	}
}

func TestCredentialsFileIsPrivate(t *testing.T) {
	dir := withConfigDir(t)
	if _, err := Store("prod", config.Profile{}, "s3cret"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "credentials.toml"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("credentials.toml mode = %04o, want no group or other access", perm)
	}
}

func TestLooseCredentialFilePermissionsAreRefused(t *testing.T) {
	dir := withConfigDir(t)
	path := filepath.Join(dir, "credentials.toml")
	if err := os.WriteFile(path, []byte("[tokens]\nprod = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve("prod", config.Profile{}, "")
	if err == nil || !strings.Contains(err.Error(), "chmod") {
		t.Fatalf("want a permissions complaint, got %v", err)
	}
}

func TestMissingTokenSuggestsLogin(t *testing.T) {
	withConfigDir(t)
	_, err := Resolve("prod", config.Profile{}, "")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "no API token") {
		t.Errorf("message = %q", err.Error())
	}
}

func TestKeyringProfileNeverFallsBackToPlaintext(t *testing.T) {
	dir := withConfigDir(t)
	// A credentials file exists, but the profile selected the keyring. A
	// keyring failure must not quietly read the weaker store instead.
	if err := os.WriteFile(filepath.Join(dir, "credentials.toml"),
		[]byte("[tokens]\nprod = \"plaintext\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := Resolve("prod", config.Profile{Keyring: true}, "")
	if err == nil {
		t.Fatalf("keyring profile silently resolved a token from %v", tok.Source)
	}
	if tok.Value != "" {
		t.Error("a failed resolution must not return a token value")
	}
}

func TestDeleteRemovesTheToken(t *testing.T) {
	withConfigDir(t)
	p := config.Profile{}
	if _, err := Store("prod", p, "x"); err != nil {
		t.Fatal(err)
	}
	if err := Delete("prod", p); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := Resolve("prod", p, ""); err == nil {
		t.Fatal("token survived deletion")
	}
}

func TestStoreRefusesToOverwriteCorruptCredentials(t *testing.T) {
	dir := withConfigDir(t)
	path := filepath.Join(dir, "credentials.toml")
	original := []byte("[tokens\nthis is not toml\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Store("prod", config.Profile{}, "replacement"); err == nil {
		t.Fatal("Store silently replaced a corrupt credentials file")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("corrupt file was modified: %q", got)
	}
}

func TestReadTokenFromStdinTrims(t *testing.T) {
	got, err := ReadTokenFromStdin(strings.NewReader("  abc123  \nignored\n"))
	if err != nil || got != "abc123" {
		t.Fatalf("ReadTokenFromStdin = %q, %v", got, err)
	}
	if _, err := ReadTokenFromStdin(strings.NewReader("\n")); err == nil {
		t.Error("empty stdin must be an error")
	}
}
