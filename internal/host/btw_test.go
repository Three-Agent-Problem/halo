package host

import (
	"context"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

func btwExec(id, text string) *a2asrv.ExecutorContext {
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text))
	return &a2asrv.ExecutorContext{TaskID: a2a.TaskID(id), ContextID: "ctx-btw", Message: msg}
}

// A btw stays open until the lead answers it and completes with the answer; the
// plan doesn't change.
func TestBtwWaitsForTheLeadsAnswer(t *testing.T) {
	l, r, _ := humanLead(t)
	before := r.plan.Subtasks[1].Status
	type result struct {
		state a2a.TaskState
		text  string
	}
	done := make(chan result, 1)
	go func() {
		var last result
		for ev := range l.btw(context.Background(), btwExec("btw-1", "why sonnet for S2?"), meta{Kind: "btw", Run: "task-1", From: "kaan"}) {
			if u, ok := ev.(*a2a.TaskStatusUpdateEvent); ok {
				last = result{u.Status.State, textOf(u.Status.Message)}
			}
		}
		done <- last
	}()
	var e event
	select {
	case e = <-r.events:
	case <-time.After(2 * time.Second):
		t.Fatal("no btw event")
	}
	if e.Kind != "btw" || e.From != "@kaan" || e.Text != "why sonnet for S2?" || e.ID == "" {
		t.Fatalf("event %+v", e)
	}
	if !r.reply(e.ID, "It's normal feature work.") {
		t.Fatal("the btw isn't answerable with answer")
	}
	select {
	case got := <-done:
		if got.state != a2a.TaskStateCompleted || got.text != "It's normal feature work." {
			t.Fatalf("got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the btw task didn't complete")
	}
	if r.plan.Subtasks[1].Status != before {
		t.Fatalf("a btw changed S2's status to %s", r.plan.Subtasks[1].Status)
	}
}

func TestBtwWithoutARunFails(t *testing.T) {
	l, _, _ := humanLead(t)
	delete(l.runs, "task-1")
	var state a2a.TaskState
	for ev := range l.btw(context.Background(), btwExec("btw-2", "hi"), meta{Kind: "btw"}) {
		if u, ok := ev.(*a2a.TaskStatusUpdateEvent); ok {
			state = u.Status.State
		}
	}
	if state != a2a.TaskStateFailed {
		t.Fatalf("state %s, want failed", state)
	}
}
