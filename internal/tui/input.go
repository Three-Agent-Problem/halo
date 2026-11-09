package tui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	inputRows     = 4 // the input grows to this many rows
	inputRowsWide = 8 // ... and to this many when the comms pane is focused
)

// newInput builds the text input every typing mode shares (the prompt, naming
// an agent, a note, later a message): one logical line that wraps and grows.
func newInput() textarea.Model {
	in := textarea.New()
	in.Prompt = ""
	in.ShowLineNumbers = false
	in.Placeholder = promptHint
	in.CharLimit = 2000
	in.DynamicHeight = true
	in.MinHeight = 1
	in.MaxHeight = inputRows
	in.EndOfBufferCharacter = ' '
	// Enter sends; the value stays one line, so no key inserts a newline.
	in.KeyMap.InsertNewline = key.NewBinding(key.WithDisabled())
	st := textarea.DefaultDarkStyles()
	for _, s := range []*textarea.StyleState{&st.Focused, &st.Blurred} {
		s.CursorLine = s.Base
		s.Text = s.Base
		s.Prompt = s.Base
	}
	in.SetStyles(st)
	in.SetWidth(60)
	return in
}

// sizeInput fits the input to the window: full width, and the rows it may grow
// to (more while the comms pane, where you read what you answer, has focus).
func (m *model) sizeInput() {
	m.input.SetWidth(max(20, m.w-5))
	rows := inputRows
	if m.ui.Focus == pComms {
		rows = inputRowsWide
	}
	m.input.MaxHeight = max(1, min(rows, (m.h-12)/2))
	m.input.SetValue(m.input.Value()) // re-flow the height
	m.input.MoveToEnd()
}

// startInput opens the input with placeholder; it returns the focus command.
func (m *model) startInput(placeholder string) tea.Cmd {
	m.input.Placeholder = placeholder
	m.popSel, m.popOff = 0, false
	m.sizeInput()
	return tea.Batch(m.input.Focus(), m.wantFiles())
}

// clearInput empties and releases the input.
func (m *model) clearInput() {
	m.input.SetValue("")
	m.input.Blur()
	m.input.Placeholder = promptHint
	m.popSel, m.popOff = 0, false
}

// setInput replaces the text and puts the cursor at its end.
func (m *model) setInput(s string) {
	m.input.SetValue(s)
	m.input.MoveToEnd()
	m.popSel, m.popOff = 0, false
}

// cleanPaste makes pasted text fit the single-line input.
func cleanPaste(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s)
	return strings.TrimSpace(s)
}

// inputText is true in the modes that type text to send.
func (m *model) inputText() bool {
	return m.mode == modePrompt || m.mode == modeNote || m.mode == modeName || m.mode == modeMessage
}

// completes is true in the modes that type to agents, where @ # and paths
// complete.
func (m *model) completes() bool {
	return m.mode == modePrompt || m.mode == modeNote || m.mode == modeMessage
}

// cursorRune is the cursor's offset in the (single-line) value, in runes.
func (m *model) cursorRune() int {
	n := 0
	for i, l := range strings.Split(m.input.Value(), "\n") {
		if i == m.input.Line() {
			return n + m.input.Column()
		}
		n += len([]rune(l)) + 1
	}
	return n
}

// popup is the open completion list: its token and candidates, none when closed.
func (m *model) popup() (Token, []Candidate) {
	if !m.completes() || m.popOff || m.picker != nil {
		return Token{}, nil
	}
	v := m.input.Value()
	if strings.HasPrefix(v, "/") {
		if tok, ok := FindToken(v, m.cursorRune()); ok && tok.Start == 0 {
			return Token{}, nil // a /command, completed by tab
		}
	}
	return Complete(v, m.cursorRune(), m.sources())
}

// popupKey moves, accepts or closes the completion list. It reports whether it
// used the key.
func (m *model) popupKey(k string) bool {
	tok, cs := m.popup()
	if len(cs) == 0 {
		return false
	}
	m.popSel = min(m.popSel, len(cs)-1)
	switch k {
	case "up", "ctrl+p":
		m.popSel = (m.popSel + len(cs) - 1) % len(cs)
	case "down", "ctrl+n":
		m.popSel = (m.popSel + 1) % len(cs)
	case "tab", "enter":
		v, cur := Accept(m.input.Value(), tok, cs[m.popSel])
		m.input.SetValue(v)
		m.input.MoveToEnd()
		m.input.SetCursorColumn(cur)
		m.popSel = 0
	case "esc":
		m.popOff = true
	default:
		return false
	}
	return true
}

// typed passes a key to the input; typing reopens the completion list.
func (m *model) typed(k tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	before := m.input.Value()
	m.input, cmd = m.input.Update(k)
	if m.input.Value() != before {
		m.popSel, m.popOff = 0, false
	}
	return cmd
}

// paste inserts pasted text into the input, newlines folded into spaces.
func (m *model) paste(text string) {
	if t := cleanPaste(text); t != "" {
		m.input.InsertString(t)
		m.popSel, m.popOff = 0, false
	}
}

type filesMsg struct {
	root  string
	files []string
}

// filer is a Backend that can list the repository's files.
type filer interface {
	Files(ctx context.Context) ([]string, error)
}

// Files implements filer.
func (l *Live) Files(ctx context.Context) ([]string, error) { return GitFiles(ctx, l.Repo) }

// wantFiles loads the repository's file list once per run.
func (m *model) wantFiles() tea.Cmd {
	f, ok := m.o.Backend.(filer)
	if !ok || m.filesFor == m.st.Root && m.filesLoaded {
		return nil
	}
	m.filesFor, m.filesLoaded = m.st.Root, true
	root := m.st.Root
	return func() tea.Msg {
		files, _ := f.Files(m.ctx)
		return filesMsg{root, files}
	}
}

// inputView is the input's rows, prompt mark first, then the completion popup.
func (m *model) inputView() []string {
	rows := strings.Split(m.input.View(), "\n")
	for i := range rows {
		mark := "  "
		if i == 0 {
			mark = c("hub!", "› ")
		}
		rows[i] = " " + mark + rows[i]
	}
	return rows
}

// popupBox draws the completion list.
func (m *model) popupBox() []string {
	tok, cs := m.popup()
	if len(cs) == 0 {
		return nil
	}
	sel := min(m.popSel, len(cs)-1)
	w := 24
	for _, c := range cs {
		w = max(w, ansi.StringWidth(c.Text)+ansi.StringWidth(c.Desc)+6)
	}
	w = min(w, max(24, m.w-4))
	var lines []string
	for i, cd := range cs {
		l := " " + cd.Text
		if cd.Desc != "" {
			l += "  " + c("dim", cd.Desc)
		}
		if i == sel {
			l = c("hub!", "▸") + c("hi!", cd.Text)
			if cd.Desc != "" {
				l += "  " + c("mute", cd.Desc)
			}
		} else {
			l = " " + c("fg", cd.Text)
			if cd.Desc != "" {
				l += "  " + c("dim", cd.Desc)
			}
		}
		lines = append(lines, l)
	}
	title := map[byte]string{'@': "AGENTS", '#': "SUBTASKS", 'p': "FILES"}[tok.Kind]
	return strings.Split(box(w, len(lines)+2, c("hub!", title), "", lines, true), "\n")
}

func (m *model) popupCands() []Candidate { _, cs := m.popup(); return cs }
