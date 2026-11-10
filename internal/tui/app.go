package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/three-agent-problem/lagrange/client"

	"github.com/three-agent-problem/halo/internal/lg"
	"github.com/three-agent-problem/halo/internal/plan"
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
	// ImageDir is where images pasted into a prompt are kept for the agents to
	// read; "" uses a folder in the system's temp directory.
	ImageDir string
	// Quiet turns off the ping (a desktop notification and a bell) when
	// something new waits on you.
	Quiet bool
}

type mode int

const (
	modeNormal mode = iota
	modePrompt
	modeAnswer
	modeNote
	modeName      // naming a new agent
	modeMessage   // a message to one agent, via the lead or direct
	modeQuestions // answering the lead's multiple-choice questions
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
	msg           msgTarget // who modeMessage writes to
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
	quitAsk       bool              // the quit dialog is open
	summary       bool              // the run summary card is open
	conn          *connWizard       // the /connections dialog, when open
	tgPaired      bool              // a Telegram bot is paired (footer mark)
	tgTip         bool              // the Telegram tip was shown this session
	confirm       *confirm          // a y/n question in front of an action
	blurred       bool              // the terminal reported losing focus
	focusKnown    bool              // ... so it reports focus at all
	seenWaits     map[string]bool   // waits already pinged about
	images        map[string]string // image chip in the input → the copied file
	qf            *qform            // the questions being answered (kept across esc)
	deleteArm     string
	deleteAt      time.Time
	picker        *picker
	runs          []RunInfo // your runs, listed beside the full-screen comms
	runSel        int
	pendingSize   Size // the comms size to keep once a run opened from the list loads
	ctx           context.Context
}

