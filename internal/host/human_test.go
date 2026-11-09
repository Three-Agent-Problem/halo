package host

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/halo/internal/config"
	"github.com/three-agent-problem/halo/internal/plan"
)

type relayed struct {
	run      *run
	id, text string
}

// humanLead is a lead with one active run of an approved plan, whose follow-ups
// are recorded instead of sent. S1 is with @w-opus and ready, S2 is with @w-sonnet
// and running, S3 isn't delegated.
func humanLead(t *testing.T) (*lead, *run, *[]relayed) {
	t.Helper()
	h := &Host{cfg: &config.Config{}, st: &config.State{Human: "kaan"}, ctx: context.Background(),
		log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	l := &lead{h: h, handle: "w-lead", runs: map[string]*run{}, byCtx: map[string]*run{}, ctxOf: map[string]string{}}
	var got []relayed
	l.relay = func(_ context.Context, r *run, id, text string) error {
		got = append(got, relayed{r, id, text})
		return nil
	}
	h.lead = l
	r := addRun(l, "task-1")
	r.plan = &plan.Plan{Goal: "g", Approved: true, Subtasks: []*plan.Subtask{
		{ID: "S1", Status: plan.Ready, StartedAt: 10},
		{ID: "S2", Status: plan.Running, StartedAt: 20},
		{ID: "S3", Status: plan.Pending},
	}}
	r.rt("S1").worker = "w-opus"
	r.rt("S2").worker = "w-sonnet"
	return l, r, &got
}

// addRun adds an active run (the lead's turn attached) to l.
func addRun(l *lead, id string) *run {
	l.nruns++
	r := &run{l: l, id: id, short: shortID(id), seq: l.nruns, events: make(chan event, 16), questions: map[string]*question{},
		ec: &a2asrv.ExecutorContext{TaskID: a2a.TaskID(id)}}
	l.runs[id] = r
	return r
}

func nextEvent(t *testing.T, r *run) event {
	t.Helper()
	select {
	case e := <-r.events:
		return e
	default:
		t.Fatal("no event")
		return event{}
	}
}

func noEvent(t *testing.T, r *run) {
	t.Helper()
	select {
	case e := <-r.events:
		t.Fatalf("unexpected event %+v", e)
	default:
	}
}

func TestHumanMessageDirectFollowsUp(t *testing.T) {
	l, r, got := humanLead(t)
	ack, err := l.route(context.Background(), meta{Kind: "human_message", To: "@w-opus", Subtask: "s1", Direct: true, From: "kaan"}, "  use the v2 API  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0].run != r || (*got)[0].id != "S1" || (*got)[0].text != "Message from @kaan: use the v2 API" {
		t.Fatalf("relayed %+v", *got)
	}
	if !strings.Contains(ack, "@w-opus") {
		t.Errorf("ack %q", ack)
	}
	e := nextEvent(t, r)
	if e.Kind != "human_message" || e.Subtask != "S1" || e.From != "@kaan" || e.To != "@w-opus" || e.Text != "use the v2 API" ||
		!strings.Contains(e.Hint, "Nothing to relay") {
		t.Errorf("event %+v", e)
	}
}

func TestHumanMessageDirectCurrentSubtask(t *testing.T) {
	l, _, got := humanLead(t)
	// No subtask: the worker's current one.
	if _, err := l.route(context.Background(), meta{To: "w-opus", Direct: true}, "hi"); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0].id != "S1" {
		t.Fatalf("relayed %+v", *got)
	}
}

func TestHumanMessageDirectBusyGoesToLead(t *testing.T) {
	l, r, got := humanLead(t)
	ack, err := l.route(context.Background(), meta{To: "w-sonnet", Subtask: "S2", Direct: true}, "stop")
	if err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Fatalf("relayed to a busy worker: %+v", *got)
	}
	if !strings.Contains(ack, "busy") {
		t.Errorf("ack %q", ack)
	}
	if e := nextEvent(t, r); e.Kind != "human_message" || e.Subtask != "S2" || !strings.Contains(e.Hint, "follow_up") {
		t.Errorf("event %+v", e)
	}
}

func TestHumanMessageViaLead(t *testing.T) {
	l, r, got := humanLead(t)
	if _, err := l.route(context.Background(), meta{To: "w-opus", Subtask: "S1"}, "also add docs"); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Fatalf("relayed directly: %+v", *got)
	}
	e := nextEvent(t, r)
	want := event{Kind: "human_message", Subtask: "S1", From: "@kaan", To: "@w-opus", Text: "also add docs",
		Hint: "relay it to the worker with follow_up (or act on it)"}
	if e.Kind != want.Kind || e.Subtask != want.Subtask || e.From != want.From || e.To != want.To || e.Text != want.Text || e.Hint != want.Hint {
		t.Errorf("event %+v, want %+v", e, want)
	}

	// To the lead itself, with or without a subtask, direct or not: always an event.
	for _, m := range []meta{{To: "w-lead"}, {}, {To: "@w-lead", Subtask: "S3", Direct: true}} {
		if _, err := l.route(context.Background(), m, "note"); err != nil {
			t.Fatalf("%+v: %v", m, err)
		}
		if e := nextEvent(t, r); e.Kind != "human_message" || e.To != "" || e.Subtask != m.Subtask {
			t.Errorf("%+v: event %+v", m, e)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("relayed: %+v", *got)
	}
}

