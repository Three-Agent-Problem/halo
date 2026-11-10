package tui

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/three-agent-problem/halo/internal/config"
)

// halo setup: one screen to set Halo up the first time and to change it later, a
// section at a time (the server, the team, Telegram, preferences). Changes save as
// they are made; ctrl+r puts the section you are in back to how it was when setup
// opened (after a y/n). Telegram has no revert: its changes are on the server.

// SetupOptions is what setup works on.
type SetupOptions struct {
	State      *config.State
	Roster     *config.Config
	RosterPath string
	// First is a first run: the screen opens on the server, which must work.
	First bool
	// Check checks a server and API key and returns the person's handle.
	Check func(ctx context.Context, server, key string) (string, error)
	// Login is the server and key `lagrange login` saved, if any.
	Login func() (server, key string)
	// Provision creates the team's addresses on the server.
	Provision func(ctx context.Context, cfg *config.Config, st *config.State) error
	// Connector talks to a server's Telegram settings.
	Connector func(server, key string) Connector
	// LookPath finds a command on PATH (default exec.LookPath).
	LookPath func(string) (string, error)
}

// RunSetup runs the setup screen until it is closed.
func RunSetup(ctx context.Context, o SetupOptions) error {
	m := newSetup(ctx, o)
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if err == tea.ErrProgramKilled && ctx.Err() != nil {
		return nil
	}
	return err
}

// A coding agent Halo can run over ACP.
type codingAgent struct {
	Name, Bin, Cmd, Install string
	Models                  []string // its model names, best guess first; nil: type one
}

var codingAgents = []codingAgent{
	{"Claude Code", "claude-agent-acp", "claude-agent-acp", "npm i -g @agentclientprotocol/claude-agent-acp", []string{"sonnet", "haiku", "opus"}},
	{"Antigravity", "agy", "agy acp --process-per-session", "see antigravity.google", []string{"gemini-3.8-flash-low", "gemini-3.8-flash-medium", "gemini-3.8-flash-high"}},
	{"Codex", "codex-acp", "codex-acp", "npm i -g @zed-industries/codex-acp", nil},
	{"Gemini CLI", "gemini", "gemini --experimental-acp", "npm i -g @google/gemini-cli", nil},
	{"opencode", "opencode", "opencode acp", "see opencode.ai", nil},
	{"Goose", "goose", "goose acp", "see block.github.io/goose", nil},
}

const (
	secServer = iota
	secTeam
	secTelegram
	secPrefs
)

var setupSections = []string{"Lagrange", "Team", "Telegram", "Preferences"}

type setup struct {
	ctx  context.Context
	o    SetupOptions
	w, h int

	sec     int
	inside  bool // keys go to the section, not the sidebar
	frame   int
	flash   string
	flashAt time.Time

	// Server.
	url, key  string
	field     int  // 0 url, 1 key
	editing   bool // typing into field (server), the model, the command or the preview
	buf       string
	checking  bool
	serverErr string
	serverOK  bool

	// Team.
	found    map[string]string // agent bin → path
	teamPane int               // 0 coding agents, 1 the roster
	agentSel int
	rowSel   int
	edit     string // what editing types: "model", "cmd", "newcmd", "preview"
	armed    bool   // d pressed once on a row

	// Telegram.
	tg *connWizard

	// Preferences.
	prefSel int

	// How things were when setup opened, for a section's revert.
	origRoster *config.Config
	origState  config.State
	revertAsk  int // the section a y/n revert question is about, or -1
}

type (
	setupTick    struct{}
	setupChecked struct {
		server, key, handle string
		err                 error
	}
	setupProvisioned struct{ err error }
)

func newSetup(ctx context.Context, o SetupOptions) *setup {
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	s := &setup{ctx: ctx, o: o, url: o.State.Server, key: o.State.Key, found: map[string]string{}}
	s.serverOK = o.State.Server != "" && o.State.Key != "" && o.State.Human != ""
	s.origRoster, s.origState, s.revertAsk = cloneRoster(o.Roster), *o.State, -1
	for _, a := range codingAgents {
		if p, err := o.LookPath(a.Bin); err == nil {
			s.found[a.Bin] = p
		}
	}
	if s.url == "" {
		s.url = "http://localhost:8080"
		if srv, _ := s.login(); srv != "" {
			s.url = srv
		}
	}
	if o.First {
		s.inside = true
	}
	return s
}