func newModel(ctx context.Context, o Options) *model {
	in := newInput()
	o.State.Hosted = o.Feed != nil
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
	cmds := []tea.Cmd{tick(), m.fetch(), m.poll(), m.fetchRuns(true), m.fetchShells()}
	if m.connector() != nil {
		// Whether Telegram is paired: the footer shows it, and the tip waits for it.
		cmds = append(cmds, tgDo(m.ctx, m.connector(), func(ctx context.Context, c Connector) (*client.Telegram, error) { return c.Telegram(ctx) }, "", true))
	}
	return tea.Batch(cmds...)
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
		if m.st.ReportNew {
			m.st.ReportNew, m.summary = false, true
		}
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
	case tea.FocusMsg:
		m.blurred, m.focusKnown = false, true
	case tea.BlurMsg:
		m.blurred, m.focusKnown = true, true
	case waitsMsg:
		ping := m.newWaits(msg)
		m.waits = msg
		if ping != nil && !m.tgPaired && !m.tgTip && m.connector() != nil && !m.o.Quiet {
			m.tgTip = true
			m.say("tip: /connections sends what waits on you to Telegram, with buttons to answer")
		}
		if len(m.waits) == 0 && (m.mode == modeAnswer || m.mode == modeNote) {
			m.mode = modeNormal
		}
		if m.qf != nil && !slices.ContainsFunc(m.waits, func(w Wait) bool { return w.TaskID == m.qf.w.TaskID }) {
			// Answered elsewhere (the web inbox), or the run ended.
			m.qf = nil
			if m.mode == modeQuestions {
				m.mode = modeNormal
			}
		}
		if ping != nil && !m.o.Quiet && (m.blurred || !m.focusKnown) {
			return m, tea.Raw(notifySeq(fmt.Sprintf("%s asks you: %s", m.st.Short(ping.Asker), firstLine(ping.Question))))
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
		if msg.err == nil {
			m.runs = msg.runs
			m.runSel = min(m.runSel, max(0, len(m.runs)-1))
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
	case clipMsg:
		switch {
		case errors.Is(msg.err, errNoImage):
			return m, m.typed(msg.key) // no image: a plain text paste
		case msg.err != nil:
			m.say("couldn't read the clipboard: " + msg.err.Error())
		case m.inputText():
			m.chip(msg.path, "clipboard.png")
		}
	case filesMsg:
		if msg.root == m.st.Root {
			m.files = msg.files
		}
	case tgMsg, tgPollMsg:
		return m, m.tgUpdate(msg)
	case tea.PasteMsg:
		if m.conn != nil {
			m.connPaste(msg.Content)
		} else if m.mode == modeQuestions && m.qf != nil {
			m.qf.paste(msg.Content)
		} else if m.inputText() && m.picker == nil && !m.ui.Help {
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
	sha, task := m.st.Commits[c.sel].SHA, m.st.Commits[c.sel].Task
	if _, ok := m.ui.Diffs[sha]; ok {
		return nil
	}
	return func() tea.Msg {
		text, err := m.o.Backend.Diff(m.ctx, sha, task)
		if err != nil {
			text = "git show failed: " + err.Error()
		}
		return diffMsg{sha, text}
	}
}

func (m *model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if m.quitAsk {
		switch key {
		case "y", "Y", "enter", "ctrl+c":
			return m, tea.Quit
		case "n", "N", "esc", "q":
			m.quitAsk = false
		}
		return m, nil
	}
	if key == "ctrl+c" {
		return m.quit()
	}
	if key == "ctrl+v" && m.inputText() && m.mode != modeName {
		return m, m.clipImage(k)
	}
	if m.confirm != nil {
		cf := m.confirm
		switch key {
		case "y", "Y", "enter":
			m.confirm = nil
			return m, cf.yes()
		case "n", "N", "esc", "q":
			m.confirm = nil
		}
		return m, nil
	}
	if m.conn != nil {
		return m, m.connKey(k)
	}
	if m.summary {
		switch key {
		case "c":
			if r := m.st.summaryReport(); r != nil {
				m.say("summary copied as Markdown")
				return m, tea.SetClipboard(r.Markdown())
			}
		case "esc", "q", "enter":
			m.summary = false
		}
		return m, nil
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
	case modeQuestions:
		if m.qf == nil {
			m.mode = modeNormal
			return m, nil
		}
		if key == "esc" {
			m.mode = modeNormal
			m.say("answer later with a")
			return m, nil
		}
		if !m.qf.key(k) {
			return m, nil
		}
		w, text := m.qf.w, m.qf.text()
		m.qf, m.mode = nil, modeNormal
		m.dropWait(w.TaskID)
		ctx, b := m.ctx, m.o.Backend
		return m, func() tea.Msg { return doneMsg{"answers sent", b.Answer(ctx, w.TaskID, text, "")} }
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
			return m, m.submit(text)
		}
		return m, m.typed(k)
	case modeMessage:
		if m.popupKey(key) {
			return m, nil
		}
		switch key {
		case "esc":
			m.mode = modeNormal
			m.clearInput()
			return m, nil
		case "tab":
			if m.msg.to != m.st.Lead {
				m.msg.direct = !m.msg.direct
				m.input.Placeholder = m.msgHint()
			}
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			t := m.msg
			m.clearInput()
			m.mode = modeNormal
			text, _ = m.expandImages(ExpandMentions(text, m.shortMap()))
			ctx, b, who := m.ctx, m.o.Backend, m.st.Short(t.to) // read here: the cmd runs off the update loop
			return m, func() tea.Msg {
				err := b.Message(ctx, t.to, t.sub, text, t.direct)
				return doneMsg{"message sent to " + who, err}
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
			text, _ = m.expandImages(ExpandMentions(text, m.shortMap()))
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
		u.SetFocus(int(key[0] - '0'))
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
		if z := u.Size[pPlan]; z > sizeNormal && !u.cur().detail && m.st.boardMove(u, key, z < sizeFull && boardWide(m.planW())) {
			return m, nil
		}
	}
	if u.Focus == pShells && m.o.Procs != nil {
		if cmd, ok := m.shellKey(key); ok {
			return m, cmd
		}
	}
	if u.Focus == pComms && u.grown() == sizeFull {
		// The runs beside the full-screen comms.
		switch key {
		case "[", "]":
			d := 1
			if key == "[" {
				d = -1
			}
			m.runSel = max(0, min(len(m.runs)-1, m.runSel+d))
			return m, nil
		case "o":
			if m.runSel < len(m.runs) {
				r := m.runs[m.runSel]
				if r.ID == m.st.Root {
					m.say("that run is the one on screen")
					return m, nil
				}
				size := u.Size[pComms]
				cmd := m.resume(r)
				m.pendingSize = size
				return m, cmd
			}
			return m, nil
		}
	}
	switch key {
	case "q":
		return m.quit()
	case "?":
		u.Help, u.Query = true, ""
	case "i", "p":
		m.mode = modePrompt
		return m, m.startInput(m.promptHint())
	case "/":
		m.mode = modePrompt
		m.setInput("/")
		return m, m.startInput(m.promptHint())
	case "m":
		if u.Focus == pPlan || u.Focus == pOrbit || u.Focus == pAgent {
			return m, m.startMessage()
		}
	case "a":
		if len(m.waits) > 0 && len(m.waits[0].Questions) > 0 {
			if m.qf == nil || m.qf.w.TaskID != m.waits[0].TaskID {
				m.qf = newQForm(m.waits[0])
			}
			m.mode = modeQuestions
		} else if len(m.waits) > 0 {
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
		if u.Focus == pComms && u.grown() == sizeFull {
			return m, m.fetchRuns(false)
		}
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
		if st := m.stoppable(); st != nil {
			sid, who, ctx, b := st.ID, m.st.Short(st.Worker), m.ctx, m.o.Backend
			m.confirm = &confirm{
				title: "STOP",
				q:     fmt.Sprintf("Stop %s %s (%s %s)?", st.ID, st.Title, st.Model, who),
				what:  "Its Claude turn is canceled; the lead asks you what next.",
				yes: func() tea.Cmd {
					return func() tea.Msg { return doneMsg{"stopping " + sid, b.StopSubtask(ctx, sid)} }
				},
			}
			return m, nil
		}
		if m.st.Root == "" || terminal(m.st.RootState) {
			return m, nil
		}
		if time.Since(m.cancelArm) > 3*time.Second {
			m.cancelArm = time.Now()
			m.say("press x again to cancel the whole run")
			return m, nil
		}
		root := m.st.Root
		return m, func() tea.Msg { return doneMsg{"run canceled", m.o.Backend.Cancel(m.ctx, root)} }
	}
	return m, nil
}

// promptHint is the prompt's placeholder: what enter will do.
func (m *model) promptHint() string {
	if m.running() {
		return "ask the lead anything on the side (btw), enter · /new starts another run"
	}
	return promptHint
}

// submit sends what was typed at the prompt. During a run it is a btw: the lead
// answers it between its steps and no run starts. Otherwise it starts a run,
// after a y/n when that replaces a run on screen.
func (m *model) submit(text string) tea.Cmd {
	if m.running() {
		return m.btw(text)
	}
	if m.st.Root != "" {
		return m.confirmRun(text)
	}
	return m.startRun(text)
}

// btw asks the lead text on the side of the run on screen.
func (m *model) btw(text string) tea.Cmd {
	if !m.running() {
		m.say("no run to ask about · just send it as a prompt")
		return nil
	}
	text, _ = m.expandImages(ExpandMentions(text, m.shortMap()))
	ctx, b, root, who := m.ctx, m.o.Backend, m.st.Root, m.st.Short(m.st.Lead)
	return func() tea.Msg {
		return doneMsg{"btw sent to " + who + " · it answers when it can", b.Btw(ctx, root, text)}
	}
}

// confirmRun asks before text starts a new run.
func (m *model) confirmRun(text string) tea.Cmd {
	q := "Start a new run with this prompt?"
	what := "The lead plans it and the team builds it, next to the run on screen."
	if !m.running() {
		what = "The lead plans it in the same conversation, so it remembers this run."
	}
	m.confirm = &confirm{title: "NEW RUN", q: q, what: what, yes: func() tea.Cmd { return m.startRun(text) }}
	return nil
}

// startRun sends text to the lead as a new run.
func (m *model) startRun(text string) tea.Cmd {
	text, images := m.expandImages(ExpandMentions(text, m.shortMap()))
	m.st.Follow, m.st.Hint = true, ""
	ctx, b, who := m.ctx, m.o.Backend, m.st.Short(m.st.Lead)
	return func() tea.Msg {
		_, err := b.Send(ctx, text, images)
		return doneMsg{"prompt sent to " + who, err}
	}
}

// confirm is a y/n dialog in front of an action.
type confirm struct {
	title, q, what string
	yes            func() tea.Cmd
}

func (cf *confirm) box() []string {
	lines := []string{"", "  " + c("hi!", cf.q), "  " + c("mute", cf.what), "",
		"  " + c("fail!", "y") + c("mute", " yes   ") + c("reply!", "n") + c("mute", " no"), ""}
	w := 4 + max(ansi.StringWidth(cf.q), ansi.StringWidth(cf.what)) + 4
	return strings.Split(box(max(w, 40), len(lines)+2, c("ask!", cf.title), "", lines, true), "\n")
}

// stoppable is the running subtask x stops: the one selected in the plan, or
// the focused agent's in the agent panel.
func (m *model) stoppable() *plan.Subtask {
	if m.st.Plan == nil {
		return nil
	}
	var st *plan.Subtask
	switch m.ui.Focus {
	case pPlan:
		if sel := m.ui.Cur[pPlan].sel; sel < len(m.st.Plan.Subtasks) {
			st = m.st.Plan.Subtasks[sel]
		}
	case pAgent:
		if a := m.st.Agents[m.st.Focus]; a != nil && a.Sub != "" {
			st = m.st.Plan.Get(a.Sub)
		}
	}
	if st == nil || (st.Status != plan.Running && st.Status != plan.Asking) {
		return nil
	}
	return st
}

// newWaits returns the first wait in ws Halo hasn't seen before, and remembers
// them all. The first list after start is only remembered: you're looking.
func (m *model) newWaits(ws []Wait) *Wait {
	first := m.seenWaits == nil
	if first {
		m.seenWaits = map[string]bool{}
	}
	var fresh *Wait
	for i := range ws {
		if !m.seenWaits[ws[i].TaskID] {
			m.seenWaits[ws[i].TaskID] = true
			if fresh == nil && !first {
				fresh = &ws[i]
			}
		}
	}
	return fresh
}

// notifySeq is a desktop notification (OSC 9: iTerm2, Ghostty, WezTerm, kitty,
// Windows Terminal) followed by a bell for the terminals without it.
func notifySeq(text string) string {
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0x9c {
			return ' '
		}
		return r
	}, "halo: "+text)
	if r := []rune(text); len(r) > 120 {
		text = string(r[:119]) + "…"
	}
	return "\x1b]9;" + text + "\x07\a"
}

// dropWait forgets a wait the human just answered, so the header updates
// before the next poll.
func (m *model) dropWait(taskID string) {
	m.waits = slices.DeleteFunc(m.waits, func(w Wait) bool { return w.TaskID == taskID })
}

// planW is the plan panel's inner width at its current size.
func (m *model) planW() int {
	switch {
	case m.w < minWide || m.ui.Size[pPlan] == sizeFull:
		return m.w - 4
	case m.ui.Size[pPlan] == sizeWide:
		return m.w - rightW - 4
	}
	return leftW - 4
}

// quit opens the "Quit Halo?" dialog: quitting stops the hosted agents and
// leaves the run, so it is never one stray key.
func (m *model) quit() (tea.Model, tea.Cmd) {
	m.quitAsk = true
	return m, nil
}

// quitBox is the quit dialog.
func (m *model) quitBox() []string {
	what := "The run stays in Lagrange; /resume picks it up."
	if m.o.Team != nil && m.running() {
		what = "A run is in flight: the agents hosted here stop."
	} else if m.o.Team != nil {
		what = "The agents hosted here stop."
	}
	lines := []string{"", "  " + c("hi!", "Quit Halo?"), "  " + c("mute", what), "",
		"  " + c("fail!", "y") + c("mute", " quit   ") + c("reply!", "n") + c("mute", " stay"), ""}
	w := 4 + ansi.StringWidth(what) + 4
	return strings.Split(box(max(w, 40), len(lines)+2, c("ask!", "QUIT"), "", lines, true), "\n")
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
			u.SetFocus(pAgent)
			u.Cur[pAgent] = &pcursor{follow: true}
		}
		return m, nil
	case pOrbit:
		u.Grow()
		return m, nil
	}
	if n == 0 && u.Focus != pAgent {
		if m.st.Root == "" {
			m.mode = modePrompt
			return m, m.startInput(m.promptHint())
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
	v.ReportFocus = true // to ping only when you're looking elsewhere
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
		Online: m.online, Waiting: len(m.waits), Flash: m.flash, UI: m.ui, Team: "team here", Procs: m.o.Procs != nil,
		Runs: m.runs, RunSel: m.runSel}
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
	if m.mode == modeQuestions && m.qf != nil && m.w >= 80 && m.h >= 24 {
		sc.Overlay = m.qf.box(m.st, min(m.w-8, 100), m.h-4, m.frame)
	}
	if m.conn != nil && m.w >= 60 && m.h >= 16 {
		sc.Overlay = m.connBox(min(m.w-8, 84))
	}
	if m.summary && m.w >= 60 && m.h >= 16 {
		sc.Overlay = m.st.summaryBox(min(m.w-8, 84), m.h-4)
	}
	if m.confirm != nil && m.w >= 80 && m.h >= 24 {
		sc.Overlay = m.confirm.box()
	}
	if m.quitAsk && m.w >= 80 && m.h >= 24 {
		sc.Overlay = m.quitBox()
	}
	return m.st.Render(sc)
}

// paneKeys are the footer hints of the focused panel.
var paneKeys = map[int][][2]string{
	pPlan: {{"enter", "subtask"}, {"o", "open preview"}, {"p", "start preview"}, {"m", "message"}, {"x", "stop worker"}}, pOrbit: {{"enter", "bigger"}, {"m", "message"}}, pAgent: {{"h/l", "agent"}, {"v", "live/calls"}, {"enter", "open"}, {"m", "message"}, {"x", "stop"}},
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
	case m.quitAsk:
		keys = k("y/⏎", "quit") + k("n/esc", "stay")
	case m.confirm != nil:
		keys = k("y/⏎", "yes") + k("n/esc", "no")
	case m.conn != nil:
		keys = k("esc", "close")
	case m.summary:
		keys = k("c", "copy as Markdown") + k("esc", "close")
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
	case m.mode == modeQuestions:
		keys = k("↑↓", "choose") + k("⏎/space", "pick") + k("←→", "questions") + k("esc", "later")
	case m.mode == modeMessage:
		keys = k("⏎", "send") + k("tab", "direct/lead") + k("esc", "cancel")
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
			keys = k("1-8", "panel") + k("+/-", "size")
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
	if m.tgPaired {
		alert = c("task", "✈ ") + alert
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
