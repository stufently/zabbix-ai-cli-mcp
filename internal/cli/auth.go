package cli

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/api"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/auth"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/config"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/service"
	"golang.org/x/term"
)

func loginCommand(g *globals) *cobra.Command {
	var url, store string
	var scopes []string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store the Zabbix URL and API token for a profile",
		Long: "Prompts for anything not supplied. The token is never accepted as a flag, because flag " +
			"values are visible in shell history and in the process list; pipe it in with --token-stdin instead.\n\n" +
			"The token is verified against the server before it is stored.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := g.profile
			if name == "" {
				name = "default"
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			existing := cfg.Profiles[name]

			// Checked before anything reads the token: with --token-stdin the
			// secret is consumed and gone, and rejecting the flag afterwards
			// makes the caller pipe it a second time.
			if store != "" && store != "file" && store != "keyring" {
				return errs.Usage("--store must be file or keyring")
			}

			if url == "" {
				url = existing.URL
			}
			if url == "" {
				url, err = prompt(g, "Zabbix URL: ")
				if err != nil {
					return err
				}
			}
			if strings.TrimSpace(url) == "" {
				return errs.Usage("a Zabbix URL is required")
			}

			var token string
			if g.tokenStdin {
				token, err = auth.ReadTokenFromStdin(g.stdin)
			} else {
				token, err = promptSecret(g, "API token: ")
			}
			if err != nil {
				return err
			}
			if strings.TrimSpace(token) == "" {
				return errs.Usage("an API token is required")
			}

			if err := config.ValidateScopes(scopes); err != nil {
				return err
			}
			backend := store
			if backend == "" {
				if existing.Keyring {
					backend = "keyring"
				} else {
					backend = "file"
				}
			}
			profile := config.Profile{
				URL:            strings.TrimSpace(url),
				Scopes:         scopes,
				Keyring:        backend == "keyring",
				TimeoutSeconds: existing.TimeoutSeconds,
				CAFile:         existing.CAFile,
				Insecure:       existing.Insecure,
			}
			if len(profile.Scopes) == 0 {
				profile.Scopes = existing.Scopes
			}

			version, err := verifyToken(cmd.Context(), profile, token)
			if err != nil {
				return err
			}

			source, err := persistLogin(cfg, name, existing, profile, token, loginPersistence{
				store:  auth.Store,
				lookup: auth.LookupStored,
				delete: auth.Delete,
				save:   config.Save,
			})
			if err != nil {
				return err
			}

			res := &output.Result{Data: map[string]any{
				"profile":         name,
				"url":             profile.URL,
				"zabbix_version":  version,
				"token_stored_in": string(source),
				"scopes":          grantedScopes(profile),
			}}
			res.Meta.Returned = 1
			res.Table = &output.Table{
				Headers: []string{"FIELD", "VALUE"},
				Rows: [][]string{
					{"profile", name},
					{"url", profile.URL},
					{"zabbix", version},
					{"token stored in", string(source)},
					{"scopes", strings.Join(grantedScopes(profile), ", ")},
				},
			}
			return g.render(res)
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "Zabbix URL, for example https://zabbix.example.com")
	cmd.Flags().StringVar(&store, "store", "", "where to keep the token: file or keyring (default: preserve the current backend, or file for a new profile)")
	cmd.Flags().StringSliceVar(&scopes, "scopes", nil,
		"scopes this profile may plan writes for: "+strings.Join(config.KnownScopes, ", "))
	return cmd
}

// loginPersistence keeps the credential and config mutations testable as one
// transaction. In production these functions are auth.Store, auth.Delete and
// config.Save; tests can make each step fail without touching a real keyring.
type loginPersistence struct {
	store  func(string, config.Profile, string) (auth.Source, error)
	lookup func(string, config.Profile) (string, bool, error)
	delete func(string, config.Profile) error
	save   func(*config.Config) error
}

