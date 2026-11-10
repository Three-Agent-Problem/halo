package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/three-agent-problem/halo/internal/lg"
)

// commands are the slash commands of the prompt.
var commands = [][2]string{
	{"/new", "start a new run with the rest of the line (asks first while one is on screen)"},
	{"/btw", "ask the lead something on the side of the run; no new run"},
	{"/clear", "start fresh: a new conversation with the lead"},
	{"/resume", "reopen a past run and continue its conversation"},
	{"/merge", "land the current run on your branch now"},
	{"/summary", "the run's summary: what it built, what it cost"},
	{"/connections", "connect Telegram: answer and follow runs from your phone"},
	{"/agents", "edit the team (panel 7)"},
	{"/help", "every key"},
}

func matchCommands(prefix string) [][2]string {
	var out [][2]string
	for _, c := range commands {
		if strings.HasPrefix(c[0], strings.Fields(prefix + " ")[0]) {
			out = append(out, c)
		}
	}
	return out
}

type (
	runsMsg struct {
		runs    []RunInfo
		startup bool
		err     error
	}
	historyMsg struct {
		root    string
		entries []lg.Entry
		err     error
	}
)

// picker is the /resume list.
type picker struct {
	runs    []RunInfo
	sel     int
	query   string
	loading bool
}

func (p *picker) shown() []RunInfo {
	var out []RunInfo
	for _, r := range p.runs {
		if matches(r.Title+" "+r.State+" "+RunShort(r.ID), p.query) {
			out = append(out, r)
		}
	}
	return out
}

func (m *model) fetchRuns(startup bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		rs, err := m.o.Backend.Runs(ctx)
		return runsMsg{rs, startup, err}
	}
}

// command runs a slash command typed at the prompt.
func (m *model) command(text string) tea.Cmd {
	name, rest, _ := strings.Cut(strings.TrimSpace(text), " ")
	rest = strings.TrimSpace(rest)
	switch name {
	case "/new":
		if rest == "" {
			m.say("/new needs a prompt: /new add a dark mode")
			return nil
		}
		if m.st.Root != "" {
			return m.confirmRun(rest)
		}
		return m.startRun(rest)
	case "/btw":
		if rest == "" {
			m.say("/btw needs a question: /btw why sonnet for S2?")
			return nil
		}
		return m.btw(rest)
	case "/clear":
		m.clear()
		m.say("fresh start: the next prompt opens a new conversation with the lead")
	case "/resume":
		m.picker = &picker{loading: true}
		return m.fetchRuns(false)
	case "/help":
		m.ui.Help, m.ui.Query = true, ""
	case "/summary":
		if m.st.Report == nil {
			m.say("no summary yet: the run publishes one once its plan is up")
		} else {
			m.summary = true
		}
	case "/connections":
		return m.openConnections()
	case "/agents":
		m.ui.SetFocus(pRoster)
	case "/merge":
		switch {
		case m.o.Team == nil:
			m.say("watch-only: the Halo that hosts the team merges its runs")
		case m.st.Root == "":
			m.say("no run to merge")
		default:
			short := RunShort(m.st.Root)
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
				defer cancel()
				what, err := m.o.Team.Land(ctx, short)
				return doneMsg{what, err}
			}
		}
	default:
		m.say("unknown command " + name + " · try /help")
	}
	return nil
}

// fresh returns an empty state with the same roster and who is online.
func (m *model) fresh() *State {
	old := m.st
	ms := []Member{old.Agents[old.Human].Member}
	ms = append(ms, m.members()...)
	st := NewState(old.Prefix, ms)
	for h, a := range st.Agents {
		if o := old.Agents[h]; o != nil {
			a.Online = o.Online
		}
	}
	st.Seq = old.Seq
	return st
}

// clear forgets the run on screen; the next prompt starts a new conversation.
func (m *model) clear() {
	m.st = m.fresh()
	m.o.State = m.st
	m.ui = NewUI()
	m.o.Backend.Continue("")
}

// resume loads a run's history into a fresh state, pinned to that run.
func (m *model) resume(r RunInfo) tea.Cmd {
	m.picker = nil
	m.say("loading “" + r.Title + "”…")
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		es, err := m.o.Backend.History(ctx)
		return historyMsg{r.ID, es, err}
	}
}

