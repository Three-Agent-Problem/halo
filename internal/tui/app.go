package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/three-agent-problem/halo/internal/lg"
)

// Options configure the TUI.
type Options struct {
	Backend Backend
	State   *State
	Server  string
	Handles []string // the roster's agent handles, for the online count
}

type mode int

const (
	modeNormal mode = iota
	modePrompt
	modeAnswer
	modeNote
)

type (
	entryMsg  lg.Entry
	tickMsg   time.Time
	connMsg   struct{ ok bool }
	waitsMsg  []Wait
	onlineMsg int
	doneMsg   struct {
		what string
		err  error
	}
	pollMsg struct{}
)

type model struct {
	o         Options
	st        *State
	w, h      int
	frame     int
	connected bool
	online    int
	waits     []Wait
	mode      mode
	input     textinput.Model
	decision  string
	flash     string
	flashAt   time.Time
	cancelArm time.Time
	ctx       context.Context
}

// Run starts the TUI and blocks until it quits.
func Run(ctx context.Context, o Options) error {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "describe what to build, then enter"
	in.CharLimit = 2000
	m := &model{o: o, st: o.State, input: in, ctx: ctx}
	p := tea.NewProgram(m, tea.WithContext(ctx))
	go func() {
		_ = o.Backend.Stream(ctx, func(e lg.Entry) { p.Send(entryMsg(e)) }, func(ok bool, _ error) { p.Send(connMsg{ok}) })
	}()
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
	return tea.Batch(
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
			n, err := m.o.Backend.Online(ctx, m.o.Handles)
			if err != nil {
				return nil
			}
			return onlineMsg(n)
		})
}

func (m *model) Init() tea.Cmd { return tea.Batch(tick(), m.fetch(), m.poll()) }

func (m *model) say(s string) { m.flash, m.flashAt = s, time.Now() }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.input.SetWidth(max(20, m.w-70))
	case tickMsg:
		m.frame++
		m.st.Prune(time.Time(msg))
		if m.flash != "" && time.Since(m.flashAt) > 5*time.Second {
			m.flash = ""
		}
		return m, tick()
	case pollMsg:
		return m, tea.Batch(m.fetch(), m.poll())
	case entryMsg:
		m.st.Apply(lg.Entry(msg), time.Now())
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
		m.online = int(msg)
	case doneMsg:
		if msg.err != nil {
			m.say(msg.what + " failed: " + msg.err.Error())
		} else if msg.what != "" {
			m.say(msg.what)
		}
		return m, m.fetch()
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modePrompt:
		switch key {
		case "esc":
			m.mode = modeNormal
			m.input.Blur()
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.input.SetValue("")
			m.input.Blur()
			m.mode = modeNormal
			m.st.Follow = true
			return m, func() tea.Msg {
				_, err := m.o.Backend.Send(m.ctx, text)
				return doneMsg{"prompt sent to " + m.st.Short(m.st.Lead), err}
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return m, cmd
	case modeAnswer:
		switch key {
		case "esc", "q":
			m.mode = modeNormal
		case "y":
			m.decision, m.mode = "approve", modeNote
			m.input.Placeholder = "optional note, then enter to approve"
			return m, m.input.Focus()
		case "n":
			m.decision, m.mode = "reject", modeNote
			m.input.Placeholder = "why? then enter to reject"
			return m, m.input.Focus()
		case "r":
			m.decision, m.mode = "", modeNote
			m.input.Placeholder = "your answer, then enter"
			return m, m.input.Focus()
		}
		return m, nil
	case modeNote:
		switch key {
		case "esc":
			m.mode = modeAnswer
			m.input.SetValue("")
			m.input.Blur()
			m.input.Placeholder = "describe what to build, then enter"
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
			m.input.SetValue("")
			m.input.Blur()
			m.input.Placeholder = "describe what to build, then enter"
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
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return m, cmd
	}
	switch key {
	case "q":
		return m, tea.Quit
	case "enter", "i", "/":
		m.mode = modePrompt
		return m, m.input.Focus()
	case "a":
		if len(m.waits) > 0 {
			m.mode = modeAnswer
		} else {
			m.say("nothing waits on you")
		}
	case "tab":
		m.st.CycleFocus(time.Now())
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

func (m *model) View() tea.View {
	if m.w == 0 {
		v := tea.NewView("")
		v.AltScreen = true
		return v
	}
	sc := Screen{W: m.w, H: m.h, Frame: m.frame, Now: time.Now(), Server: m.o.Server, Connected: m.connected,
		Online: m.online, Waiting: len(m.waits), Flash: m.flash}
	sc.Footer = m.footer()
	if (m.mode == modeAnswer || m.mode == modeNote) && len(m.waits) > 0 {
		sc.Bottom, sc.BottomTag = m.answerPane(), fmt.Sprintf("%s asks you", m.st.Short(m.waits[0].Asker))
	}
	v := tea.NewView(m.st.Render(sc))
	v.AltScreen = true
	v.WindowTitle = "halo"
	return v
}

func (m *model) footer() string {
	alert := ""
	if len(m.waits) > 0 && m.mode == modeNormal && (m.frame/8)%2 == 0 {
		alert = styles["alert"].Render(" A ▸ ANSWER ") + " "
	} else if len(m.waits) > 0 && m.mode == modeNormal {
		alert = c("ask!", " A ▸ ANSWER ") + " "
	}
	var keys string
	switch m.mode {
	case modePrompt:
		keys = c("mute!", "⏎") + c("dim", " send  ") + c("mute!", "esc") + c("dim", " back")
	case modeAnswer:
		keys = c("mute!", "y") + c("dim", " approve  ") + c("mute!", "n") + c("dim", " reject  ") + c("mute!", "r") + c("dim", " reply  ") + c("mute!", "esc") + c("dim", " later")
	case modeNote:
		keys = c("mute!", "⏎") + c("dim", " send  ") + c("mute!", "esc") + c("dim", " back")
	default:
		keys = c("mute!", "⏎") + c("dim", " prompt  ") + c("mute!", "a") + c("dim", " answer  ") + c("mute!", "tab") + c("dim", " focus  ") +
			c("mute!", "x") + c("dim", " cancel  ") + c("mute!", "q") + c("dim", " quit")
	}
	var prompt string
	switch m.mode {
	case modePrompt, modeNote:
		prompt = c("hub!", "› ") + m.input.View()
	default:
		text := m.st.Prompt
		if text == "" {
			text = "press enter to describe what to build"
		}
		prompt = c("hub!", "› ") + c("mute", text)
	}
	return " " + alert + prompt + "  " + keys
}

func (m *model) answerPane() string {
	w := m.waits[0]
	width := max(20, m.w-8)
	var lines []string
	for _, l := range strings.Split(wrap(w.Question, width), "\n") {
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

func wrap(s string, w int) string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			if len(line)+len(word)+1 > w && line != "" {
				out = append(out, line)
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
