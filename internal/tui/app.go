package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/three-agent-problem/halo/internal/lg"
)

// Options configure the TUI.
type Options struct {
	Backend Backend
	State   *State
	Server  string
	Handles []string // the roster's agent handles, for the online count
	// Team is the roster this Halo hosts; nil when it only watches, for the reason
	// in Watching.
	Team     Team
	Watching string
	// Notes are messages from the agents' host (errors, restarts) for the header.
	Notes <-chan string
	// Feed is the raw output of the hosted agents; Procs their shells. Both are
	// nil when this Halo only watches.
	Feed  <-chan Update
	Procs Procs
	// Preview finds the live preview of the run with this short id: the run
	// worktree and the command that serves it, or ok=false for a non-web project.
	Preview func(short string) (dir, cmd string, ok bool)
}

type mode int

const (
	modeNormal mode = iota
	modePrompt
	modeAnswer
	modeNote
	modeName // naming a new agent
)

const promptHint = "describe what to build, then enter"

type (
	entryMsg   lg.Entry
	tickMsg    time.Time
	connMsg    struct{ ok bool }
	waitsMsg   []Wait
	onlineMsg  []string
	commitsMsg []Commit
	diffMsg    struct{ sha, text string }
	doneMsg    struct {
		what string
		err  error
	}
	pollMsg struct{}
)

type model struct {
	previewBranch string    // the run branch st.Preview was resolved for
	previewAt     time.Time // when it was last resolved
	o             Options
	st            *State
	ui            *UI
	w, h          int
	frame         int
	connected     bool
	online        int
	waits         []Wait
	mode          mode
	input         textarea.Model
	popSel        int  // the highlighted completion
	popOff        bool // esc closed the completion list
	files         []string
	filesFor      string // the run the file list was read for
	filesLoaded   bool
	decision      string
	flash         string
	flashAt       time.Time
	cancelArm     time.Time
	quitArm       time.Time
	deleteArm     string
	deleteAt      time.Time
	picker        *picker
	ctx           context.Context
}

func newModel(ctx context.Context, o Options) *model {
	in := newInput()
	return &model{o: o, st: o.State, ui: NewUI(), input: in, ctx: ctx}
}

// Run starts the TUI and blocks until it quits.
func Run(ctx context.Context, o Options) error {
	m := newModel(ctx, o)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	go func() {
		_ = o.Backend.Stream(ctx, func(e lg.Entry) { p.Send(entryMsg(e)) }, func(ok bool, _ error) { p.Send(connMsg{ok}) })
	}()
	if o.Notes != nil {
		go func() {
			for n := range o.Notes {
				p.Send(noteMsg(n))
			}
		}()
	}
	if o.Feed != nil {
		go func() {
			for u := range o.Feed {
				p.Send(u)
			}
		}()
	}
	_, err := p.Run()
	if err == tea.ErrProgramKilled && ctx.Err() != nil {
		return nil
	}
	return err
}

func tick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) poll() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return pollMsg{} })
}

func (m *model) fetch() tea.Cmd {
	cmds := []tea.Cmd{
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
			defer cancel()
			ws, err := m.o.Backend.Waiting(ctx)
			if err != nil {
				return nil
			}
			return waitsMsg(ws)
		},
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
			defer cancel()
			on, err := m.o.Backend.Online(ctx, m.o.Handles)
			if err != nil {
				return nil
			}
			return onlineMsg(on)
		},
	}
	if m.st.Root != "" {
		short, since := RunShort(m.st.Root), m.st.RootAt
		cmds = append(cmds, func() tea.Msg {
			cs, err := m.o.Backend.Commits(m.ctx, short, since)
			if err != nil {
				return nil
			}
			return commitsMsg(cs)
		})
	}
	return tea.Batch(cmds...)
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(tick(), m.fetch(), m.poll(), m.fetchRuns(true), m.fetchShells())
}

