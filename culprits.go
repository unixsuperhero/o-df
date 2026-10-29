package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// A culprit is one row of data: a named group of paths that grows on its
// own (a cache, a store, a download folder), how to size it, whether it is
// rebuildable (only those can be cleared) and how to clear it. The defaults
// are embedded (culprits.json); the user's file adds rows, replaces a row of
// the same name whole, or drops one with "disabled": true. o-df has no code
// per culprit.

//go:embed culprits.json
var defaultCulprits []byte

// Culprit is one row.
type Culprit struct {
	Name     string `json:"name"`
	Title    string `json:"title,omitempty"`
	Category string `json:"category,omitempty"`
	// Paths are ~-relative or absolute; a * matches within one path
	// segment (filepath.Glob). A path holding {project_root} stands for
	// one path per entry of the file's project_roots (none when the list
	// is empty). A missing path is absent, not an error.
	Paths []string `json:"paths"`
	// Rebuildable says the owning tool recreates the data on demand; only
	// a rebuildable culprit can be cleared.
	Rebuildable bool   `json:"rebuildable"`
	Rebuild     string `json:"rebuild,omitempty"`
	// Size, when set, is a command whose stdout's first integer is the
	// culprit's size in bytes; otherwise o-df walks the paths.
	Size []string `json:"size,omitempty"`
	// Clear, when set, is the commands clear runs, each an argv; a command
	// holding {path} runs once per present path. Otherwise clear removes the
	// contents of each path (the directory itself stays).
	Clear [][]string `json:"clear,omitempty"`
	// Recommend is the advice the scan prints: delete, relocate or keep.
	Recommend string `json:"recommend,omitempty"`
	Notes     string `json:"notes,omitempty"`
	// External lets this row size a path that resolves onto another
	// volume than the home directory's (skipped by default).
	External bool `json:"external,omitempty"`
	Disabled bool `json:"disabled,omitempty"`
	// Source is where the row came from: "default" or the user file.
	Source string `json:"source,omitempty"`
}

// Config is a culprits file.
type Config struct {
	// RelocateTo is the directory relocate uses when --to is not given.
	// The defaults set none: relocate without either fails, it never
	// guesses a volume.
	RelocateTo string `json:"relocate_to,omitempty"`
	// ProjectRoots are the directories that hold projects (~/src, say);
	// a culprit path holding {project_root} expands once per root. The
	// defaults set none, so per-project rows find nothing until the
	// user's file names their roots.
	ProjectRoots []string `json:"project_roots,omitempty"`
	// External lets every row (and top) read paths on other volumes.
	External bool      `json:"external,omitempty"`
	Culprits []Culprit `json:"culprits"`
}

// Env is where o-df looks: the home directory and the user's culprits file.
type Env struct {
	Home     string
	UserFile string
	// External allows paths that resolve onto another volume than the
	// home directory's (the culprits file's "external", --external).
	External bool
	// ProjectRoots fill {project_root} in culprit paths (the culprits
	// file's "project_roots").
	ProjectRoots []string
}

// ProjectRootPlaceholder in a culprit path stands for each project root.
const ProjectRootPlaceholder = "{project_root}"

// DefaultEnv reads HOME and the config directory ($O_DF_CULPRITS, else
// $XDG_CONFIG_HOME/o-df/culprits.json, else ~/.config/o-df/culprits.json).
func DefaultEnv() Env {
	home, _ := os.UserHomeDir()
	file := os.Getenv("O_DF_CULPRITS")
	if file == "" {
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".config")
		}
		file = filepath.Join(dir, "o-df", "culprits.json")
	}
	return Env{Home: home, UserFile: file}
}

