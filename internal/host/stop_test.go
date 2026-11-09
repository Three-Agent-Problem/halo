package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/three-agent-problem/halo/internal/plan"
)

func TestStopSubtask(t *testing.T) {
	l, r, _ := humanLead(t)
	r.plan.Get("S2").Task = "t-s2"
	var canceled []string
	l.cancel = func(_ context.Context, worker, task string) error {
		canceled = append(canceled, worker+" "+task)
		return nil
	}
	ack, err := l.stop(context.Background(), meta{Kind: "stop_subtask", Subtask: "s2", From: "kaan"})
	if err != nil {
		t.Fatal(err)
	}
	if len(canceled) != 1 || canceled[0] != "w-sonnet t-s2" || !strings.Contains(ack, "Stopping @w-sonnet on S2") {
		t.Fatalf("canceled %v, ack %q", canceled, ack)
	}
	noEvent(t, r) // the lead hears it when the task ends

	// The canceled task ends: the subtask fails as stopped, and the lead is told
	// not to retry on its own.
	r.workEnded("S2", "w-sonnet", a2a.TaskStateCanceled, "")
	if s := r.plan.Get("S2"); s.Status != plan.Failed || s.Note != "stopped by @kaan" {
		t.Fatalf("S2 %s %q", s.Status, s.Note)
	}
	e := nextEvent(t, r)
	if e.Kind != "subtask_failed" || !strings.Contains(e.Text, "on purpose") || !strings.Contains(e.Hint, "ask_human") {
		t.Fatalf("event %+v", e)
	}
	// The next failure of S2 is an ordinary one again.
	r.workEnded("S2", "w-sonnet", a2a.TaskStateFailed, "boom")
	if e := nextEvent(t, r); strings.Contains(e.Text, "on purpose") || e.Hint != "escalate, or follow_up with guidance" {
		t.Fatalf("second failure %+v", e)
	}
}

func TestStopSubtaskRefuses(t *testing.T) {
	l, r, _ := humanLead(t)
	l.cancel = func(context.Context, string, string) error { return errors.New("lagrange said no") }
	for _, tc := range []struct{ sub, want string }{
		{"", "which subtask"},
		{"S9", "no subtask S9"},
		{"S3", "isn't delegated"},
		{"S1", "isn't running (it is ready)"},
	} {
		r.plan.Get("S1").Task = "t-s1"
		if _, err := l.stop(context.Background(), meta{Subtask: tc.sub}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.sub, err, tc.want)
		}
	}
	r.plan.Get("S2").Task = "t-s2"
	if _, err := l.stop(context.Background(), meta{Subtask: "S2"}); err == nil {
		t.Fatal("a failed cancel is an error")
	}
	if r.subs["S2"].stoppedBy != "" {
		t.Fatal("a failed cancel doesn't mark the subtask stopped")
	}
}
