package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"strings"
	"testing"
	"time"

	"github.com/three-agent-problem/halo/internal/lg"
)

type histBackend struct {
	*Demo
	cont    string
	members []Member
}

func (h *histBackend) Continue(root string) { h.cont = root }

func (h *histBackend) Members() []Member { return h.members }

func (h *histBackend) SetMembers(_ context.Context, ms []Member) error {
	h.members = ms
	return nil
}

func (h *histBackend) Land(context.Context, string) (string, error) { return "landed", nil }

func TestResumeAndClear(t *testing.T) {
	b := &histBackend{Demo: &Demo{}}
	m := newModel(context.Background(), Options{Backend: b, State: demoState()})
	m.w, m.h = 200, 56
	old := time.Now().Add(-time.Hour)
	es := []lg.Entry{
		{Seq: 1, At: old, Kind: "task", TaskID: "r1", Sender: "kaan", Recipient: "halo-lead", State: stSubmitted, Summary: "first run"},
		{Seq: 2, At: old, Kind: "task", TaskID: "r2", Sender: "kaan", Recipient: "halo-lead", State: stSubmitted, Summary: "second run"},
		{Seq: 3, At: old, Kind: "task", TaskID: "t1", ParentTaskID: "r1", Sender: "halo-lead", Recipient: "halo-sonnet-1", State: stSubmitted, Summary: "[S1] build it"},
		{Seq: 4, At: old, Kind: "task", TaskID: "t2", ParentTaskID: "r2", Sender: "halo-lead", Recipient: "halo-opus-1", State: stSubmitted, Summary: "[S1] other"},
	}
	m.replay(historyMsg{root: "r1", entries: es})
	st := m.st
	if st.Root != "r1" || st.Prompt != "first run" || st.Tasks["t1"] == nil || st.Tasks["t2"] != nil {
		t.Fatalf("resumed: root %q prompt %q tasks %v", st.Root, st.Prompt, len(st.Tasks))
	}
	if len(st.Packets) != 0 {
		t.Fatalf("a replay draws no packets, got %d", len(st.Packets))
	}
	if b.cont != "r1" {
		t.Fatalf("prompts continue r1, got %q", b.cont)
	}
	// The live stream overlapping the replay is ignored; newer entries apply.
	st.Apply(es[2], time.Now())
	st.Apply(lg.Entry{Seq: 5, Kind: "task", TaskID: "t1", Sender: "halo-lead", Recipient: "halo-sonnet-1", State: stCompleted, At: time.Now()}, time.Now())
	if st.Tasks["t1"].State != stCompleted {
		t.Fatal("a newer entry should apply")
	}
	checkFrame(t, m.render(time.Now()), 200, 56, "resumed")

	m.input.SetValue("/clear")
	m.mode = modePrompt
	m.key(keyPress("enter"))
	if m.st.Root != "" || b.cont != "" || m.mode != modeNormal {
		t.Fatalf("/clear: root %q cont %q mode %d", m.st.Root, b.cont, m.mode)
	}
	if m.st.Seq != 5 {
		t.Fatalf("/clear keeps the stream position, seq %d", m.st.Seq)
	}
}

func keyPress(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: '\r'}
	case "esc":
		return tea.KeyPressMsg{Code: 27}
	}
	return tea.KeyPressMsg{Code: rune(k[0]), Text: k}
}

func TestPickerAndSlashMenu(t *testing.T) {
	b := &histBackend{Demo: &Demo{}}
	m := newModel(context.Background(), Options{Backend: b, State: demoState()})
	m.w, m.h = 160, 44
	m.command("/resume")
	if m.picker == nil {
		t.Fatal("/resume opens the picker")
	}
	m.Update(runsMsg{runs: []RunInfo{
		{ID: "01a1c114-847f-7ae1-9847-b927e9c1e175", Title: "add tests", State: "TASK_STATE_COMPLETED", Tasks: 30, Updated: time.Now().Add(-time.Hour)},
		{ID: "01a1c11c-5108-75e3-9e67-d32523ccde88", Title: "halo merge command", State: "TASK_STATE_WORKING", Tasks: 41, Updated: time.Now()},
	}})
	out := stripANSI(m.render(time.Now()))
	if !strings.Contains(out, "RESUME") || !strings.Contains(out, "c1e175") || !strings.Contains(out, "halo merge command") {
		t.Fatalf("picker not drawn:\n%s", out)
	}
	checkFrame(t, m.render(time.Now()), 160, 44, "picker")
	for _, r := range "merge" {
		m.key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := m.picker.shown(); len(got) != 1 || got[0].Title != "halo merge command" {
		t.Fatalf("filter: %+v", got)
	}
	m.key(keyPress("esc"))
	if m.picker != nil {
		t.Fatal("esc closes the picker")
	}
	if got := matchCommands("/re"); len(got) != 1 || got[0][0] != "/resume" {
		t.Fatalf("/re: %v", got)
	}
	if got := matchCommands("/"); len(got) != len(commands) {
		t.Fatalf("/ lists every command, got %d", len(got))
	}
}

func TestRosterEditing(t *testing.T) {
	b := &histBackend{Demo: &Demo{}}
	m := newModel(context.Background(), Options{Backend: b, State: demoState(), Team: b})
	m.w, m.h = 200, 56
	press(m, "7", "j", "j") // @haiku-1
	if cmd, _ := m.rosterKey("m"); cmd != nil {
		cmd()
	}
	if a := m.st.Agents["halo-haiku-1"]; a.Model != "sonnet" {
		t.Fatalf("m cycles haiku → sonnet, got %s", a.Model)
	}
	m.rosterKey("d")
	if cmd, _ := m.rosterKey("d"); cmd != nil {
		cmd()
	}
	if m.st.Agents["halo-haiku-1"] != nil || len(b.members) != 5 {
		t.Fatalf("d d removes the worker: %d members", len(b.members))
	}
	if cmd := m.addAgent("opus-2"); cmd != nil {
		cmd()
	}
	if a := m.st.Agents["halo-opus-2"]; a == nil || a.Model != "opus" || a.Role != "worker" {
		t.Fatalf("added: %+v", a)
	}
	if m.addAgent("Bad Name") != nil {
		t.Fatal("bad names are refused")
	}
	// Locked while a run works.
	m.st.Root, m.st.RootState = "r", stWorking
	if cmd, _ := m.rosterKey("R"); cmd != nil {
		t.Fatal("the roster is locked while a run works")
	}
	checkFrame(t, m.render(time.Now()), 200, 56, "roster")
}
