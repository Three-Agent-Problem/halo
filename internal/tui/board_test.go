package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/three-agent-problem/halo/internal/plan"
)

func boardModel() *model {
	m := newModel(context.Background(), Options{Backend: &Demo{}, State: demoState()})
	m.w, m.h = 200, 56
	m.st.RootAt = time.Now().Add(-time.Minute)
	m.st.Plan = &plan.Plan{Goal: "g", Approved: true, Subtasks: []*plan.Subtask{
		{ID: "S1", Title: "one", Model: "haiku", Status: plan.Done},
		{ID: "S2", Title: "two", Model: "sonnet", Status: plan.Running, Worker: "halo-sonnet-1"},
		{ID: "S3", Title: "three", Model: "sonnet", Status: plan.Running},
		{ID: "S4", Title: "four", Model: "haiku", Status: plan.Pending, Deps: []string{"S2"}},
		{ID: "S5", Title: "five", Model: "opus", Status: plan.Review},
	}}
	return m
}

func TestBoardKeys(t *testing.T) {
	m := boardModel()
	press(m, "1", "+", "+") // wide: columns side by side
	cur := m.ui.Cur[pPlan]
	sel := func() string { return m.st.Plan.Subtasks[cur.sel].ID }
	cur.sel = 1 // S2, RUNNING
	press(m, "j")
	if sel() != "S3" {
		t.Fatalf("j moves down the RUNNING column, at %s", sel())
	}
	press(m, "j")
	if sel() != "S3" {
		t.Fatalf("j stops at the column's end, at %s", sel())
	}
	press(m, "l")
	if sel() != "S5" {
		t.Fatalf("l moves to WAITING, at %s", sel())
	}
	press(m, "l")
	if sel() != "S1" {
		t.Fatalf("l moves to DONE, at %s", sel())
	}
	press(m, "h", "h", "h")
	if sel() != "S4" {
		t.Fatalf("h h h back to PENDING, at %s", sel())
	}
	out := stripANSI(m.render(time.Now()))
	for _, want := range []string{"PENDING 1", "RUNNING 2", "WAITING 1", "DONE 1", "⧗ after S2", "S4 four"} {
		if !strings.Contains(out, want) {
			t.Errorf("board lacks %q:\n%s", want, out)
		}
	}

	// Column size stacks the columns; j walks through all of them.
	press(m, "-")
	cur.sel = 3 // S4, the only PENDING one
	press(m, "j")
	if sel() != "S2" {
		t.Fatalf("stacked: j from PENDING goes on to RUNNING, at %s", sel())
	}
	press(m, "enter")
	if !cur.detail {
		t.Fatal("enter opens the subtask")
	}
}

func TestTimeline(t *testing.T) {
	m := boardModel()
	now := time.Now()
	m.st.Tasks["t2"] = &Task{ID: "t2", Sender: m.st.Lead, Recipient: "halo-sonnet-1", Sub: "S2", Kind: "task", Started: now.Add(-40 * time.Second)}
	m.st.Tasks["q"] = &Task{ID: "q", Sender: "halo-sonnet-1", Recipient: m.st.Lead, Kind: "ask", Started: now.Add(-20 * time.Second), Ended: now.Add(-10 * time.Second)}
	press(m, "1", "+", "+", "+")
	out := stripANSI(m.render(now))
	for _, want := range []string{"@sonnet-1", "S2███", "???", "NOW"} {
		if !strings.Contains(out, want) {
			t.Errorf("timeline lacks %q:\n%s", want, out)
		}
	}
}

type stopRec struct {
	*Demo
	stopped []string
}

func (s *stopRec) StopSubtask(_ context.Context, sub string) error {
	s.stopped = append(s.stopped, sub)
	return nil
}

func TestStopWorker(t *testing.T) {
	rec := &stopRec{Demo: &Demo{}}
	m := boardModel()
	m.o.Backend = rec
	press(m, "1")
	m.ui.Cur[pPlan].sel = 0 // S1 is done: x falls back to canceling the run
	press(m, "x")
	if m.confirm != nil {
		t.Fatal("a finished subtask can't be stopped")
	}
	m.cancelArm = time.Time{}
	m.ui.Cur[pPlan].sel = 1 // S2 runs
	press(m, "x")
	if m.confirm == nil {
		t.Fatal("x on a running subtask asks first")
	}
	if out := stripANSI(m.render(time.Now())); !strings.Contains(out, "Stop S2 two (sonnet @sonnet-1)?") {
		t.Fatalf("dialog:\n%s", out)
	}
	if _, cmd := m.key(tea.KeyPressMsg{Code: 'n', Text: "n"}); cmd != nil || m.confirm != nil {
		t.Fatal("n closes it and does nothing")
	}
	press(m, "x")
	_, cmd := m.key(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("y stops")
	}
	cmd()
	if len(rec.stopped) != 1 || rec.stopped[0] != "S2" {
		t.Fatalf("stopped %v", rec.stopped)
	}
}