func (s *setup) login() (string, string) {
	if s.o.Login == nil {
		return "", ""
	}
	return s.o.Login()
}

func (s *setup) say(f string, a ...any) { s.flash, s.flashAt = fmt.Sprintf(f, a...), time.Now() }

func (s *setup) Init() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return setupTick{} })
}

func (s *setup) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.w, s.h = msg.Width, msg.Height
	case setupTick:
		s.frame++
		if s.flash != "" && time.Since(s.flashAt) > 6*time.Second {
			s.flash = ""
		}
		return s, s.Init()
	case setupChecked:
		s.checking = false
		if msg.err != nil {
			s.serverErr, s.serverOK = msg.err.Error(), false
			return s, nil
		}
		st := s.o.State
		st.Server, st.Key, st.Human = msg.server, msg.key, msg.handle
		s.serverErr, s.serverOK, s.tg = "", true, nil
		if err := st.Save(); err != nil {
			s.serverErr = "saving: " + err.Error()
			return s, nil
		}
		s.say("✓ saved: you are @%s on %s", msg.handle, msg.server)
		return s, s.provision()
	case setupProvisioned:
		if msg.err != nil {
			s.say("couldn't create the team's addresses: %v", msg.err)
		}
	case tgMsg, tgPollMsg:
		if s.tg != nil {
			return s, s.tg.update(msg)
		}
	case tea.PasteMsg:
		switch {
		case s.editing:
			s.buf += strings.TrimSpace(msg.Content)
		case s.sec == secTelegram && s.tg != nil:
			s.tg.paste(msg.Content)
		}
	case tea.KeyPressMsg:
		return s, s.keyPress(msg)
	}
	return s, nil
}

func (s *setup) keyPress(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if sec := s.revertAsk; sec >= 0 {
		s.revertAsk = -1
		if key == "y" || key == "enter" {
			return s.revert(sec)
		}
		return nil
	}
	switch key {
	case "ctrl+c":
		return tea.Quit
	case "ctrl+r":
		s.editing = false
		s.askRevert()
		return nil
	}
	if s.editing {
		return s.typing(k)
	}
	if !s.inside {
		switch key {
		case "o":
			s.openLagrange()
		case "r":
			s.askRevert()
		case "q", "esc":
			return tea.Quit
		case "up", "k":
			s.sec = (s.sec + len(setupSections) - 1) % len(setupSections)
		case "down", "j", "tab":
			s.sec = (s.sec + 1) % len(setupSections)
		case "1", "2", "3", "4":
			s.sec = int(key[0] - '1')
			return s.enter()
		case "enter", "right", "l":
			return s.enter()
		}
		return nil
	}
	if key == "esc" || (key == "left" && s.sec != secTelegram) {
		s.inside, s.armed = false, false
		return nil
	}
	switch s.sec {
	case secServer:
		return s.serverKey(key)
	case secTeam:
		return s.teamKey(key)
	case secTelegram:
		if s.tg != nil {
			return s.tg.key(k)
		}
	case secPrefs:
		return s.prefsKey(key)
	}
	return nil
}

// enter moves the keys into the selected section.
func (s *setup) enter() tea.Cmd {
	s.inside = true
	if s.sec == secTelegram && s.tg == nil && s.serverOK && s.o.Connector != nil {
		var cmd tea.Cmd
		s.tg, cmd = newConnWizard(s.ctx, s.o.Connector(s.o.State.Server, s.o.State.Key))
		return cmd
	}
	return nil
}

// typing edits the open text field.
func (s *setup) typing(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		s.editing = false
	case "enter":
		s.editing = false
		return s.commit(strings.TrimSpace(s.buf))
	case "backspace":
		if r := []rune(s.buf); len(r) > 0 {
			s.buf = string(r[:len(r)-1])
		}
	case "ctrl+u":
		s.buf = ""
	default:
		s.buf += k.Text
	}
	return nil
}

