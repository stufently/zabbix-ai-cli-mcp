// Package auth resolves the Zabbix API token for a profile.
//
// The resolution order is fixed and documented. There is deliberately no
// silent fallback from the OS keyring to a plaintext file: a keyring that
// fails must say so, because quietly writing the token to disk would downgrade
// the user's chosen protection without telling them.
package auth

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/config"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/zalando/go-keyring"
)

// keyringService namespaces this program's secrets in the OS keyring.
const keyringService = "zabbix-ai-cli-mcp"

// Source records where a token came from, for `auth status`.
type Source string

const (
	SourceStdin       Source = "stdin"
	SourceEnv         Source = "environment"
	SourceEnvFile     Source = "environment token file"
	SourceProfileFile Source = "profile token file"
	SourceKeyring     Source = "OS keyring"
	SourceFile        Source = "credentials file"
	SourceNone        Source = "none"
)

// Token is a resolved credential.
type Token struct {
	Value  string
	Source Source
}

// Resolve finds the token for the named profile.
//
// stdinToken, when non-empty, comes from --token-stdin and wins. A --token
// flag is deliberately not offered: flag values are visible in shell history
// and in the process list.
func Resolve(name string, p config.Profile, stdinToken string) (Token, error) {
	if stdinToken != "" {
		return Token{Value: stdinToken, Source: SourceStdin}, nil
	}
	if v := os.Getenv(config.EnvToken); v != "" {
		return Token{Value: v, Source: SourceEnv}, nil
	}
	if f := os.Getenv(config.EnvTokenFile); f != "" {
		v, err := readTokenFile(f)
		if err != nil {
			return Token{}, err
		}
		return Token{Value: v, Source: SourceEnvFile}, nil
	}
	if p.TokenFile != "" {
		v, err := readTokenFile(p.TokenFile)
		if err != nil {
			return Token{}, err
		}
		return Token{Value: v, Source: SourceProfileFile}, nil
	}
	if p.Keyring {
		v, err := keyring.Get(keyringService, name)
		if err != nil {
			return Token{}, keyringFailure(name, err)
		}
		return Token{Value: v, Source: SourceKeyring}, nil
	}
	v, err := readCredentialsFile(name)
	if err != nil {
		return Token{}, err
	}
	if v == "" {
		return Token{}, errs.New(errs.CodeAuth, errs.ExitAuth,
			"no API token is stored for profile %q", name).
			WithSuggestion("run 'zabbix-ai-cli-mcp login --profile %s', or set %s", name, config.EnvToken)
	}
	return Token{Value: v, Source: SourceFile}, nil
}

func keyringFailure(name string, err error) error {
	if errors.Is(err, keyring.ErrNotFound) {
		return errs.New(errs.CodeAuth, errs.ExitAuth,
			"profile %q uses the OS keyring but holds no token", name).
			WithSuggestion("run 'zabbix-ai-cli-mcp login --profile %s'", name)
	}
	suggestion := fmt.Sprintf(
		"the keyring is unavailable and this program will not silently fall back to a plaintext file; "+
			"set %s, set %s to a file path, or re-run login with --store file",
		config.EnvToken, config.EnvTokenFile)
	if runtime.GOOS == "linux" && os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		suggestion = "no D-Bus session is available, which is normal in containers and over SSH; " + suggestion
	}
	return errs.New(errs.CodeAuth, errs.ExitAuth,
		"could not read the OS keyring for profile %q", name).
		WithSuggestion("%s", suggestion).Wrap(err)
}

// Store writes the token using the backend the profile selects.
func Store(name string, p config.Profile, token string) (Source, error) {
	if p.Keyring {
		if err := keyring.Set(keyringService, name, token); err != nil {
			return SourceNone, keyringFailure(name, err)
		}
		return SourceKeyring, nil
	}
	if err := writeCredentialsFile(name, token); err != nil {
		return SourceNone, err
	}
	return SourceFile, nil
}

// LookupStored reads only the persistent backend selected by the profile. It
// deliberately ignores environment and token-file overrides, which makes it
// suitable for transactional credential replacement and rollback.
func LookupStored(name string, p config.Profile) (string, bool, error) {
	if p.Keyring {
		v, err := keyring.Get(keyringService, name)
		if errors.Is(err, keyring.ErrNotFound) {
			return "", false, nil
		}
		if err != nil {
			return "", false, keyringFailure(name, err)
		}
		return v, true, nil
	}
	v, err := readCredentialsFile(name)
	if err != nil {
		return "", false, err
	}
	return v, v != "", nil
}

// Delete removes a stored token. A missing token is not an error.
func Delete(name string, p config.Profile) error {
	if p.Keyring {
		if err := keyring.Delete(keyringService, name); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return keyringFailure(name, err)
		}
		return nil
	}
	return writeCredentialsFile(name, "")
}

func credentialsPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.toml"), nil
}

type credentials struct {
	Tokens map[string]string `toml:"tokens"`
}

func readCredentialsFile(name string) (string, error) {
	path, err := credentialsPath()
	if err != nil {
		return "", err
	}
	data, err := readSecretFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var c credentials
	if err := toml.Unmarshal(data, &c); err != nil {
		return "", errs.Usage("credentials file %s is not valid TOML: %v", path, err)
	}
	return c.Tokens[name], nil
}

func writeCredentialsFile(name, token string) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	c := credentials{Tokens: map[string]string{}}
	if data, err := readSecretFile(path); err == nil {
		if err := toml.Unmarshal(data, &c); err != nil {
			return errs.Usage("credentials file %s is not valid TOML: %v", path, err)
		}
		if c.Tokens == nil {
			c.Tokens = map[string]string{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if token == "" {
		delete(c.Tokens, name)
	} else {
		c.Tokens[name] = token
	}

	tmp, err := os.CreateTemp(dir, ".credentials-*.toml")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := toml.NewEncoder(tmp).Encode(c); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// readSecretFile reads a credential file, refusing anything that is not a
// regular file owned by the current user with no access for group or other.
func readSecretFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, errs.Usage("%s is a symlink; refusing to read a credential through one", path)
	}
	if !fi.Mode().IsRegular() {
		return nil, errs.Usage("%s is not a regular file", path)
	}
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			return nil, errs.Usage(
				"%s is readable by other users (mode %04o); run 'chmod 600 %s'", path, perm, path)
		}
		if err := checkOwner(fi); err != nil {
			return nil, err
		}
	}
	return os.ReadFile(path)
}

func readTokenFile(path string) (string, error) {
	data, err := readSecretFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", errs.New(errs.CodeAuth, errs.ExitAuth, "token file %s does not exist", path)
	}
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errs.New(errs.CodeAuth, errs.ExitAuth, "token file %s is empty", path)
	}
	return token, nil
}

// ReadTokenFromStdin reads a token from r, for --token-stdin.
func ReadTokenFromStdin(r io.Reader) (string, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return "", errs.Usage("no token was read from stdin")
	}
	return token, nil
}
