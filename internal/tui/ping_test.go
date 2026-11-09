package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestPingOnNewWait(t *testing.T) {
	m := newModel(context.Background(), Options{Backend: &Demo{}, State: demoState()})
	m.w, m.h = 160, 44
	old := Wait{TaskID: "w0", Asker: m.st.Lead, Question: "old", Since: time.Now()}
	if _, cmd := m.Update(waitsMsg{old}); cmd != nil {
		t.Fatal("what waits at start doesn't ping")
	}
	m.Update(tea.FocusMsg{})
	fresh := Wait{TaskID: "w1", Asker: m.st.Lead, Question: "Approve this plan?\nS1 …", Since: time.Now()}
	if _, cmd := m.Update(waitsMsg{old, fresh}); cmd != nil {
		t.Fatal("no ping while you look at Halo")
	}
	m.Update(tea.BlurMsg{})
	again := Wait{TaskID: "w2", Asker: m.st.Lead, Question: "Which store?", Since: time.Now()}
	_, cmd := m.Update(waitsMsg{old, fresh, again})
	if cmd == nil {
		t.Fatal("a new wait pings while you're away")
	}
	if _, cmd := m.Update(waitsMsg{old, fresh, again}); cmd != nil {
		t.Fatal("each wait pings once")
	}
	m.o.Quiet = true
	if _, cmd := m.Update(waitsMsg{{TaskID: "w3", Asker: m.st.Lead, Question: "x"}}); cmd != nil {
		t.Fatal("notify: false never pings")
	}
}

func TestNotifySeq(t *testing.T) {
	got := notifySeq("@lead asks you: hi\x1b]evil\x07 " + strings.Repeat("x", 200))
	if !strings.HasPrefix(got, "\x1b]9;halo: @lead asks you: hi ]evil  x") || !strings.HasSuffix(got, "…\x07\a") {
		t.Fatalf("%q", got)
	}
	// One ESC opens the OSC; two BELs: its terminator and the bell.
	if strings.Count(got, "\x1b") != 1 || strings.Count(got, "\a") != 2 {
		t.Fatalf("control characters leak: %q", got)
	}
}