func (s *setup) startEdit(what, value string) {
	s.edit, s.buf, s.editing = what, value, true
}

// commit saves what was typed.
func (s *setup) commit(v string) tea.Cmd {
	switch s.edit {
	case "url":
		s.url = strings.TrimRight(v, "/")
		if s.key != "" {
			return s.check(s.url, s.key)
		}
	case "key":
		if v == "" {
			return nil
		}
		s.key = v
		return s.check(s.url, s.key)
	case "model":
		if i := s.rowSel; i < len(s.o.Roster.Agents) {
			old := s.o.Roster.Agents[i].Model
			s.o.Roster.Agents[i].Model = v
			if !s.saveRoster() {
				s.o.Roster.Agents[i].Model = old
			}
		}
	case "cmd":
		if i := s.rowSel; i < len(s.o.Roster.Agents) && v != "" {
			old := s.o.Roster.Agents[i].ACP
			s.o.Roster.Agents[i].ACP = s.cmdFor(v)
			if !s.saveRoster() {
				s.o.Roster.Agents[i].ACP = old
			}
		}
	case "newcmd":
		if v != "" {
			return s.addWorker(codingAgent{Name: strings.Fields(v)[0], Cmd: v})
		}
	case "preview":
		s.o.Roster.Preview = v
		s.saveRoster()
	}
	return nil
}

// cmdFor stores the default coding agent as "" (the roster's acp).
func (s *setup) cmdFor(cmd string) string {
	if cmd == s.o.Roster.ACP || (s.o.Roster.ACP == "" && cmd == "claude-agent-acp") {
		return ""
	}
	return cmd
}

// saveRoster validates and writes the roster; false (with a message) when invalid.
func (s *setup) saveRoster() bool {
	if err := s.o.Roster.SaveRoster(s.o.RosterPath); err != nil {
		s.say("not saved: %v", err)
		return false
	}
	s.say("✓ saved")
	return true
}

func cloneRoster(c *config.Config) *config.Config {
	n := c.Clone()
	if c.Notify != nil {
		v := *c.Notify
		n.Notify = &v
	}
	return n
}

// changed reports whether a section differs from when setup opened.
func (s *setup) changed(sec int) bool {
	a, b := s.o.Roster, s.origRoster
	st, o := s.o.State, s.origState
	switch sec {
	case secServer:
		return st.Server != o.Server || st.Key != o.Key || st.Human != o.Human
	case secTeam:
		return !slices.Equal(a.Agents, b.Agents)
	case secPrefs:
		return a.Notifies() != b.Notifies() || a.Preview != b.Preview
	}
	return false
}

// askRevert asks y/n before reverting the selected section.
func (s *setup) askRevert() {
	switch {
	case s.sec == secTelegram:
		s.say("Telegram changes happen on the server: d d disconnects the bot")
	case !s.changed(s.sec):
		s.say("nothing changed in %s since setup opened", setupSections[s.sec])
	default:
		s.revertAsk = s.sec
	}
}

// revert puts a section back to how it was when setup opened, and saves that.
func (s *setup) revert(sec int) tea.Cmd {
	r, o := s.o.Roster, s.origRoster
	switch sec {
	case secServer:
		st := s.o.State
		st.Server, st.Key, st.Human = s.origState.Server, s.origState.Key, s.origState.Human
		s.url, s.key, s.serverErr, s.tg = st.Server, st.Key, "", nil
		if s.url == "" {
			s.url = "http://localhost:8080"
		}
		s.serverOK = st.Server != "" && st.Key != "" && st.Human != ""
		if err := st.Save(); err != nil {
			s.say("not saved: %v", err)
			return nil
		}
		s.say("↺ Server reverted")
		return nil
	case secTeam:
		r.Agents = slices.Clone(o.Agents)
		s.rowSel = min(s.rowSel, max(0, len(r.Agents)-1))
	case secPrefs:
		r.Preview = o.Preview
		r.Notify = cloneRoster(o).Notify
	}
	if s.saveRoster() {
		s.say("↺ %s reverted", setupSections[sec])
		if sec == secTeam {
			return s.provision()
		}
	}
	return nil
}