func (m *model) say(s string) { m.flash, m.flashAt = s, time.Now() }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.sizeInput()
	case tickMsg:
		m.frame++
		m.st.Prune(time.Time(msg))
		if m.flash != "" && time.Since(m.flashAt) > 5*time.Second {
			m.flash = ""
		}
		if m.frame%12 == 0 {
			return m, tea.Batch(tick(), m.wantOutput())
		}
		return m, tick()
	case pollMsg:
		return m, tea.Batch(m.fetch(), m.poll(), m.fetchShells())
	case Update:
		m.st.Feed(msg)
	case shellsMsg:
		c := m.ui.Cur[pShells]
		pid := 0
		if c.sel < len(m.st.Shells) {
			pid = m.st.Shells[c.sel].PID
		}
		m.st.Shells = msg
		for i, sh := range msg {
			if sh.PID == pid {
				c.sel = i
			}
		}
		if c.detail && pid != 0 && (c.sel >= len(msg) || msg[c.sel].PID != pid) {
			c.detail = false
			m.say(fmt.Sprintf("pid %d exited", pid))
		}
	case outMsg:
		m.ui.Out[msg.pid] = msg.text
	case entryMsg:
		root := m.st.Root
		m.st.Apply(lg.Entry(msg), time.Now())
		if m.st.Root != root {
			m.ui.Diffs = map[string]string{}
			m.filesLoaded = false
			m.ui.Cur[pCommits] = &pcursor{follow: true}
		}
		if msg.Kind == "task" && msg.Recipient == m.st.Human {
			return m, m.fetch()
		}
	case connMsg:
		m.connected = msg.ok
	case waitsMsg:
		m.waits = msg
		if len(m.waits) == 0 && (m.mode == modeAnswer || m.mode == modeNote) {
			m.mode = modeNormal
		}
	case onlineMsg:
		m.online = len(msg)
		m.st.SetOnline(msg)
	case commitsMsg:
		m.st.SetCommits(msg)
		return m, m.wantDiff()
	case diffMsg:
		m.ui.Diffs[msg.sha] = msg.text
	case noteMsg:
		m.say(string(msg))
	case runsMsg:
		switch {
		case msg.startup:
			m.startupHint(msg.runs)
		case m.picker != nil && msg.err != nil:
			m.picker = nil
			m.say("can't list runs: " + msg.err.Error())
		case m.picker != nil:
			m.picker.runs, m.picker.loading = msg.runs, false
		}
	case historyMsg:
		m.replay(msg)
		return m, m.fetch()
	case doneMsg:
		if msg.err != nil {
			m.say(msg.what + " failed: " + msg.err.Error())
		} else if msg.what != "" {
			m.say(msg.what)
		}
		return m, m.fetch()
	case filesMsg:
		if msg.root == m.st.Root {
			m.files = msg.files
		}
	case tea.PasteMsg:
		if m.inputText() && m.picker == nil && !m.ui.Help {
			m.paste(msg.Content)
		}
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

// wantDiff loads the diff of the selected commit when its detail is open.
func (m *model) wantDiff() tea.Cmd {
	c := m.ui.Cur[pCommits]
	if !c.detail || c.sel >= len(m.st.Commits) {
		return nil
	}
	sha := m.st.Commits[c.sel].SHA
	if _, ok := m.ui.Diffs[sha]; ok {
		return nil
	}
	return func() tea.Msg {
		text, err := m.o.Backend.Diff(m.ctx, sha)
		if err != nil {
			text = "git show failed: " + err.Error()
		}
		return diffMsg{sha, text}
	}
}

