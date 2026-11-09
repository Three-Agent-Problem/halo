// Package config reads halo.yaml (the roster) and the saved state (server, API key,
// address tokens).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Roles of an agent.
const (
	RoleLead     = "lead"
	RoleWorker   = "worker"
	RoleReviewer = "reviewer"
)

// Config is halo.yaml.
type Config struct {
	// Server is the Lagrange server's URL. LAGRANGE_SERVER overrides it.
	Server string `yaml:"server"`
	// Prefix starts every address handle: "halo" gives @halo-lead, @halo-sonnet-1, ...
	Prefix string `yaml:"prefix"`
	// Repo is the git repository the agents work on. Relative paths are resolved
	// against the directory of halo.yaml.
	Repo string `yaml:"repo"`
	// ACP is the coding agent command, "claude-agent-acp" by default.
	ACP string `yaml:"acp"`
	// Preview is the command that serves the project for the live preview row,
	// e.g. "npm run dev". Setting it marks the project as a web project; without
	// it Halo detects one from package.json (see [Config.PreviewCommand]).
	Preview string `yaml:"preview"`
	// Agents is the roster: one lead, one reviewer, any number of workers.
	Agents []Agent `yaml:"agents"`
}

// Agent is one roster entry.
type Agent struct {
	Name     string `yaml:"name"`
	Model    string `yaml:"model"`
	Role     string `yaml:"role"`
	Capacity int    `yaml:"capacity"`
}

// Default is the roster used when there is no halo.yaml.
func Default() *Config {
	c := &Config{Agents: []Agent{
		{Name: "lead", Model: "opus", Role: RoleLead},
		{Name: "haiku-1", Model: "haiku"},
		{Name: "sonnet-1", Model: "sonnet"},
		{Name: "sonnet-2", Model: "sonnet"},
		{Name: "opus-1", Model: "opus"},
		{Name: "reviewer", Model: "sonnet", Role: RoleReviewer},
	}}
	c.fill(".")
	return c
}

// Load reads path, or returns Default if it doesn't exist.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := yaml.Unmarshal(raw, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.fill(filepath.Dir(path))
	return c, c.Validate()
}

func (c *Config) fill(dir string) {
	if c.Prefix == "" {
		c.Prefix = "halo"
	}
	if c.Repo == "" {
		c.Repo = "."
	}
	if !filepath.IsAbs(c.Repo) {
		c.Repo = filepath.Join(dir, c.Repo)
	}
	if abs, err := filepath.Abs(c.Repo); err == nil {
		c.Repo = abs
	}
	if c.ACP == "" {
		c.ACP = "claude-agent-acp"
	}
	for i := range c.Agents {
		a := &c.Agents[i]
		if a.Role == "" {
			a.Role = RoleWorker
		}
		if a.Capacity == 0 {
			a.Capacity = 1
			if a.Role != RoleWorker {
				a.Capacity = 4
			}
		}
	}
}

func (c *Config) validate() error {
	var leads, reviewers, workers int
	for _, a := range c.Agents {
		switch a.Role {
		case RoleLead:
			leads++
		case RoleReviewer:
			reviewers++
		case RoleWorker:
			workers++
		default:
			return fmt.Errorf("agent %s: unknown role %q", a.Name, a.Role)
		}
	}
	if leads != 1 || reviewers != 1 || workers == 0 {
		return fmt.Errorf("the roster needs exactly one lead, one reviewer and at least one worker")
	}
	return nil
}

// Handle is the Lagrange handle of a roster name.
func (c *Config) Handle(name string) string { return c.Prefix + "-" + name }

// Name is the roster name of a handle, or the handle itself.
func (c *Config) Name(handle string) string {
	return strings.TrimPrefix(handle, c.Prefix+"-")
}

// ByRole returns the agents with role.
func (c *Config) ByRole(role string) []Agent {
	var out []Agent
	for _, a := range c.Agents {
		if a.Role == role {
			out = append(out, a)
		}
	}
	return out
}

// Lead returns the lead.
func (c *Config) Lead() Agent { return c.ByRole(RoleLead)[0] }

// Reviewer returns the reviewer.
func (c *Config) Reviewer() Agent { return c.ByRole(RoleReviewer)[0] }