func (s *setup) provision() tea.Cmd {
	if !s.serverOK || s.o.Provision == nil {
		return nil
	}
	cfg, st := s.o.Roster.Clone(), s.o.State
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()
		return setupProvisioned{s.o.Provision(ctx, cfg, st)}
	}
}

// --- Server

func (s *setup) serverKey(key string) tea.Cmd {
	if s.checking {
		return nil
	}
	switch key {
	case "up", "k":
		s.field = 0
	case "down", "j":
		s.field = 1
	case "enter", "e":
		if s.field == 0 {
			s.startEdit("url", s.url)
		} else {
			s.startEdit("key", "")
		}
	case "c":
		return s.check(s.url, s.key)
	case "o":
		s.openLagrange()
	case "L":
		if srv, k := s.login(); srv != "" {
			s.url, s.key = srv, k
			return s.check(srv, k)
		}
	}
	return nil
}

func (s *setup) check(server, key string) tea.Cmd {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if server == "" || key == "" {
		s.serverErr = "it needs the server's URL and an API key (Lagrange → Settings → API keys)"
		return nil
	}
	if s.o.Check == nil {
		return nil
	}
	s.checking, s.serverErr = true, ""
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
		defer cancel()
		h, err := s.o.Check(ctx, server, key)
		return setupChecked{server: server, key: key, handle: h, err: err}
	}
}

// openLagrange opens the Lagrange web app in the browser, when there is one.
func (s *setup) openLagrange() {
	u := strings.TrimRight(nonEmpty(s.o.State.Server, s.url), "/")
	switch {
	case u == "":
		s.say("no Lagrange URL yet")
	case !canOpenBrowser():
		s.say("no browser here (SSH or no display): open %s yourself", u)
	default:
		if err := openURL(u); err != nil {
			s.say("can't open %s: %v", u, err)
		} else {
			s.say("opened %s", u)
		}
	}
}

// --- Team

// agentOf is the coding agent a roster entry runs, by its command.
func (s *setup) agentOf(a config.Agent) (codingAgent, bool) {
	cmd := s.o.Roster.Command(a)
	if cmd == "" {
		cmd = "claude-agent-acp"
	}
	for _, ca := range codingAgents {
		if cmd == ca.Cmd || strings.Fields(cmd)[0] == ca.Bin {
			return ca, true
		}
	}
	return codingAgent{Name: strings.Fields(cmd)[0], Cmd: cmd}, false
}

// uses reports how many roster agents run ca.
func (s *setup) uses(ca codingAgent) int {
	n := 0
	for _, a := range s.o.Roster.Agents {
		if got, ok := s.agentOf(a); ok && got.Bin == ca.Bin {
			n++
		}
	}
	return n
}