func (m *model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if key == "ctrl+c" {
		return m.quit()
	}
	if m.picker != nil {
		return m.pickerKey(k)
	}
	if m.ui.Help {
		switch key {
		case "esc":
			m.ui.Help, m.ui.Query = false, ""
		case "backspace":
			if r := []rune(m.ui.Query); len(r) > 0 {
				m.ui.Query = string(r[:len(r)-1])
			}
		case "ctrl+u":
			m.ui.Query = ""
		default:
			if key == "?" && m.ui.Query == "" {
				m.ui.Help = false
			} else if t := k.Text; t != "" {
				m.ui.Query += t
			}
		}
		return m, nil
	}
	switch m.mode {
	case modeName:
		switch key {
		case "esc":
			m.mode = modeNormal
			m.clearInput()
			return m, nil
		case "enter":
			name := m.input.Value()
			m.clearInput()
			m.mode = modeNormal
			return m, m.addAgent(name)
		}
		return m, m.typed(k)
	case modePrompt:
		if m.popupKey(key) {
			return m, nil
		}
		switch key {
		case "esc":
			m.mode = modeNormal
			m.input.Blur()
			return m, nil
		case "tab":
			if v := m.input.Value(); strings.HasPrefix(v, "/") {
				if cs := matchCommands(v); len(cs) > 0 {
					m.setInput(cs[0][0])
				}
			}
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.clearInput()
			m.mode = modeNormal
			if strings.HasPrefix(text, "/") {
				return m, m.command(text)
			}
			text = ExpandMentions(text, m.shortMap())
			m.st.Follow, m.st.Hint = true, ""
			return m, func() tea.Msg {
				_, err := m.o.Backend.Send(m.ctx, text)
				return doneMsg{"prompt sent to " + m.st.Short(m.st.Lead), err}
			}
		}
		return m, m.typed(k)
	case modeAnswer:
		switch key {
		case "esc", "q":
			m.mode = modeNormal
		case "y":
			m.decision, m.mode = "approve", modeNote
			return m, m.startInput("optional note, then enter to approve")
		case "n":
			m.decision, m.mode = "reject", modeNote
			return m, m.startInput("why? then enter to reject")
		case "r":
			m.decision, m.mode = "", modeNote
			return m, m.startInput("your answer, then enter")
		}
		return m, nil
	case modeNote:
		if m.popupKey(key) {
			return m, nil
		}
		switch key {
		case "esc":
			m.mode = modeAnswer
			m.clearInput()
			return m, nil
		case "enter":
			if len(m.waits) == 0 {
				m.mode = modeNormal
				return m, nil
			}
			w, text, decision := m.waits[0], strings.TrimSpace(m.input.Value()), m.decision
			if decision == "" && text == "" {
				return m, nil
			}
			m.clearInput()
			text = ExpandMentions(text, m.shortMap())
			m.mode = modeNormal
			m.waits = m.waits[1:]
			return m, func() tea.Msg {
				err := m.o.Backend.Answer(m.ctx, w.TaskID, text, decision)
				what := "answered"
				if decision != "" {
					what = decision + "d"
				}
				return doneMsg{what, err}
			}
		}
		return m, m.typed(k)
	}
	return m.normalKey(key)
}

func (m *model) normalKey(key string) (tea.Model, tea.Cmd) {
	u := m.ui
	n := m.st.Count(u.Focus, u)
	page := max(3, (m.h-4)/2)
	if len(key) == 1 && key[0] >= '1' && key[0] <= '0'+nPanes {
		u.Focus = int(key[0] - '0')
		return m, nil
	}
	if u.Focus == pRoster && !u.cur().detail {
		if cmd, ok := m.rosterKey(key); ok {
			return m, cmd
		}
	}
	if u.Focus == pPlan {
		if cmd, ok := m.previewKey(key); ok {
			return m, cmd
		}
	}
	if u.Focus == pShells && m.o.Procs != nil {
		if cmd, ok := m.shellKey(key); ok {
			return m, cmd
		}
	}
	switch key {
	case "q":
		return m.quit()
	case "?":
		u.Help, u.Query = true, ""
	case "i", "p":
		m.mode = modePrompt
		return m, m.startInput(promptHint)
	case "/":
		m.mode = modePrompt
		m.setInput("/")
		return m, m.startInput(promptHint)
	case "a":
		if len(m.waits) > 0 {
			m.mode = modeAnswer
		} else {
			m.say("nothing waits on you")
		}
	case "tab":
		u.FocusNext(1)
	case "shift+tab":
		u.FocusNext(-1)
	case "+", "=":
		u.Grow()
	case "_", "-":
		u.Shrink()
	case "j", "down":
		u.Move(1, n)
		return m, m.wantDiff()
	case "k", "up":
		u.Move(-1, n)
		return m, m.wantDiff()
	case "ctrl+d", "pgdown":
		u.Move(page, n)
	case "ctrl+u", "pgup":
		u.Move(-page, n)
	case "g", "home":
		u.Top()
	case "G", "end":
		u.Bottom(n)
	case "enter":
		return m.open(n)
	case "esc":
		switch c := u.cur(); {
		case c.detail:
			c.detail = false
		case u.Size[u.Focus] > sizeNormal:
			u.Size[u.Focus] = sizeNormal
		}
	case "h", "left", "l", "right":
		if u.Focus == pAgent {
			d := 1
			if key == "h" || key == "left" {
				d = -1
			}
			m.st.CycleFocus(time.Now(), d)
			u.Cur[pAgent] = &pcursor{follow: true}
		}
	case "v":
		if u.Focus == pAgent {
			u.Calls = !u.Calls
			u.Cur[pAgent] = &pcursor{follow: true}
			m.say(pick(u.Calls, "agent: the tool calls Lagrange saw", "agent: live output"))
		}
	case "f":
		if u.Focus == pComms {
			hs := m.st.commHandles()
			next := ""
			for i, h := range hs {
				if u.Filter == "" {
					next = h
					break
				}
				if h == u.Filter && i+1 < len(hs) {
					next = hs[i+1]
					break
				}
			}
			u.Filter = next
			u.Cur[pComms] = &pcursor{follow: true}
			if next == "" {
				m.say("comms: everyone")
			}
		}
	case "F":
		if u.Focus == pComms {
			u.Filter = ""
			u.Cur[pComms] = &pcursor{follow: true}
		}
	case "[", "]":
		if c := u.Cur[pCommits]; u.Focus == pCommits && c.detail {
			d := 1
			if key == "[" {
				d = -1
			}
			c.sel = max(0, min(len(m.st.Commits)-1, c.sel+d))
			c.follow, c.dOff = false, 0
			return m, m.wantDiff()
		}
	case "x":
		if m.st.Root == "" || terminal(m.st.RootState) {
			return m, nil
		}
		if time.Since(m.cancelArm) > 3*time.Second {
			m.cancelArm = time.Now()
			m.say("press x again to cancel the run")
			return m, nil
		}
		root := m.st.Root
		return m, func() tea.Msg { return doneMsg{"run canceled", m.o.Backend.Cancel(m.ctx, root)} }
	}
	return m, nil
}