// replay folds a resumed run's history in at once: no packets, no playback.
func (m *model) replay(msg historyMsg) {
	if msg.err != nil {
		m.say("resume failed: " + msg.err.Error())
		return
	}
	st := m.fresh()
	st.Seq, st.Pin = 0, msg.root
	now := time.Now()
	for _, e := range msg.entries {
		st.Apply(e, now)
	}
	if st.Seq < m.st.Seq {
		st.Seq = m.st.Seq
	}
	// Later prompts start new runs in the same conversation.
	st.Pin, st.Follow = "", false
	st.Packets, st.Dots = nil, nil
	m.st, m.o.State, m.ui = st, st, NewUI()
	if m.pendingSize > sizeNormal {
		// Opened from the list beside the full-screen comms: stay there.
		m.ui.Size[pComms], m.pendingSize = m.pendingSize, sizeNormal
	}
	m.o.Backend.Continue(msg.root)
	if st.Root == "" {
		m.say("that run has no history on this server")
		return
	}
	m.say("resumed · your next prompt continues this conversation")
}

func (m *model) pickerKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.picker
	shown := p.shown()
	switch key := k.String(); key {
	case "esc":
		m.picker = nil
	case "up", "ctrl+p", "ctrl+k":
		p.sel = max(0, p.sel-1)
	case "down", "ctrl+n", "ctrl+j":
		p.sel = min(len(shown)-1, p.sel+1)
	case "enter":
		if p.sel < len(shown) {
			return m, m.resume(shown[p.sel])
		}
	case "backspace":
		if r := []rune(p.query); len(r) > 0 {
			p.query = string(r[:len(r)-1])
			p.sel = 0
		}
	default:
		if k.Text != "" {
			p.query += k.Text
			p.sel = 0
		}
	}
	return m, nil
}

var runStateKey = map[string]string{
	"working": "task", "submitted": "task", "input-required": "ask", "auth-required": "ask",
	"completed": "reply", "failed": "fail", "canceled": "fail", "rejected": "fail",
}

// pickerBox draws the /resume list.
func (m *model) pickerBox(w, h int) []string {
	p := m.picker
	q := c("hub!", "/ ") + c("hi", p.query) + cursor(m.frame)
	if p.query == "" {
		q = c("hub!", "/ ") + c("dim", "type to filter") + cursor(m.frame)
	}
	lines := []string{q, ""}
	shown := p.shown()
	switch {
	case p.loading:
		lines = append(lines, c("dim", "loading your runs…"))
	case len(shown) == 0:
		lines = append(lines, c("dim", "no runs"))
	}
	for i, r := range shown {
		if len(lines) >= h-3 {
			lines = append(lines, c("dim", fmt.Sprintf("… %d more · type to filter", len(shown)-i)))
			break
		}
		state := strings.ToLower(strings.TrimPrefix(r.State, "TASK_STATE_"))
		state = strings.ReplaceAll(state, "_", "-")
		mark := "  "
		if i == p.sel {
			mark = c("hub!", "▌ ")
		}
		meta := fmt.Sprintf("%3d tasks · %s", r.Tasks, ago(time.Since(r.Updated)))
		title := fit(firstLine(r.Title), w-4-2-7-15-len(meta)-2)
		lines = append(lines, mark+c("hub", RunShort(r.ID))+" "+c(nonEmpty(runStateKey[state], "dim")+"!", pad(state, 14))+" "+c("fg", title)+"  "+c("dim", meta))
	}
	return strings.Split(box(w, h, c("hi!", "RESUME")+c("dim", " · your runs with the lead"), c("dim", "↑↓ enter esc"), lines, true), "\n")
}

// startupHint tells you about your last run, so you can pick it up.
func (m *model) startupHint(rs []RunInfo) {
	if len(rs) == 0 || m.st.Root != "" {
		return
	}
	r := rs[0]
	state := strings.ToLower(strings.TrimPrefix(r.State, "TASK_STATE_"))
	m.st.Hint = fmt.Sprintf("last run “%s” · %s · %s · /resume to reopen it", firstLine(r.Title), state, ago(time.Since(r.Updated)))
}