func (s *setup) teamKey(key string) tea.Cmd {
	agents := s.o.Roster.Agents
	if key == "tab" {
		s.teamPane, s.armed = 1-s.teamPane, false
		return nil
	}
	if s.teamPane == 0 {
		n := len(codingAgents) + 1 // + another command
		switch key {
		case "up", "k":
			s.agentSel = (s.agentSel + n - 1) % n
		case "down", "j":
			s.agentSel = (s.agentSel + 1) % n
		case "space", "enter":
			if s.agentSel == len(codingAgents) {
				s.startEdit("newcmd", "")
				return nil
			}
			ca := codingAgents[s.agentSel]
			if _, ok := s.found[ca.Bin]; !ok {
				s.say("%s isn't installed: %s", ca.Name, ca.Install)
				return nil
			}
			if s.uses(ca) > 0 {
				return s.dropAgent(ca)
			}
			return s.addWorker(ca)
		case "a":
			if s.agentSel < len(codingAgents) {
				ca := codingAgents[s.agentSel]
				if _, ok := s.found[ca.Bin]; ok {
					return s.addWorker(ca)
				}
			}
		}
		return nil
	}
	armed := s.armed
	s.armed = false
	if s.rowSel >= len(agents) {
		s.rowSel = max(0, len(agents)-1)
	}
	switch key {
	case "up", "k":
		s.rowSel = max(0, s.rowSel-1)
	case "down", "j":
		s.rowSel = min(len(agents)-1, s.rowSel+1)
	case "m":
		a := agents[s.rowSel]
		ca, _ := s.agentOf(a)
		if len(ca.Models) == 0 {
			s.startEdit("model", a.Model)
			return nil
		}
		i := slices.Index(ca.Models, a.Model)
		agents[s.rowSel].Model = ca.Models[(i+1)%len(ca.Models)]
		s.saveRoster()
	case "e":
		s.startEdit("model", agents[s.rowSel].Model)
	case "g":
		// The next installed coding agent.
		a := agents[s.rowSel]
		cur, _ := s.agentOf(a)
		var inst []codingAgent
		for _, ca := range codingAgents {
			if _, ok := s.found[ca.Bin]; ok {
				inst = append(inst, ca)
			}
		}
		if len(inst) < 2 {
			s.say("only one coding agent is installed; x types any ACP command")
			return nil
		}
		i := slices.IndexFunc(inst, func(ca codingAgent) bool { return ca.Bin == cur.Bin })
		next := inst[(i+1)%len(inst)]
		old := a
		agents[s.rowSel].ACP = s.cmdFor(next.Cmd)
		agents[s.rowSel].Model = ""
		if len(next.Models) > 0 {
			agents[s.rowSel].Model = next.Models[0]
		}
		if !s.saveRoster() {
			agents[s.rowSel] = old
		}
	case "x":
		s.startEdit("cmd", s.o.Roster.Command(agents[s.rowSel]))
	case "+", "=":
		agents[s.rowSel].Capacity = min(8, max(1, agents[s.rowSel].Capacity)+1)
		s.saveRoster()
	case "-":
		agents[s.rowSel].Capacity = max(1, agents[s.rowSel].Capacity-1)
		s.saveRoster()
	case "a":
		return s.addWorker(codingAgent{Name: "Claude Code", Bin: "claude-agent-acp", Cmd: "claude-agent-acp", Models: []string{"sonnet"}})
	case "d":
		a := agents[s.rowSel]
		if a.Role == config.RoleLead || a.Role == config.RoleReviewer {
			s.say("the team needs its %s: change its model or agent instead", a.Role)
			return nil
		}
		if !armed {
			s.armed = true
			s.say("press d again to remove %s", a.Name)
			return nil
		}
		old := slices.Clone(agents)
		s.o.Roster.Agents = slices.Delete(agents, s.rowSel, s.rowSel+1)
		if !s.saveRoster() {
			s.o.Roster.Agents = old
		}
		s.rowSel = min(s.rowSel, len(s.o.Roster.Agents)-1)
	}
	return nil
}

// addWorker adds a worker running ca, with its first model.
func (s *setup) addWorker(ca codingAgent) tea.Cmd {
	model := ""
	if len(ca.Models) > 0 {
		model = ca.Models[0]
	}
	base := model
	if base == "" || strings.Contains(base, "-") {
		base = strings.ToLower(strings.ReplaceAll(ca.Name, " ", "-"))
	}
	base = strings.Trim(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, strings.ToLower(base)), "-")
	if base == "" {
		base = "worker"
	}
	name := ""
	for i := 1; ; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
		if !slices.ContainsFunc(s.o.Roster.Agents, func(a config.Agent) bool { return a.Name == name }) {
			break
		}
	}
	old := slices.Clone(s.o.Roster.Agents)
	s.o.Roster.Agents = append(s.o.Roster.Agents, config.Agent{Name: name, Model: model, Role: config.RoleWorker, Capacity: 1, ACP: s.cmdFor(ca.Cmd)})
	if !s.saveRoster() {
		s.o.Roster.Agents = old
		return nil
	}
	s.say("✓ added %s (%s)", name, ca.Name)
	return s.provision()
}

// dropAgent removes the workers running ca (the lead and reviewer stay).
func (s *setup) dropAgent(ca codingAgent) tea.Cmd {
	old := slices.Clone(s.o.Roster.Agents)
	s.o.Roster.Agents = slices.DeleteFunc(s.o.Roster.Agents, func(a config.Agent) bool {
		got, ok := s.agentOf(a)
		return ok && got.Bin == ca.Bin && a.Role != config.RoleLead && a.Role != config.RoleReviewer
	})
	if len(s.o.Roster.Agents) == len(old) {
		s.say("%s runs the lead or the reviewer: switch those first (Team, g)", ca.Name)
		return nil
	}
	if !s.saveRoster() {
		s.o.Roster.Agents = old
	}
	return nil
}