func persistLogin(cfg *config.Config, name string, existing, profile config.Profile, token string, p loginPersistence) (auth.Source, error) {
	previous, existed := cfg.Profiles[name]
	previousActive := cfg.ActiveProfile
	newLocation := !existed || existing.Keyring != profile.Keyring
	var previousToken string
	var hadPreviousToken bool
	if existed && !newLocation {
		var err error
		previousToken, hadPreviousToken, err = p.lookup(name, existing)
		if err != nil {
			return auth.SourceNone, fmt.Errorf("read existing credential before replacement: %w", err)
		}
	}

	source, err := p.store(name, profile, token)
	if err != nil {
		return auth.SourceNone, err
	}
	cfg.Profiles[name] = profile
	if cfg.ActiveProfile == "" {
		cfg.ActiveProfile = name
	}
	newActive := cfg.ActiveProfile
	if err := p.save(cfg); err != nil {
		restoreProfile(cfg, name, previous, existed, previousActive)
		var rollbackErr error
		if newLocation {
			rollbackErr = p.delete(name, profile)
		} else if hadPreviousToken {
			_, rollbackErr = p.store(name, existing, previousToken)
		} else {
			rollbackErr = p.delete(name, profile)
		}
		if rollbackErr != nil {
			return auth.SourceNone, fmt.Errorf("save profile: %w; restore previous credential: %v", err, rollbackErr)
		}
		return auth.SourceNone, err
	}

	if existed && existing.Keyring != profile.Keyring {
		if cleanupErr := p.delete(name, existing); cleanupErr != nil {
			// Keep config and credential selection consistent: if the old secret
			// cannot be removed, put the config back and remove the new copy.
			restoreProfile(cfg, name, previous, true, previousActive)
			if rollbackErr := p.save(cfg); rollbackErr != nil {
				// The persisted config still selects the new backend, so its token
				// must remain available even though the cleanup could not finish.
				cfg.Profiles[name] = profile
				cfg.ActiveProfile = newActive
				return auth.SourceNone, fmt.Errorf("remove token from previous backend: %w; restore profile configuration: %v", cleanupErr, rollbackErr)
			}
			if rollbackErr := p.delete(name, profile); rollbackErr != nil {
				return auth.SourceNone, fmt.Errorf("remove token from previous backend: %w; remove token from new backend during rollback: %v", cleanupErr, rollbackErr)
			}
			return auth.SourceNone, fmt.Errorf("remove token from previous backend: %w", cleanupErr)
		}
	}
	return source, nil
}

func restoreProfile(cfg *config.Config, name string, previous config.Profile, existed bool, active string) {
	if existed {
		cfg.Profiles[name] = previous
	} else {
		delete(cfg.Profiles, name)
	}
	cfg.ActiveProfile = active
}

func grantedScopes(p config.Profile) []string {
	if len(p.Scopes) == 0 {
		return []string{config.ScopeRead}
	}
	return append([]string{config.ScopeRead}, p.Scopes...)
}

// verifyToken proves the credential works before it is written anywhere, so a
// typo is reported now rather than at the next incident.
func verifyToken(ctx context.Context, p config.Profile, token string) (string, error) {
	transport, err := transportFor(p)
	if err != nil {
		return "", err
	}
	client := api.New(p.URL, token,
		api.WithHTTPClient(&http.Client{Timeout: 20 * time.Second, Transport: transport}),
		api.WithUserAgent("zabbix-ai-cli-mcp/"+Version))
	svc := service.New(client)
	version, err := svc.Version(ctx)
	if err != nil {
		return "", err
	}
	// apiinfo.version needs no authentication, so the token is only proven by
	// a call that does.
	var probe []map[string]any
	if err := client.CallIdempotent(ctx, "host.get",
		map[string]any{"output": []string{"hostid"}, "limit": 1}, &probe); err != nil {
		return "", errs.FromAPI(err)
	}
	return version.String(), nil
}

func logoutCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored token for a profile",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			name, profile, err := cfg.Resolve(g.profile)
			if err != nil {
				return err
			}
			if err := auth.Delete(name, profile); err != nil {
				return err
			}
			res := &output.Result{Data: map[string]any{"profile": name, "token_removed": true}}
			res.Meta.Returned = 1
			res.Table = &output.Table{
				Headers: []string{"RESULT"},
				Rows:    [][]string{{"removed the stored token for profile " + name}},
			}
			return g.render(res)
		},
	}
}

func authCommand(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Inspect authentication"}
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Check whether the active profile can reach Zabbix",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			name, profile, err := cfg.Resolve(g.profile)
			if err != nil {
				return err
			}
			token, err := auth.Resolve(name, profile, "")
			if err != nil {
				return err
			}
			version, err := verifyToken(cmd.Context(), profile, token.Value)
			status := "ok"
			if err != nil {
				status = "failed"
			}
			data := map[string]any{
				"profile":        name,
				"url":            profile.URL,
				"token_source":   string(token.Source),
				"status":         status,
				"scopes":         grantedScopes(profile),
				"zabbix_version": version,
			}
			res := &output.Result{Data: data}
			res.Meta.Returned = 1
			res.Meta.Profile = name
			res.Table = &output.Table{
				Headers: []string{"FIELD", "VALUE"},
				Rows: [][]string{
					{"profile", name},
					{"url", profile.URL},
					{"authentication", "API token from " + string(token.Source)},
					{"scopes", strings.Join(grantedScopes(profile), ", ")},
					{"zabbix", version},
					{"status", status},
				},
			}
			if err != nil {
				// The check itself is the answer; report it rather than
				// failing, so `auth status` always describes the situation.
				res.Warn("%s", errs.FromAPI(err).Message)
			}
			return g.render(res)
		},
	})
	return cmd
}

