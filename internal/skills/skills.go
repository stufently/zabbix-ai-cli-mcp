// Package skills embeds the skill files and installs them where Claude Code
// and Codex look for them.
//
// The skills are embedded rather than fetched, so an installed binary carries
// everything it needs and installation never depends on the network.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
)

//go:embed all:files
var files embed.FS

const root = "files"

// Skill is one embedded skill.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// List returns the embedded skills.
func List() ([]Skill, error) {
	entries, err := fs.ReadDir(files, root)
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s := Skill{Name: e.Name()}
		if data, err := fs.ReadFile(files, joinPath(e.Name(), "SKILL.md")); err == nil {
			s.Description = frontmatterField(string(data), "description")
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func joinPath(parts ...string) string {
	return root + "/" + strings.Join(parts, "/")
}

// frontmatterField reads one field out of the YAML front matter without
// pulling in a YAML parser for the sake of two keys.
func frontmatterField(doc, field string) string {
	lines := strings.Split(doc, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	prefix := field + ":"
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			return ""
		}
		if strings.HasPrefix(line, prefix) {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, prefix)), `"`)
		}
	}
	return ""
}

// Target is a supported agent runtime.
type Target string

// The runtimes this program knows how to install into.
const (
	TargetClaude Target = "claude"
	TargetCodex  Target = "codex"
)

// Targets lists the supported runtimes.
var Targets = []string{string(TargetClaude), string(TargetCodex)}

// Destination reports where skills go for a runtime.
//
// Both Claude Code and Codex read skills/<name>/SKILL.md with YAML front
// matter, which is why one set of files serves both.
func Destination(target Target, global bool) (string, error) {
	var dir string
	switch target {
	case TargetClaude:
		dir = ".claude"
	case TargetCodex:
		dir = ".codex"
	default:
		return "", errs.Usage("unknown target %q; supported targets are %s",
			target, strings.Join(Targets, ", "))
	}
	if !global {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, dir, "skills"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dir, "skills"), nil
}

// InstallResult records what happened to one skill.
type InstallResult struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

// Install copies the embedded skills into dest.
//
// An existing skill is left alone unless force is set: overwriting a file the
// user may have edited is not this program's decision to make.
func Install(dest string, force bool) ([]InstallResult, error) {
	skills, err := List()
	if err != nil {
		return nil, err
	}
	var results []InstallResult
	for _, s := range skills {
		target := filepath.Join(dest, s.Name)
		status, err := installOne(s.Name, target, force)
		if err != nil {
			return nil, err
		}
		results = append(results, InstallResult{Name: s.Name, Path: target, Status: status})
	}
	return results, nil
}

func installOne(name, target string, force bool) (string, error) {
	if _, err := os.Stat(target); err == nil && !force {
		return "skipped, already present", nil
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", target, err)
	}
	source := joinPath(name)
	entries, err := fs.ReadDir(files, source)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := fs.ReadFile(files, source+"/"+e.Name())
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(target, e.Name()), data, 0o644); err != nil {
			return "", err
		}
	}
	return "installed", nil
}