// Load merges the embedded defaults with the user's file (absent is fine)
// and returns the enabled rows in name order.
func Load(env Env) (Config, error) {
	var base Config
	if err := decode(defaultCulprits, &base); err != nil {
		return Config{}, fmt.Errorf("embedded culprits.json: %w", err)
	}
	for i := range base.Culprits {
		base.Culprits[i].Source = "default"
	}
	if env.UserFile != "" {
		data, err := os.ReadFile(env.UserFile)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return Config{}, err
		default:
			var user Config
			if err := decode(data, &user); err != nil {
				return Config{}, fmt.Errorf("%s: %w", env.UserFile, err)
			}
			base = merge(base, user, env.UserFile)
		}
	}
	out := base.Culprits[:0]
	for _, c := range base.Culprits {
		if !c.Disabled {
			out = append(out, c)
		}
	}
	base.Culprits = out
	sort.Slice(base.Culprits, func(i, j int) bool { return base.Culprits[i].Name < base.Culprits[j].Name })
	return base, validate(base)
}

func decode(data []byte, into *Config) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}

func merge(base, user Config, source string) Config {
	if user.RelocateTo != "" {
		base.RelocateTo = user.RelocateTo
	}
	if user.ProjectRoots != nil {
		base.ProjectRoots = user.ProjectRoots
	}
	base.External = base.External || user.External
	for _, c := range user.Culprits {
		c.Source = source
		if i := slices.IndexFunc(base.Culprits, func(b Culprit) bool { return b.Name == c.Name }); i >= 0 {
			base.Culprits[i] = c
		} else {
			base.Culprits = append(base.Culprits, c)
		}
	}
	return base
}

func validate(cfg Config) error {
	for _, root := range cfg.ProjectRoots {
		if strings.TrimSpace(root) == "" || strings.Contains(root, ProjectRootPlaceholder) {
			return fmt.Errorf("project_roots: %q is not a directory", root)
		}
	}
	seen := map[string]bool{}
	for _, c := range cfg.Culprits {
		switch {
		case c.Name == "" || strings.ContainsAny(c.Name, "/ "):
			return fmt.Errorf("culprit %q: a name is one word without / or spaces", c.Name)
		case seen[c.Name]:
			return fmt.Errorf("culprit %q is listed twice", c.Name)
		case len(c.Paths) == 0 && len(c.Size) == 0:
			return fmt.Errorf("culprit %q has no paths", c.Name)
		}
		for _, cmd := range c.Clear {
			if len(cmd) == 0 {
				return fmt.Errorf("culprit %q: an empty clear command", c.Name)
			}
		}
		seen[c.Name] = true
	}
	return nil
}

// Find is the culprit named name.
func (cfg Config) Find(name string) (Culprit, error) {
	for _, c := range cfg.Culprits {
		if c.Name == name {
			return c, nil
		}
	}
	return Culprit{}, fmt.Errorf("no culprit %q (o-df culprit list names them)", name)
}

// Templates are a culprit path with {project_root} filled in once per
// project root (no paths when there are none), else the path alone.
func (env Env) Templates(p string) []string {
	if !strings.Contains(p, ProjectRootPlaceholder) {
		return []string{p}
	}
	out := make([]string, 0, len(env.ProjectRoots))
	for _, root := range env.ProjectRoots {
		out = append(out, strings.ReplaceAll(p, ProjectRootPlaceholder, strings.TrimRight(root, "/")))
	}
	return out
}

// Expand is a path with ~ and $HOME made absolute, its globs matched; the
// paths that exist, in order, plus the literal path when nothing matched
// (so an absent culprit still shows where it would be).
func (env Env) Expand(p string) (present []string, literal string) {
	p = os.Expand(p, func(k string) string {
		if k == "HOME" {
			return env.Home
		}
		return os.Getenv(k)
	})
	if p == "~" {
		p = env.Home
	} else if rest, ok := strings.CutPrefix(p, "~/"); ok {
		p = filepath.Join(env.Home, rest)
	}
	matches, _ := filepath.Glob(p)
	return matches, p
}

// Tilde shortens a path under home to ~/...
func (env Env) Tilde(p string) string {
	if env.Home == "" {
		return p
	}
	if p == env.Home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, env.Home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return p
}