// --- Preferences

func (s *setup) prefsKey(key string) tea.Cmd {
	switch key {
	case "up", "k":
		s.prefSel = 0
	case "down", "j":
		s.prefSel = 1
	case "space", "enter":
		if s.prefSel == 0 {
			on := !s.o.Roster.Notifies()
			s.o.Roster.Notify = &on
			s.saveRoster()
		} else {
			s.startEdit("preview", s.o.Roster.Preview)
		}
	}
	return nil
}

// --- View

func (s *setup) View() tea.View {
	v := tea.NewView(s.render())
	v.AltScreen = true
	v.WindowTitle = "halo setup"
	return v
}

func (s *setup) render() string {
	if s.w == 0 {
		return ""
	}
	side := 28
	main := max(40, s.w-side-1)
	h := max(10, s.h-2)
	left := strings.Split(box(side, h, c("hub!", "HALO SETUP"), "", s.sidebar(side-4), !s.inside), "\n")
	title := strings.ToUpper(setupSections[s.sec])
	right := strings.Split(box(main, h, c("task!", title), "", s.section(main-6, h-2), s.inside), "\n")
	var out []string
	for i := range h {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, fit(l, side)+" "+r)
	}
	return strings.Join(append(out, s.footer()), "\n")
}

func (s *setup) sidebar(w int) []string {
	lines := []string{""}
	for i, name := range setupSections {
		mark, sub := s.status(i)
		row := fmt.Sprintf("%d  %s", i+1, name)
		style := "fg"
		if i == s.sec {
			style = "hi!"
			row = "› " + row
		} else {
			row = "  " + row
		}
		if s.changed(i) {
			mark += c("ask", " changed")
		}
		lines = append(lines, " "+c(style, row)+" "+mark, "      "+c("mute", fit(sub, w-6)), "")
	}
	for _, l := range mascot(s.frame, 150*time.Millisecond) {
		lines = append(lines, " "+l)
	}
	lines = append(lines, "", " "+c("mute", "Changes save as you go."), " "+c("reply!", "ctrl+r")+c("mute", " reverts a section"), " "+c("mute", "Come back: halo setup"))
	return lines
}

// status is a section's mark and one line of what is set.
func (s *setup) status(sec int) (string, string) {
	ok, todo := c("reply!", "✓"), c("ask!", "!")
	switch sec {
	case secServer:
		if s.serverOK {
			return ok, "@" + s.o.State.Human
		}
		return todo, "start here"
	case secTeam:
		var names []string
		for _, ca := range codingAgents {
			if s.uses(ca) > 0 {
				names = append(names, ca.Name)
			}
		}
		return ok, fmt.Sprintf("%d agents · %s", len(s.o.Roster.Agents), strings.Join(names, ", "))
	case secTelegram:
		switch {
		case !s.serverOK:
			return c("dim", "·"), "needs Lagrange"
		case s.tg == nil || s.tg.st == nil:
			return c("dim", "·"), "optional"
		case s.tg.st.Paired:
			return ok, "@" + s.tg.st.BotUsername
		case s.tg.st.Connected:
			return todo, "waiting to pair"
		}
		return c("dim", "·"), "optional · not connected"
	case secPrefs:
		n := "notifications on"
		if !s.o.Roster.Notifies() {
			n = "notifications off"
		}
		return c("dim", "·"), n
	}
	return "", ""
}

func (s *setup) section(w, h int) []string {
	var lines []string
	switch s.sec {
	case secServer:
		lines = s.serverView(w)
	case secTeam:
		lines = s.teamView(w)
	case secTelegram:
		switch {
		case !s.serverOK:
			lines = []string{"", "  " + c("mute", "Telegram lives in Lagrange: set up Lagrange first (1).")}
		case s.tg == nil:
			lines = []string{"", "  " + c("fg", "Get Halo's questions on your phone and answer them with buttons."), "", "  " + c("reply!", "enter") + c("mute", " set it up")}
		default:
			lines = s.tg.lines(w, s.frame, "back")
		}
	case secPrefs:
		lines = s.prefsView(w)
	}
	if len(lines) > h {
		lines = append(lines[:h-1], "  "+c("dim", "…"))
	}
	return lines
}