// Agent returns the roster entry of a handle.
func (c *Config) Agent(handle string) (Agent, bool) {
	for _, a := range c.Agents {
		if c.Handle(a.Name) == handle {
			return a, true
		}
	}
	return Agent{}, false
}

// WorkerModels lists the models workers run, in ladder order of appearance.
func (c *Config) WorkerModels() []string {
	var out []string
	for _, a := range c.ByRole(RoleWorker) {
		found := false
		for _, m := range out {
			found = found || m == a.Model
		}
		if !found {
			out = append(out, a.Model)
		}
	}
	return out
}

// State is what halo remembers between runs: the server, the API key, who it
// acts for, and the address tokens it minted.
type State struct {
	Server string            `json:"server"`
	Key    string            `json:"key"`
	Human  string            `json:"human"`
	Tokens map[string]string `json:"tokens,omitempty"` // "server handle" -> token
}

// StatePath is $HALO_STATE or halo/state.json in the user's config directory.
func StatePath() (string, error) {
	if p := os.Getenv("HALO_STATE"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "halo", "state.json"), nil
}

// LoadState reads the state, applying LAGRANGE_SERVER and LAGRANGE_KEY.
func LoadState() (*State, error) {
	s := &State{}
	p, err := StatePath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, s); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	if v := os.Getenv("LAGRANGE_SERVER"); v != "" {
		s.Server = v
	}
	if v := os.Getenv("LAGRANGE_KEY"); v != "" {
		s.Key = v
	}
	s.Server = strings.TrimRight(s.Server, "/")
	return s, nil
}

// Save writes the state, readable by the user only.
func (s *State) Save() error {
	p, err := StatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(s, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Token returns the saved token of a handle on the current server.
func (s *State) Token(handle string) string { return s.Tokens[s.Server+" "+handle] }

// SetToken saves a token.
func (s *State) SetToken(handle, token string) {
	if s.Tokens == nil {
		s.Tokens = map[string]string{}
	}
	s.Tokens[s.Server+" "+handle] = token
}

// RosterPath is the global roster, next to the state: one team for every repo.
// HALO_ROSTER overrides it.
func RosterPath() (string, error) {
	if p := os.Getenv("HALO_ROSTER"); p != "" {
		return p, nil
	}
	p, err := StatePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "roster.yaml"), nil
}

// LoadRoster returns the roster: local if that file exists (a repo that wants its
// own team), else the global one, which it creates from Default on first use.
// The second result says where it came from.
func LoadRoster(local string) (*Config, string, error) {
	if local != "" {
		if _, err := os.Stat(local); err == nil {
			c, err := Load(local)
			return c, local, err
		}
	}
	p, err := RosterPath()
	if err != nil {
		return nil, "", err
	}
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		c := Default()
		return c, p, c.SaveRoster(p)
	}
	c, err := Load(p)
	return c, p, err
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// Validate checks the roster: one lead, one reviewer, at least one worker, and
// unique names that work as handles.
func (c *Config) Validate() error {
	seen := map[string]bool{}
	for _, a := range c.Agents {
		if !nameRe.MatchString(a.Name) {
			return fmt.Errorf("agent name %q: use lowercase letters, digits and dashes", a.Name)
		}
		if seen[a.Name] {
			return fmt.Errorf("two agents are named %s", a.Name)
		}
		seen[a.Name] = true
	}
	return c.validate()
}

// SaveRoster writes the roster (prefix, coding agent and agents) to path.
func (c *Config) SaveRoster(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	out := struct {
		Server string  `yaml:"server,omitempty"`
		Prefix string  `yaml:"prefix"`
		ACP    string  `yaml:"acp,omitempty"`
		Agents []Agent `yaml:"agents"`
	}{c.Server, c.Prefix, c.ACP, c.Agents}
	if out.ACP == "claude-agent-acp" {
		out.ACP = ""
	}
	raw, err := yaml.Marshal(out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	head := "# Halo's team, shared by every repo. Edit it in Halo: panel 7 (roster).\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(head), raw...), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Clone returns a deep copy.
func (c *Config) Clone() *Config {
	n := *c
	n.Agents = append([]Agent(nil), c.Agents...)
	return &n
}
