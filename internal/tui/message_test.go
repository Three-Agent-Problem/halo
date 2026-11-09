package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/three-agent-problem/halo/internal/plan"
)

type msgBackend struct {
	*Demo
	to, sub, text string
	direct        bool
	calls         int
}

func (b *msgBackend) Message(_ context.Context, to, sub, text string, direct bool) error {
	b.to, b.sub, b.text, b.direct = to, sub, text, direct
	b.calls++
	return nil
}

func newMsgModel() (*model, *msgBackend) {
	b := &msgBackend{Demo: &Demo{}}
	m := newModel(context.Background(), Options{Backend: b, State: demoState()})
	m.w, m.h = 160, 44
	m.sizeInput()
	m.st.Plan = &plan.Plan{Subtasks: []*plan.Subtask{{ID: "S1", Title: "x", Worker: "halo-sonnet-1"}, {ID: "S2", Title: "y"}}}
	return m, b
}

func TestMessageAgent(t *testing.T) {
	m, b := newMsgModel()
	m.ui.Focus = pAgent
	m.st.Focus = "halo-sonnet-1"
	m.st.Agents["halo-sonnet-1"].Sub = "S1"
	m.key(keyPress("m"))
	if m.mode != modeMessage || m.msg.to != "halo-sonnet-1" || m.msg.sub != "S1" || m.msg.direct {
		t.Fatalf("open: mode %v target %+v", m.mode, m.msg)
	}
	if !strings.Contains(m.input.Placeholder, "@sonnet-1") || !strings.Contains(m.input.Placeholder, "via lead") {
		t.Fatalf("placeholder %q", m.input.Placeholder)
	}
	m.key(keyPress("tab"))
	if !m.msg.direct || !strings.Contains(m.input.Placeholder, "direct") {
		t.Fatalf("tab should go direct: %+v %q", m.msg, m.input.Placeholder)
	}
	typeText(m, "use sqlite")
	_, cmd := m.key(keyPress("enter"))
	if m.mode != modeNormal || cmd == nil {
		t.Fatal("enter sends")
	}
	d, ok := cmd().(doneMsg)
	if !ok || d.err != nil || d.what != "message sent to @sonnet-1" {
		t.Fatalf("done %+v", d)
	}
	if b.to != "halo-sonnet-1" || b.sub != "S1" || b.text != "use sqlite" || !b.direct {
		t.Fatalf("backend got %+v", b)
	}
}

func TestMessageEscAndTargets(t *testing.T) {
	m, b := newMsgModel()
	m.ui.Focus = pOrbit
	m.st.Focus = m.st.Lead
	m.key(keyPress("m"))
	if m.mode != modeMessage || m.msg.to != m.st.Lead {
		t.Fatalf("orbit: %+v", m.msg)
	}
	m.key(keyPress("tab"))
	if m.msg.direct {
		t.Fatal("the lead has no direct route")
	}
	typeText(m, "hi")
	m.key(keyPress("esc"))
	if m.mode != modeNormal || m.input.Value() != "" || b.calls != 0 {
		t.Fatal("esc cancels")
	}
	// plan: the selected subtask's worker, else the lead
	m.ui.Focus = pPlan
	m.key(keyPress("m"))
	if m.msg.to != "halo-sonnet-1" || m.msg.sub != "S1" {
		t.Fatalf("plan: %+v", m.msg)
	}
	m.key(keyPress("esc"))
	m.ui.Cur[pPlan].sel = 1
	m.key(keyPress("m"))
	if m.msg.to != m.st.Lead || m.msg.sub != "S2" {
		t.Fatalf("unassigned subtask goes to the lead: %+v", m.msg)
	}
	m.key(keyPress("esc"))
	// no target: a status error, no input
	m.ui.Focus, m.st.Focus = pAgent, m.st.Human
	m.key(keyPress("m"))
	if m.mode != modeNormal || m.flash == "" {
		t.Fatalf("want a status error, mode %v flash %q", m.mode, m.flash)
	}
}

func TestMessageTabPopup(t *testing.T) {
	m, _ := newMsgModel()
	m.ui.Focus, m.st.Focus = pAgent, "halo-sonnet-1"
	m.key(keyPress("m"))
	typeText(m, "ask @haiku")
	if _, cs := m.popup(); len(cs) == 0 {
		t.Fatal("popup should be open")
	}
	m.key(keyPress("tab"))
	if m.msg.direct || !strings.Contains(m.input.Value(), "@haiku-1") {
		t.Fatalf("tab with the popup open accepts it: direct=%v %q", m.msg.direct, m.input.Value())
	}
	if _, cs := m.popup(); len(cs) != 0 {
		t.Fatal("popup should be closed after accepting")
	}
	m.key(keyPress("tab"))
	if !m.msg.direct {
		t.Fatal("tab with the popup closed toggles direct")
	}
}