func TestHumanMessageLatestRun(t *testing.T) {
	l, old, _ := humanLead(t)
	newer := addRun(l, "task-2")
	if _, err := l.route(context.Background(), meta{To: "w-lead"}, "hello"); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, newer)
	noEvent(t, old)
	// A subtask only the older run has still finds it.
	if _, err := l.route(context.Background(), meta{Subtask: "S1"}, "hello"); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, old)
}

func TestHumanMessageErrors(t *testing.T) {
	l, r, got := humanLead(t)
	cases := []struct {
		m    meta
		text string
		want string
	}{
		{meta{To: "w-opus", Subtask: "S9", Direct: true}, "x", "no subtask S9"},
		{meta{Subtask: "S9"}, "x", "no subtask S9"},
		{meta{To: "w-opus", Subtask: "S3", Direct: true}, "x", "isn't delegated yet"},
		{meta{To: "w-opus", Subtask: "S3"}, "x", "isn't delegated yet"},
		{meta{To: "w-haiku", Subtask: "S1", Direct: true}, "x", "S1 is with @w-opus"},
		{meta{To: "w-haiku", Direct: true}, "x", "@w-haiku has no subtask"},
		{meta{To: "w-opus", Direct: true}, "   ", "empty"},
	}
	for _, c := range cases {
		if _, err := l.route(context.Background(), c.m, c.text); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: err %v, want %q", c.m, err, c.want)
		}
	}
	noEvent(t, r)

	// No active run: detached or merged runs don't count.
	r.mu.Lock()
	r.ec = nil
	r.mu.Unlock()
	if _, err := l.route(context.Background(), meta{To: "w-lead"}, "x"); err == nil || !strings.Contains(err.Error(), "no active run") {
		t.Errorf("detached: %v", err)
	}
	r.mu.Lock()
	r.ec, r.landing = &a2asrv.ExecutorContext{}, landMerged
	r.mu.Unlock()
	if _, err := l.route(context.Background(), meta{To: "w-opus", Subtask: "S1", Direct: true}, "x"); err == nil || !strings.Contains(err.Error(), "no active run") {
		t.Errorf("merged: %v", err)
	}
	if len(*got) != 0 {
		t.Fatalf("relayed: %+v", *got)
	}
}

// The A2A task completes at once with the ack, or fails with the reason; it never
// starts a run.
func TestHumanMessageExecute(t *testing.T) {
	l, _, _ := humanLead(t)
	exec := func(m meta) *a2a.TaskStatusUpdateEvent {
		msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello"))
		msg.Metadata = m.asMap()
		ec := &a2asrv.ExecutorContext{TaskID: "t", ContextID: "c", Message: msg}
		var last *a2a.TaskStatusUpdateEvent
		for ev, err := range l.Execute(context.Background(), ec) {
			if err != nil {
				t.Fatal(err)
			}
			last = ev.(*a2a.TaskStatusUpdateEvent)
		}
		return last
	}
	ev := exec(meta{Kind: "human_message", To: "w-opus", Subtask: "S1", Direct: true})
	if ev.Status.State != a2a.TaskStateCompleted || !strings.Contains(textOf(ev.Status.Message), "Sent to @w-opus") {
		t.Errorf("ok: %s %q", ev.Status.State, textOf(ev.Status.Message))
	}
	ev = exec(meta{Kind: "human_message", Subtask: "S9"})
	if ev.Status.State != a2a.TaskStateFailed || !strings.Contains(textOf(ev.Status.Message), "no subtask S9") {
		t.Errorf("fail: %s %q", ev.Status.State, textOf(ev.Status.Message))
	}
	if len(l.runs) != 1 {
		t.Errorf("a human message started a run: %d runs", len(l.runs))
	}
}

func TestMetaRoundTrip(t *testing.T) {
	m := meta{Kind: "human_message", To: "w-opus", Subtask: "S1", Direct: true, From: "kaan"}
	msg := a2a.NewMessage(a2a.MessageRoleUser)
	msg.Metadata = m.asMap()
	if got := metaOf(msg); got != m {
		t.Errorf("got %+v, want %+v", got, m)
	}
}
