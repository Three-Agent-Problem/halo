package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateEnv clears the Lagrange variables and points the state file at a
// temp directory, so the host environment can't leak into a test.
func isolateEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("LAGRANGE_SERVER", "")
	t.Setenv("LAGRANGE_KEY", "")
	p := filepath.Join(t.TempDir(), "nested", "state.json")
	t.Setenv("HALO_STATE", p)
	return p
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "halo.yaml")
	c, err := Load(missing)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Prefix != "halo" {
		t.Errorf("Prefix = %q, want halo", c.Prefix)
	}
	if c.ACP != "claude-agent-acp" {
		t.Errorf("ACP = %q, want claude-agent-acp", c.ACP)
	}
	if len(c.Agents) != 6 {
		t.Errorf("len(Agents) = %d, want 6", len(c.Agents))
	}
	if !filepath.IsAbs(c.Repo) {
		t.Errorf("Repo = %q, want an absolute path", c.Repo)
	}
	if n := len(c.ByRole(RoleLead)); n != 1 {
		t.Errorf("leads = %d, want 1", n)
	}
	if n := len(c.ByRole(RoleReviewer)); n != 1 {
		t.Errorf("reviewers = %d, want 1", n)
	}
}

func TestLoadResolvesFieldsAndDefaults(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "halo.yaml", `
server: https://lagrange.example
prefix: crew
repo: sub
agents:
  - name: boss
    role: lead
  - name: rev
    role: reviewer
  - name: w1
  - name: w2
    model: haiku
    capacity: 3
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Server != "https://lagrange.example" {
		t.Errorf("Server = %q", c.Server)
	}
	if c.Prefix != "crew" {
		t.Errorf("Prefix = %q, want crew", c.Prefix)
	}
	wantRepo := filepath.Join(dir, "sub")
	if c.Repo != wantRepo {
		t.Errorf("Repo = %q, want %q", c.Repo, wantRepo)
	}
	if !filepath.IsAbs(c.Repo) {
		t.Errorf("Repo %q is not absolute", c.Repo)
	}

	byName := map[string]Agent{}
	for _, a := range c.Agents {
		byName[a.Name] = a
	}
	if got := byName["w1"].Role; got != RoleWorker {
		t.Errorf("w1 role = %q, want worker", got)
	}
	if got := byName["w1"].Capacity; got != 1 {
		t.Errorf("w1 capacity = %d, want 1", got)
	}
	if got := byName["boss"].Capacity; got != 4 {
		t.Errorf("lead capacity = %d, want 4", got)
	}
	if got := byName["rev"].Capacity; got != 4 {
		t.Errorf("reviewer capacity = %d, want 4", got)
	}
	if got := byName["w2"].Capacity; got != 3 {
		t.Errorf("explicit capacity = %d, want 3", got)
	}
}

func TestLoadDefaultsPrefixWhenOmitted(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "halo.yaml", `
agents:
  - name: lead
    role: lead
  - name: rev
    role: reviewer
  - name: w
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Prefix != "halo" {
		t.Errorf("Prefix = %q, want halo", c.Prefix)
	}
	if c.Repo != dir {
		t.Errorf("Repo = %q, want the config directory %q", c.Repo, dir)
	}
}

func TestLoadValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantMsg []string
	}{
		{
			name: "unknown role",
			yaml: `
agents:
  - name: lead
    role: lead
  - name: rev
    role: reviewer
  - name: odd
    role: boss
`,
			wantMsg: []string{"odd", "boss"},
		},
		{
			name: "no lead",
			yaml: `
agents:
  - name: rev
    role: reviewer
  - name: w
`,
		},
		{
			name: "two leads",
			yaml: `
agents:
  - name: lead1
    role: lead
  - name: lead2
    role: lead
  - name: rev
    role: reviewer
  - name: w
`,
		},
		{
			name: "no reviewer",
			yaml: `
agents:
  - name: lead
    role: lead
  - name: w
`,
		},
		{
			name: "no workers",
			yaml: `
agents:
  - name: lead
    role: lead
  - name: rev
    role: reviewer
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := writeFile(t, t.TempDir(), "halo.yaml", tt.yaml)
			_, err := Load(p)
			if err == nil {
				t.Fatal("Load succeeded, want an error")
			}
			for _, want := range tt.wantMsg {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestLoadInvalidYAMLNamesPath(t *testing.T) {
	p := writeFile(t, t.TempDir(), "halo.yaml", "agents: [\n")
	_, err := Load(p)
	if err == nil {
		t.Fatal("Load succeeded, want an error")
	}
	if !strings.Contains(err.Error(), p) {
		t.Errorf("error %q does not contain path %q", err, p)
	}
}

func testConfig() *Config {
	return &Config{
		Prefix: "halo",
		Agents: []Agent{
			{Name: "lead", Model: "opus", Role: RoleLead},
			{Name: "haiku-1", Model: "haiku", Role: RoleWorker},
			{Name: "sonnet-1", Model: "sonnet", Role: RoleWorker},
			{Name: "haiku-2", Model: "haiku", Role: RoleWorker},
			{Name: "reviewer", Model: "sonnet", Role: RoleReviewer},
		},
	}
}

func TestHandleNameAgentLookups(t *testing.T) {
	c := testConfig()

	if got := c.Handle("lead"); got != "halo-lead" {
		t.Errorf("Handle(lead) = %q, want halo-lead", got)
	}
	if got := c.Name("halo-sonnet-1"); got != "sonnet-1" {
		t.Errorf("Name(halo-sonnet-1) = %q, want sonnet-1", got)
	}
	if got := c.Name("someone-else"); got != "someone-else" {
		t.Errorf("Name of foreign handle = %q, want it unchanged", got)
	}

	a, ok := c.Agent("halo-haiku-1")
	if !ok || a.Name != "haiku-1" {
		t.Errorf("Agent(halo-haiku-1) = %+v, %v", a, ok)
	}
	if _, ok := c.Agent("halo-nobody"); ok {
		t.Error("Agent(halo-nobody) found an agent, want none")
	}

	workers := c.ByRole(RoleWorker)
	if len(workers) != 3 {
		t.Errorf("ByRole(worker) = %d agents, want 3", len(workers))
	}
	if got := c.ByRole("nothing"); len(got) != 0 {
		t.Errorf("ByRole(nothing) = %v, want empty", got)
	}
	if got := c.Lead().Name; got != "lead" {
		t.Errorf("Lead() = %q, want lead", got)
	}
	if got := c.Reviewer().Name; got != "reviewer" {
		t.Errorf("Reviewer() = %q, want reviewer", got)
	}
}

func TestWorkerModelsDedupsInOrder(t *testing.T) {
	c := testConfig()
	got := c.WorkerModels()
	want := []string{"haiku", "sonnet"}
	if len(got) != len(want) {
		t.Fatalf("WorkerModels() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("WorkerModels() = %v, want %v", got, want)
		}
	}
}

func TestLoadStateMissingFileIsEmpty(t *testing.T) {
	isolateEnv(t)
	s, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if s.Server != "" || s.Key != "" || s.Human != "" || len(s.Tokens) != 0 {
		t.Errorf("LoadState() = %+v, want empty", s)
	}
}

func TestSaveLoadStateRoundTrip(t *testing.T) {
	p := isolateEnv(t)
	s := &State{
		Server: "https://lagrange.example",
		Key:    "k-123",
		Human:  "kaan",
	}
	s.SetToken("halo-lead", "tok-1")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.Server != s.Server || got.Key != s.Key || got.Human != s.Human {
		t.Errorf("round trip = %+v, want %+v", got, s)
	}
	if tok := got.Token("halo-lead"); tok != "tok-1" {
		t.Errorf("Token after round trip = %q, want tok-1", tok)
	}

	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("state mode = %o, want 600", mode)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file left behind: %v", err)
	}
}

func TestLoadStateTrimsTrailingSlash(t *testing.T) {
	p := isolateEnv(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Dir(p), "state.json", `{"server":"https://x.example///","key":"k"}`)

	got, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.Server != "https://x.example" {
		t.Errorf("Server = %q, want trailing slashes trimmed", got.Server)
	}
}

func TestLagrangeEnvOverridesSavedValues(t *testing.T) {
	isolateEnv(t)
	if err := (&State{Server: "https://saved.example", Key: "saved-key"}).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	t.Setenv("LAGRANGE_SERVER", "https://env.example/")
	t.Setenv("LAGRANGE_KEY", "env-key")

	got, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.Server != "https://env.example" {
		t.Errorf("Server = %q, want env value with slash trimmed", got.Server)
	}
	if got.Key != "env-key" {
		t.Errorf("Key = %q, want env-key", got.Key)
	}
}

func TestLoadStateCorruptJSONErrors(t *testing.T) {
	p := isolateEnv(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Dir(p), "state.json", "{not json")

	if _, err := LoadState(); err == nil {
		t.Fatal("LoadState succeeded on corrupt JSON, want an error")
	}
}

func TestStatePathHonorsEnv(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom.json")
	t.Setenv("HALO_STATE", want)
	got, err := StatePath()
	if err != nil {
		t.Fatalf("StatePath: %v", err)
	}
	if got != want {
		t.Errorf("StatePath() = %q, want %q", got, want)
	}
}

func TestTokenNilMap(t *testing.T) {
	var s State
	if got := s.Token("halo-lead"); got != "" {
		t.Errorf("Token on nil map = %q, want empty", got)
	}
}

func TestSetTokenNilMap(t *testing.T) {
	var s State
	s.SetToken("halo-lead", "tok")
	if s.Tokens == nil {
		t.Fatal("SetToken left the map nil")
	}
	if got := s.Token("halo-lead"); got != "tok" {
		t.Errorf("Token = %q, want tok", got)
	}
}

func TestTokensKeyedByServer(t *testing.T) {
	s := &State{Server: "https://a.example"}
	s.SetToken("halo-lead", "tok-a")
	if got := s.Token("halo-lead"); got != "tok-a" {
		t.Fatalf("Token on a = %q, want tok-a", got)
	}
	s.Server = "https://b.example"
	if got := s.Token("halo-lead"); got != "" {
		t.Errorf("Token on b = %q, want empty", got)
	}
}
