package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func timeNow() time.Time { return time.Now() }

func (m *model) wantFilesMsg(b *sendBackend) tea.Msg { return filesMsg{m.st.Root, b.files} }

func TestPaste(t *testing.T) {
	m, _ := newTestModel()
	m.Update(tea.PasteMsg{Content: "first line\r\nsecond\tline\n\nthird\n"})
	if got := m.input.Value(); got != "first line second line  third" {
		t.Fatalf("paste: %q", got)
	}
	typeText(m, "!")
	if got := m.input.Value(); !strings.HasSuffix(got, "third!") {
		t.Fatalf("typing after paste: %q", got)
	}
	// pasting in normal mode does nothing, in the name mode it lands in the input
	m.clearInput()
	m.mode = modeName
	m.Update(tea.PasteMsg{Content: "sonnet-3"})
	if m.input.Value() != "sonnet-3" {
		t.Fatalf("name paste: %q", m.input.Value())
	}
	m.clearInput()
	m.mode = modeNormal
	m.Update(tea.PasteMsg{Content: "x"})
	if m.input.Value() != "" {
		t.Fatal("paste outside a text mode must not fill the input")
	}
}

func TestInputWraps(t *testing.T) {
	m, _ := newTestModel()
	m.ui.Focus = pAgent
	m.sizeInput()
	if m.input.Height() != 1 {
		t.Fatalf("starts at one row, got %d", m.input.Height())
	}
	base := len(strings.Split(m.render(timeNow()), "\n"))
	m.Update(tea.PasteMsg{Content: strings.Repeat("word ", 80)}) // 400 cols in a 155-col input
	if h := m.input.Height(); h != 3 && h != 4 {
		t.Fatalf("wrapped height %d", h)
	}
	m.Update(tea.PasteMsg{Content: strings.Repeat("more ", 200)})
	if h := m.input.Height(); h != inputRows {
		t.Fatalf("grows to the cap, got %d", h)
	}
	out := m.render(timeNow())
	checkFrame(t, out, 160, 44, "wrapped input")
	if n := len(strings.Split(out, "\n")); n != 44 || base != 44 {
		t.Fatalf("frame height %d/%d", n, base)
	}
	// with the comms pane focused the cap is higher
	m.ui.Focus = pComms
	m.sizeInput()
	m.Update(tea.PasteMsg{Content: strings.Repeat("most ", 300)})
	if h := m.input.Height(); h <= inputRows {
		t.Fatalf("comms focus allows more rows, got %d", h)
	}
	checkFrame(t, m.render(timeNow()), 160, 44, "tall input")
	// enter still sends, esc cancels
	m.key(keyPress("esc"))
	if m.mode != modeNormal {
		t.Fatal("esc cancels")
	}
}