// quit quits, after a second press when this Halo's agents are working on a run.
func (m *model) quit() (tea.Model, tea.Cmd) {
	if m.o.Team != nil && m.running() && time.Since(m.quitArm) > 3*time.Second {
		m.quitArm = time.Now()
		m.say("a run is in flight: press q again to stop the agents and quit")
		return m, nil
	}
	return m, tea.Quit
}

// open opens the selected item of the focused panel.
func (m *model) open(n int) (tea.Model, tea.Cmd) {
	u := m.ui
	c := u.cur()
	switch u.Focus {
	case pRoster:
		if c.sel < len(m.st.Order) && m.st.Order[c.sel] != m.st.Human {
			m.st.Focus = m.st.Order[c.sel]
			m.st.manualFocus = time.Now().Add(time.Hour)
			u.Focus, u.Cur[pAgent] = pAgent, &pcursor{follow: true}
		}
		return m, nil
	case pOrbit:
		u.Grow()
		return m, nil
	}
	if n == 0 && u.Focus != pAgent {
		if m.st.Root == "" {
			m.mode = modePrompt
			return m, m.startInput(promptHint)
		}
		return m, nil
	}
	if c.follow && n > 0 {
		c.sel = n - 1
	}
	c.follow, c.detail, c.dOff = false, true, 0
	if u.Focus == pShells {
		c.tail = true
		return m, m.wantOutput()
	}
	return m, m.wantDiff()
}

func (m *model) View() tea.View {
	if m.w == 0 {
		v := tea.NewView("")
		v.AltScreen = true
		return v
	}
	v := tea.NewView(m.render(time.Now()))
	v.AltScreen = true
	v.WindowTitle = "halo"
	return v
}

func (m *model) render(now time.Time) string {
	if pl := m.st.Plan; pl != nil && (pl.Branch != m.previewBranch ||
		(!m.st.Preview.Web() && now.Sub(m.previewAt) >= previewRecheck)) {
		// A run that isn't a web project yet may create package.json or index.html
		// in its worktree later, so look again every few seconds.
		m.previewBranch, m.previewAt = pl.Branch, now
		m.st.Preview = m.previewFor(pl.Branch)
	}
	sc := Screen{W: m.w, H: m.h, Frame: m.frame, Now: now, Server: m.o.Server, Connected: m.connected,
		Online: m.online, Waiting: len(m.waits), Flash: m.flash, UI: m.ui, Team: "team here", Procs: m.o.Procs != nil}
	if m.o.Team == nil {
		sc.Team = "watch-only"
	}
	sc.Footer = m.footer()
	sc.Popup = m.popupBox()
	if (m.mode == modeAnswer || m.mode == modeNote) && len(m.waits) > 0 {
		sc.Bottom, sc.BottomTag = m.answerPane(), fmt.Sprintf("%s asks you", m.st.Short(m.waits[0].Asker))
	}
	if m.picker != nil && m.w >= 80 && m.h >= 24 {
		sc.Overlay = m.pickerBox(min(m.w-4, 110), min(m.h-4, max(9, len(m.picker.shown())+6)))
	}
	return m.st.Render(sc)
}

