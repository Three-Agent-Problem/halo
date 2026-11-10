package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/three-agent-problem/halo/internal/lg"
)

type btwBackend struct {
	*Demo
	sent, asked, root string
}

func (b *btwBackend) Send(_ context.Context, text string, _ []string) (string, error) {
	b.sent = text
	return "", nil
}

func (b *btwBackend) Btw(_ context.Context, root, text string) error {
	b.root, b.asked = root, text
	return nil
}

func btwModel(t *testing.T, state string) (*model, *btwBackend) {
	t.Helper()
	b := &btwBackend{Demo: &Demo{}}
	m := newModel(context.Background(), Options{Backend: b, State: demoState()})
	m.w, m.h = 200, 50
	m.sizeInput()
	now := time.Now()
	m.st.Apply(lg.Entry{Kind: "task", TaskID: "root-1", Sender: "ada", Recipient: "halo-lead", State: "TASK_STATE_SUBMITTED", Summary: "build it", At: now}, now)
	if state != "" {
		m.st.Apply(lg.Entry{Kind: "task", TaskID: "root-1", Sender: "ada", Recipient: "halo-lead", State: state, At: now}, now)
	}
	return m, b
}

func submitPrompt(m *model, text string) {
	m.key(tea.KeyPressMsg{Code: 'i', Text: "i"})
	typeText(m, text)
	if _, cmd := m.key(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		cmd()
	}
}

// During a run, the prompt asks the lead on the side: no new run.
func TestPromptDuringRunIsBtw(t *testing.T) {
	m, b := btwModel(t, "")
	submitPrompt(m, "why sonnet?")
	if b.sent != "" || b.asked != "why sonnet?" || b.root != "root-1" {
		t.Fatalf("sent %q, asked %q under %q", b.sent, b.asked, b.root)
	}
	if m.confirm != nil {
		t.Fatal("a btw shouldn't ask first")
	}
}

// After a run, a prompt starts a new one only after a y/n.
func TestPromptAfterRunConfirmsNewRun(t *testing.T) {
	m, b := btwModel(t, stCompleted)
	submitPrompt(m, "add dark mode")
	if m.confirm == nil || b.sent != "" {
		t.Fatalf("no y/n (confirm %v, sent %q)", m.confirm, b.sent)
	}
	if _, cmd := m.key(tea.KeyPressMsg{Code: 'y', Text: "y"}); cmd != nil {
		cmd()
	}
	if b.sent != "add dark mode" {
		t.Fatalf("sent %q after y", b.sent)
	}
}

// /new during a run asks first, then starts another run.
func TestNewCommandDuringRun(t *testing.T) {
	m, b := btwModel(t, "")
	submitPrompt(m, "/new add dark mode")
	if m.confirm == nil {
		t.Fatal("/new during a run should ask first")
	}
	if _, cmd := m.key(tea.KeyPressMsg{Code: 'n', Text: "n"}); cmd != nil {
		cmd()
	}
	if b.sent != "" {
		t.Fatal("n still started a run")
	}
}

// A btw task shows as a question and an answer in the run's comms and never
// starts a run, even with the lead as its sender.
func TestBtwEntriesStayInTheRun(t *testing.T) {
	m, _ := btwModel(t, "")
	st, now := m.st, time.Now()
	tags := map[string]any{"tags": map[string]any{"run": "root-1", BtwTag: "1"}}
	st.Apply(lg.Entry{Kind: "task", TaskID: "b1", ParentTaskID: "root-1", Sender: "halo-lead", Recipient: "halo-lead", State: "TASK_STATE_SUBMITTED", Summary: "why sonnet?", Data: tags, At: now}, now)
	st.Apply(lg.Entry{Kind: "task", TaskID: "b1", ParentTaskID: "root-1", Sender: "halo-lead", Recipient: "halo-lead", State: stCompleted, Summary: "normal feature work", Data: tags, At: now}, now)
	// The same btw without a parent (an older Lagrange) still isn't a run.
	st.Apply(lg.Entry{Kind: "task", TaskID: "b2", Sender: "ada", Recipient: "halo-lead", State: "TASK_STATE_SUBMITTED", Summary: "and S3?", Data: tags, At: now}, now)
	if st.Root != "root-1" {
		t.Fatalf("a btw started run %s", st.Root)
	}
	var got []string
	for _, c := range st.Comms {
		got = append(got, c.Kind+" "+c.From+"→"+c.To+" "+c.Text)
	}
	want := []string{"ask ada→halo-lead btw · why sonnet?", "reply halo-lead→ada btw · normal feature work", "ask ada→halo-lead btw · and S3?"}
	all := strings.Join(got, "\n")
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("comms miss %q:\n%s", w, all)
		}
	}
}

// The comms grow taller, then take the left side with the other panels
// compact beside them, then the screen with the runs beside them.
func TestCommsSizes(t *testing.T) {
	m, _ := btwModel(t, "")
	u := m.ui
	u.SetFocus(pComms)
	var sizes []Size
	for range 4 {
		u.Grow()
		sizes = append(sizes, u.Size[pComms])
	}
	if want := []Size{sizeColumn, sizeWide, sizeFull, sizeFull}; !equalSizes(sizes, want) {
		t.Fatalf("ladder %v, want %v", sizes, want)
	}
	m.runs = []RunInfo{{ID: "root-0", Title: "an older run", State: "TASK_STATE_COMPLETED", Updated: time.Now()}}
	for _, z := range []Size{sizeWide, sizeFull} {
		u.Size[pComms] = z
		out := ansi.Strip(m.View().Content)
		lines := strings.Split(out, "\n")
		if len(lines) != m.h {
			t.Fatalf("%s: %d lines, want %d", z, len(lines), m.h)
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > m.w {
				t.Fatalf("%s: line %d is %d wide", z, i, w)
			}
		}
		switch z {
		case sizeWide:
			for _, p := range []string{"COMMS", "PLAN", "AGENT", "ROSTER"} {
				if !strings.Contains(out, p) {
					t.Errorf("side: no %s panel", p)
				}
			}
		case sizeFull:
			if !strings.Contains(out, "RUNS") || !strings.Contains(out, "an older run") {
				t.Errorf("full: no runs list:\n%s", out)
			}
		}
	}
}

func equalSizes(a, b []Size) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRunsOf(t *testing.T) {
	at := time.Unix(100, 0)
	es := []lg.Entry{
		{Kind: "task", TaskID: "r1", Sender: "ada", Recipient: "lead", State: "TASK_STATE_SUBMITTED", Summary: "first", At: at},
		{Kind: "task", TaskID: "t1", ParentTaskID: "r1", Sender: "lead", Recipient: "w", At: at},
		{Kind: "task", TaskID: "t2", ParentTaskID: "t1", Sender: "w", Recipient: "lead", At: at},
		{Kind: "task", TaskID: "r1", Sender: "ada", Recipient: "lead", State: "TASK_STATE_COMPLETED", At: at},
		{Kind: "task", TaskID: "r2", Sender: "ada", Recipient: "lead", State: "TASK_STATE_WORKING", Summary: "second", At: at},
		{Kind: "task", TaskID: "b", Sender: "ada", Recipient: "lead", State: "TASK_STATE_SUBMITTED", Data: map[string]any{"tags": map[string]any{BtwTag: "1"}}, At: at},
	}
	rs := runsOf(es, "ada", "lead")
	if len(rs) != 2 || rs[0].ID != "r2" || rs[1].ID != "r1" || rs[1].Tasks != 2 || rs[1].State != "TASK_STATE_COMPLETED" || rs[1].Title != "first" {
		t.Fatalf("runs %+v", rs)
	}
}