func (s *setup) wrap(w int, style, text string) []string {
	var out []string
	for _, l := range strings.Split(ansi.Wordwrap(text, w, ""), "\n") {
		out = append(out, "  "+c(style, l))
	}
	return out
}

// input draws a field: its value, or the text being typed into it.
func (s *setup) input(what, value string, secret bool) string {
	if s.editing && s.edit == what {
		v := s.buf
		if secret {
			v = strings.Repeat("•", len([]rune(v)))
		}
		return c("hi", v) + c("reply!", "▏")
	}
	if secret && value != "" {
		r := []rune(value)
		return c("fg", strings.Repeat("•", 8)+string(r[max(0, len(r)-4):]))
	}
	if value == "" {
		return c("dim", "(none)")
	}
	return c("fg", value)
}

func (s *setup) serverView(w int) []string {
	lines := []string{""}
	lines = append(lines, s.wrap(w, "fg", "Lagrange is where Halo's agents get their addresses and where every run is recorded. Halo needs its URL and an API key (Lagrange → Settings → API keys).")...)
	lines = append(lines, "")
	cur := func(i int) string {
		if s.inside && s.field == i {
			return c("hub!", "› ")
		}
		return "  "
	}
	lines = append(lines,
		"  "+cur(0)+c("mute", "server   ")+s.input("url", s.url, false),
		"  "+cur(1)+c("mute", "API key  ")+s.input("key", s.key, true),
		"")
	switch {
	case s.checking:
		lines = append(lines, "  "+c("hub", spinner(s.frame)+" checking…"))
	case s.serverErr != "":
		lines = append(lines, s.wrap(w, "fail", s.serverErr)...)
	case s.serverOK:
		lines = append(lines, "  "+c("reply!", "✓ ")+c("fg", "you are ")+c("hi!", "@"+s.o.State.Human)+c("fg", " on "+s.o.State.Server))
	}
	hints := "  " + keyHint("↑↓", "field") + keyHint("enter", "edit") + keyHint("c", "check and save")
	if canOpenBrowser() {
		hints += keyHint("o", "open in the browser")
	}
	lines = append(lines, "", hints)
	if srv, _ := s.login(); srv != "" {
		lines = append(lines, "  "+keyHint("L", "use your lagrange login ("+srv+")"))
	}
	return lines
}