// paneKeys are the footer hints of the focused panel.
var paneKeys = map[int][][2]string{
	pPlan: {{"enter", "subtask"}, {"o", "open preview"}, {"p", "start preview"}}, pOrbit: {{"enter", "bigger"}}, pAgent: {{"h/l", "agent"}, {"v", "live/calls"}, {"enter", "open"}},
	pComms: {{"enter", "read"}, {"f", "filter"}}, pCommits: {{"enter", "diff"}}, pEvents: {{"enter", "json"}}, pRoster: {{"enter", "watch"}, {"n", "add"}, {"m", "model"}, {"d", "remove"}},
	pShells: {{"enter", "output"}, {"o", "open port"}, {"d d", "stop"}},
}

func (m *model) footer() string {
	alert := ""
	if len(m.waits) > 0 && m.mode == modeNormal && (m.frame/8)%2 == 0 {
		alert = styles["alert"].Render(" A ▸ ANSWER ") + " "
	} else if len(m.waits) > 0 && m.mode == modeNormal {
		alert = c("ask!", " A ▸ ANSWER ") + " "
	}
	k := func(key, what string) string { return c("mute!", key) + c("dim", " "+what+"  ") }
	var keys string
	switch {
	case m.picker != nil:
		keys = k("↑↓", "choose") + k("enter", "resume") + k("type", "filter") + k("esc", "close")
	case m.ui.Help:
		keys = k("type", "search") + k("esc", "close")
	case m.mode == modePrompt && strings.HasPrefix(m.input.Value(), "/"):
		for _, cm := range matchCommands(m.input.Value()) {
			keys += c("hub!", cm[0]) + c("dim", " "+cm[1]+"  ")
		}
		keys += k("tab", "complete")
	case len(m.popupCands()) > 0:
		keys = k("↑↓", "choose") + k("tab/⏎", "accept") + k("esc", "close")
	case m.mode == modePrompt || m.mode == modeNote || m.mode == modeName:
		keys = k("⏎", "send") + k("esc", "back")
	case m.mode == modeAnswer:
		keys = k("y", "approve") + k("n", "reject") + k("r", "reply") + k("esc", "later")
	default:
		if m.ui.cur().detail {
			keys = k("j/k", "scroll") + k("esc", "back")
			if m.ui.Focus == pCommits {
				keys += k("[ ]", "prev/next")
			}
		} else {
			keys = k("1-8", "panel") + k("+/_", "size")
			for _, h := range paneKeys[m.ui.Focus] {
				keys += k(h[0], h[1])
			}
		}
		keys += k("i", "prompt") + k("?", "keys") + k("q", "quit")
	}
	if m.inputText() {
		return strings.Join(append(m.inputView(), " "+keys), "\n")
	}
	text := m.st.Prompt
	if text == "" {
		text = "press i to describe what to build"
	}
	return " " + alert + c("hub!", "› ") + c("mute", fit(firstLine(text), max(20, m.w-110))) + "  " + keys
}

func (m *model) answerPane() string {
	w := m.waits[0]
	width := max(20, m.w-8)
	var lines []string
	for _, l := range wrapLines(w.Question, width, 0) {
		lines = append(lines, c("hi", l))
	}
	lines = append(lines, "", c("dim", "waiting "+mmss(time.Since(w.Since))+" · task "+w.TaskID))
	if m.mode == modeAnswer {
		lines = append(lines, "", c("reply!", "y")+c("mute", " approve   ")+c("fail!", "n")+c("mute", " reject   ")+c("task!", "r")+c("mute", " reply with text   ")+c("dim", "esc later"))
	} else {
		lines = append(lines, "", c("ask!", strings.ToUpper(nonEmpty(m.decision, "reply"))+" ")+c("dim", "· type a note, enter to send"))
	}
	n := commsH - 2
	if len(lines) > n {
		lines = append(lines[:n-3], lines[len(lines)-3:]...)
	}
	return strings.Join(lines, "\n")
}
