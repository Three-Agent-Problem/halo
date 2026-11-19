package tui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/three-agent-problem/halo/internal/plan"
)

type resumeRec struct {
	*Demo
	modes []string
}

func (r *resumeRec) Resume(_ context.Context, mode string) error {
	r.modes = append(r.modes, mode)
	return nil
}

func TestPausedPlan(t *testing.T) {
	m := boardModel()
	now := time.Now()
	p := *m.st.Plan
	p.Subtasks = append([]*plan.Subtask(nil), p.Subtasks...)
	paused := *p.Subtasks[1]
	paused.Status = plan.Paused
	p.Subtasks[1] = &paused
	resets := time.Date(2026, 11, 19, 19, 50, 0, 0, time.Local)
	p.Paused = &plan.Pause{Reason: "You've hit your 5-hour limit · resets 7:50pm (Europe/Rome)", Resets: resets.UnixMilli()}
	m.st.setPlan(&p, now)
	last := m.st.Comms[len(m.st.Comms)-1]
	if !strings.HasPrefix(last.Text, "⏸ You've hit your 5-hour limit") || !strings.Contains(last.Text, "/continue") {
		t.Fatalf("hub line %q", last.Text)
	}
	if got := m.st.summary(pPlan, Screen{Now: now}); !strings.Contains(got, "⏸ paused (usage limit, resets 19:50)") {
		t.Fatalf("summary %q", got)
	}
	if b := m.st.buckets(); !slices.Contains(b[2], 1) {
		t.Fatalf("a paused subtask waits: %v", b)
	}

	q := p
	q.Paused = nil
	m.st.setPlan(&q, now)
	if last := m.st.Comms[len(m.st.Comms)-1]; !strings.Contains(last.Text, "continuing after the usage limit") {
		t.Fatalf("hub line %q", last.Text)
	}
}

func TestContinueCommand(t *testing.T) {
	rec := &resumeRec{Demo: &Demo{}}
	m := boardModel()
	m.o.Backend = rec
	for _, c := range []string{"/continue", "/continue wait", "/continue STOP"} {
		if cmd := m.command(c); cmd != nil {
			cmd()
		}
	}
	if strings.Join(rec.modes, " ") != "now wait stop" {
		t.Fatalf("modes %v", rec.modes)
	}
	if m.command("/continue later") != nil || !strings.Contains(m.flash, "/continue wait") {
		t.Fatalf("flash %q", m.flash)
	}
	// Watch-only: there is no claude here to log in.
	m.o.Team = nil
	if m.command("/login") != nil || !strings.Contains(m.flash, "watch-only") {
		t.Fatalf("flash %q", m.flash)
	}
}