func (s *setup) teamView(w int) []string {
	lines := []string{""}
	lines = append(lines, s.wrap(w, "fg", "Every agent is a coding agent Halo runs over ACP. The lead plans and asks you, workers code in their own worktrees, the reviewer approves merges.")...)
	head := func(pane int, t string) string {
		if s.inside && s.teamPane == pane {
			return "  " + c("hub!", t)
		}
		return "  " + c("mute", t)
	}
	lines = append(lines, "", head(0, "CODING AGENTS ON THIS MACHINE"))
	for i, ca := range codingAgents {
		cur := "  "
		if s.inside && s.teamPane == 0 && s.agentSel == i {
			cur = c("hub!", "› ")
		}
		box := c("dim", "[ ]")
		if n := s.uses(ca); n > 0 {
			box = c("reply!", "[✓]")
		}
		var what string
		if p, ok := s.found[ca.Bin]; ok {
			what = c("hi", fit(ca.Name, 12)) + c("mute", " "+p)
			if n := s.uses(ca); n > 0 {
				what += c("reply", fmt.Sprintf("  · %d in the team", n))
			}
		} else {
			box = c("dim", " ✗ ")
			what = c("dim", fit(ca.Name, 12)+" not installed · "+ca.Install)
		}
		lines = append(lines, "  "+cur+box+" "+fit(what, w-8))
	}
	cur := "  "
	if s.inside && s.teamPane == 0 && s.agentSel == len(codingAgents) {
		cur = c("hub!", "› ")
	}
	other := c("mute", "+ another ACP command…")
	if s.editing && s.edit == "newcmd" {
		other = c("mute", "command  ") + s.input("newcmd", "", false)
	}
	lines = append(lines, "  "+cur+"    "+other)

	lines = append(lines, "", head(1, "THE TEAM"))
	lines = append(lines, "    "+c("dim", fit("name", 14)+fit("role", 10)+fit("agent", 14)+fit("model", 24)+"slots"))
	for i, a := range s.o.Roster.Agents {
		cur := "  "
		if s.inside && s.teamPane == 1 && s.rowSel == i {
			cur = c("hub!", "› ")
		}
		ca, _ := s.agentOf(a)
		model := a.Model
		if model == "" {
			model = "(its default)"
		}
		mcell := c(modelKey(a.Model), fit(model, 23)) + " "
		if s.editing && s.edit == "model" && s.rowSel == i {
			mcell = fit(s.input("model", "", false), 24)
		}
		agent := ca.Name
		if s.editing && s.edit == "cmd" && s.rowSel == i {
			agent = ""
		}
		row := c("hi", fit(a.Name, 14)) + c("mute", fit(a.Role, 10)) + c("fg", fit(agent, 14)) + mcell + c("mute", fmt.Sprint(max(1, a.Capacity)))
		lines = append(lines, "  "+cur+fit(row, w-4))
		if s.editing && s.edit == "cmd" && s.rowSel == i {
			lines = append(lines, "      "+c("mute", "ACP command  ")+s.input("cmd", "", false))
		}
	}
	lines = append(lines, "")
	if s.teamPane == 0 {
		lines = append(lines, "  "+keyHint("space", "use it / stop using it")+keyHint("a", "add one more")+keyHint("tab", "the team"))
	} else {
		lines = append(lines, "  "+keyHint("m", "next model")+keyHint("e", "type a model")+keyHint("g", "next agent")+keyHint("x", "type a command"),
			"  "+keyHint("+/-", "slots")+keyHint("a", "add")+keyHint("d d", "remove")+keyHint("tab", "coding agents"))
	}
	return lines
}

func (s *setup) prefsView(w int) []string {
	lines := []string{""}
	cur := func(i int) string {
		if s.inside && s.prefSel == i {
			return c("hub!", "› ")
		}
		return "  "
	}
	on := c("reply!", "on")
	if !s.o.Roster.Notifies() {
		on = c("mute", "off")
	}
	lines = append(lines,
		"  "+cur(0)+c("fg", fit("Desktop notifications", 24))+on,
		"      "+c("mute", "a notification and a bell when something waits on you and the terminal isn't focused"),
		"",
		"  "+cur(1)+c("fg", fit("Preview command", 24))+s.input("preview", s.o.Roster.Preview, false),
		"      "+c("mute", "serves the project for the live preview (e.g. npm run dev); empty: detect from package.json"),
		"",
		"  "+c("mute", "Team file  ")+c("fg", s.o.RosterPath),
		"",
		"  "+keyHint("↑↓", "choose")+keyHint("space", "toggle")+keyHint("enter", "edit"))
	_ = w
	return lines
}

func (s *setup) footer() string {
	var keys string
	switch {
	case s.revertAsk >= 0:
		return fit(" "+c("ask!", "Revert "+setupSections[s.revertAsk]+" to how it was when setup opened? ")+keyHint("y", "yes")+keyHint("n", "no"), s.w)
	case s.editing:
		keys = keyHint("enter", "save") + keyHint("esc", "cancel") + keyHint("ctrl+u", "clear")
	case s.inside:
		keys = keyHint("esc", "back") + keyHint("ctrl+r", "revert this section") + keyHint("ctrl+c", "quit")
	default:
		keys = keyHint("↑↓", "section") + keyHint("enter", "open") + keyHint("r", "revert")
		if canOpenBrowser() {
			keys += keyHint("o", "open Lagrange")
		}
		keys += keyHint("q", "done")
	}
	out := " " + keys
	if s.flash != "" {
		out += c("hub", s.flash)
	}
	return fit(out, s.w)
}

func keyHint(k, what string) string { return c("reply!", k) + c("mute", " "+what+"   ") }
