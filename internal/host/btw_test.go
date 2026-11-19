package host

import (
	"context"
	"iter"
	"path/filepath"
	"strings"
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

// talkExec answers every prompt with one message, as Claude would.
type talkExec struct {
	fakeExec
	l    *lead
	keys []string
}

func (f *talkExec) Execute(_ context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		f.mu.Lock()
		f.prompts = append(f.prompts, textOf(ec.Message))
		f.keys = append(f.keys, f.l.sess.key(ec.ContextID))
		f.mu.Unlock()
		art := &a2a.TaskArtifactUpdateEvent{TaskID: ec.TaskID, ContextID: ec.ContextID,
			Artifact: &a2a.Artifact{ID: "a1", Parts: a2a.ContentParts{a2a.NewTextPart("S2 failed its tests, ")}}}
		more := &a2a.TaskArtifactUpdateEvent{TaskID: ec.TaskID, ContextID: ec.ContextID, Append: true,
			Artifact: &a2a.Artifact{ID: "a1", Parts: a2a.ContentParts{a2a.NewTextPart("so I escalated it.")}}}
		thought := &a2a.TaskArtifactUpdateEvent{TaskID: ec.TaskID, ContextID: ec.ContextID,
			Artifact: &a2a.Artifact{ID: "t1", Name: "thoughts", Parts: a2a.ContentParts{a2a.NewTextPart("hmm")}}}
		for _, ev := range []a2a.Event{a2a.NewStatusUpdateEvent(ec, a2a.TaskStateWorking, nil), thought, art, more,
			a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, nil)} {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// A btw about a run that is over is a Claude turn of the lead in the run's own
// session: it answers, and no run starts.
func TestBtwAboutAFinishedRun(t *testing.T) {
	l, r, _ := humanLead(t)
	r.ec = nil // the run's turn ended
	l.ctxOf["task-1"] = "ctx-run"
	l.sess = &leadSessions{f: openSessions(filepath.Join(t.TempDir(), "s.json"))}
	f := &talkExec{l: l}
	l.exec, _ = newProcs(func() (agentExec, error) { return f, nil })

	var last *a2a.TaskStatusUpdateEvent
	for ev := range l.btw(context.Background(), btwExec("btw-2", "why did S2 move to opus?"), meta{Kind: "btw", Run: "task-1", From: "kaan"}) {
		if u, ok := ev.(*a2a.TaskStatusUpdateEvent); ok {
			last = u
		}
	}
	if last == nil || last.Status.State != a2a.TaskStateCompleted || textOf(last.Status.Message) != "S2 failed its tests, so I escalated it." {
		t.Fatalf("answer %+v", last)
	}
	if len(f.prompts) != 1 || !strings.Contains(f.prompts[0], "why did S2 move to opus?") || !strings.Contains(f.prompts[0], "no new run") {
		t.Fatalf("prompts %q", f.prompts)
	}
	if f.keys[0] != "lead ctx-run" {
		t.Fatalf("the talk ran in session %q, not the run's", f.keys[0])
	}
	if l.sess.key("ctx-btw") != "lead ctx-btw" {
		t.Fatal("the alias outlived the talk")
	}
	if len(l.runs) != 1 {
		t.Fatal("a talk started a run")
	}
	noEvent(t, r)

	// No run at all: the person is told to send a prompt.
	for ev := range l.btw(context.Background(), btwExec("btw-3", "hi"), meta{Kind: "btw", From: "kaan"}) {
		last, _ = ev.(*a2a.TaskStatusUpdateEvent)
	}
	if last == nil || last.Status.State != a2a.TaskStateFailed {
		t.Fatalf("no run: %+v", last)
	}
}