func profileCommand(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "profile", Short: "Manage profiles"}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			type entry struct {
				Name   string   `json:"name"`
				URL    string   `json:"url"`
				Active bool     `json:"active"`
				Scopes []string `json:"scopes"`
			}
			list := make([]entry, 0, len(cfg.Profiles))
			rows := [][]string{}
			for _, name := range cfg.Names() {
				p := cfg.Profiles[name]
				e := entry{Name: name, URL: p.URL, Active: name == cfg.ActiveProfile, Scopes: grantedScopes(p)}
				list = append(list, e)
				marker := ""
				if e.Active {
					marker = "*"
				}
				rows = append(rows, []string{marker, name, p.URL, strings.Join(e.Scopes, ", ")})
			}
			res := &output.Result{Data: list}
			res.Meta.Returned = len(list)
			res.Table = &output.Table{Headers: []string{"", "NAME", "URL", "SCOPES"}, Rows: rows}
			return g.render(res)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show [name]",
		Short: "Show one profile",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			want := g.profile
			if len(args) == 1 {
				want = args[0]
			}
			name, p, err := cfg.Resolve(want)
			if err != nil {
				return err
			}
			res := &output.Result{Data: map[string]any{
				"name": name, "url": p.URL, "scopes": grantedScopes(p),
				"token_backend": tokenBackend(p), "insecure": p.Insecure,
			}}
			res.Meta.Returned = 1
			res.Table = &output.Table{
				Headers: []string{"FIELD", "VALUE"},
				Rows: [][]string{
					{"name", name},
					{"url", p.URL},
					{"scopes", strings.Join(grantedScopes(p), ", ")},
					{"token backend", tokenBackend(p)},
					{"tls verification", boolText(!p.Insecure, "enabled", "disabled")},
				},
			}
			return g.render(res)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "use <name>",
		Short: "Make a profile the default",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if _, ok := cfg.Profiles[args[0]]; !ok {
				return errs.NotFound("profile %q does not exist", args[0]).
					WithSuggestion("known profiles: %s", strings.Join(cfg.Names(), ", "))
			}
			cfg.ActiveProfile = args[0]
			if err := config.Save(cfg); err != nil {
				return err
			}
			res := &output.Result{Data: map[string]any{"active_profile": args[0]}}
			res.Meta.Returned = 1
			res.Table = &output.Table{Headers: []string{"RESULT"}, Rows: [][]string{{"active profile is now " + args[0]}}}
			return g.render(res)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "delete <name>",
		Short: "Remove a profile and its stored token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			p, ok := cfg.Profiles[args[0]]
			if !ok {
				return errs.NotFound("profile %q does not exist", args[0])
			}
			if err := auth.Delete(args[0], p); err != nil {
				return err
			}
			delete(cfg.Profiles, args[0])
			if cfg.ActiveProfile == args[0] {
				cfg.ActiveProfile = ""
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			res := &output.Result{Data: map[string]any{"deleted": args[0]}}
			res.Meta.Returned = 1
			res.Table = &output.Table{Headers: []string{"RESULT"}, Rows: [][]string{{"deleted profile " + args[0]}}}
			return g.render(res)
		},
	})

	var add, remove []string
	scopesCmd := &cobra.Command{
		Use:   "scopes <name>",
		Short: "Show or change which writes a profile may plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			p, ok := cfg.Profiles[args[0]]
			if !ok {
				return errs.NotFound("profile %q does not exist", args[0])
			}
			if err := config.ValidateScopes(append(append([]string{}, add...), remove...)); err != nil {
				return err
			}
			changed := false
			for _, s := range add {
				if s != config.ScopeRead && !p.HasScope(s) {
					p.Scopes = append(p.Scopes, s)
					changed = true
				}
			}
			for _, s := range remove {
				for i, existing := range p.Scopes {
					if existing == s {
						p.Scopes = append(p.Scopes[:i], p.Scopes[i+1:]...)
						changed = true
						break
					}
				}
			}
			if changed {
				cfg.Profiles[args[0]] = p
				if err := config.Save(cfg); err != nil {
					return err
				}
			}
			res := &output.Result{Data: map[string]any{"profile": args[0], "scopes": grantedScopes(p)}}
			res.Meta.Returned = 1
			res.Table = &output.Table{
				Headers: []string{"PROFILE", "SCOPES"},
				Rows:    [][]string{{args[0], strings.Join(grantedScopes(p), ", ")}},
			}
			return g.render(res)
		},
	}
	scopesCmd.Flags().StringSliceVar(&add, "add", nil, "grant a scope")
	scopesCmd.Flags().StringSliceVar(&remove, "remove", nil, "revoke a scope")
	cmd.AddCommand(scopesCmd)

	return cmd
}

func tokenBackend(p config.Profile) string {
	switch {
	case p.TokenFile != "":
		return "token file " + p.TokenFile
	case p.Keyring:
		return "OS keyring"
	default:
		return "credentials file"
	}
}

func boolText(v bool, yes, no string) string {
	if v {
		return yes
	}
	return no
}

func prompt(g *globals, label string) (string, error) {
	fmt.Fprint(g.stderr, label)
	reader := bufio.NewReader(g.stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", errs.Usage("no value was entered")
	}
	return strings.TrimSpace(line), nil
}

// promptSecret reads a credential without echoing it. When stdin is not a
// terminal there is nothing to disable, and the caller is told to pipe the
// token in explicitly rather than have it echoed into a log.
func promptSecret(g *globals, label string) (string, error) {
	f, ok := g.stdin.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", errs.Usage("stdin is not a terminal, so a token cannot be prompted for").
			WithSuggestion("pipe it in: printf %%s \"$TOKEN\" | zabbix-ai-cli-mcp login --token-stdin --url ...")
	}
	fmt.Fprint(g.stderr, label)
	data, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(g.stderr)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
