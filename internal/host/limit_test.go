package host

import (
	"context"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/halo/internal/plan"
)

// realLimit is what a worker's task said when the 5-hour limit hit (claude-agent-acp
// 0.101.1 through a2aacp).
const realLimit = `acp agent error: Internal error: You've hit your 5-hour limit · resets 7:50pm (Europe/Rome) (code -32603): {"errorKind":"rate_limit"}`

func TestUsageLimit(t *testing.T) {
	rome, _ := time.LoadLocation("Europe/Rome")
	now := time.Date(2026, 11, 19, 17, 10, 0, 0, rome)
	hit, ok := usageLimit(realLimit, now)
	if !ok || hit.Text != "You've hit your 5-hour limit · resets 7:50pm (Europe/Rome)" {
		t.Fatalf("%v %+v", ok, hit)
	}
	if want := time.Date(2026, 11, 19, 19, 50, 0, 0, rome); !hit.Resets.Equal(want) {
		t.Fatalf("resets %v, want %v", hit.Resets, want)
	}

	for _, tc := range []struct {
		text string
		want time.Time
	}{
		// Past the time today: tomorrow.
		{"You've hit your 5-hour limit · resets 3am (Europe/Rome)", time.Date(2026, 11, 20, 3, 0, 0, 0, rome)},
		{"You've hit your weekly limit · resets Nov 21, 8am (Europe/Rome)", time.Date(2026, 11, 21, 8, 0, 0, 0, rome)},
		{"You've hit your weekly limit · resets Nov 21 at 8:30am (Europe/Rome)", time.Date(2026, 11, 21, 8, 30, 0, 0, rome)},
		// After New Year.
		{"You've reached your Opus limit · resets Jan 2, 9am (Europe/Rome)", time.Date(2027, 1, 2, 9, 0, 0, 0, rome)},
		{"You're out of extra usage", time.Time{}},
	} {
		hit, ok := usageLimit("acp agent error: Internal error: "+tc.text+" (code -32603)", now)
		if !ok || !hit.Resets.Equal(tc.want) {
			t.Errorf("%q: %v, resets %v, want %v", tc.text, ok, hit.Resets, tc.want)
		}
	}

	// A plain rate limit Claude Code gave up retrying: a short wait.
	if hit, ok := usageLimit(`acp agent error: Internal error: API Error: 429 (code -32603): {"errorKind":"rate_limit"}`, now); !ok || hit.Resets != now.Add(rateWait) {
		t.Fatalf("rate limit: %v %+v", ok, hit)
	}
	for _, text := range []string{"acp agent error: boom", "the agent reached its token limit", ""} {
		if _, ok := usageLimit(text, now); ok {
			t.Errorf("%q counted as a usage limit", text)
		}
	}
}

func TestLimitChoice(t *testing.T) {
	for text, want := range map[string]string{
		"Limit: Continue now":                     resumeNow,
		"Limit: Wait for the reset (Recommended)": resumeWait,
		"Limit: Stop the run":                     resumeStop,
		"Limit: I switched accounts, go":          resumeNow,
		"Limit: (no answer, your call)":           resumeWait,
	} {
		if got := limitChoice(a2a.TaskStateCompleted, text); got != want {
			t.Errorf("%q: %s, want %s", text, got, want)
		}
	}
	if got := limitChoice(a2a.TaskStateCanceled, "Limit: Stop the run"); got != resumeWait {
		t.Errorf("canceled question: %s", got)
	}
}

// fakeExec is an executor whose turns end with what fn returns.
type fakeExec struct {
	name     string
	mu       sync.Mutex
	prompts  []string
	canceled []a2a.TaskID
	shutdown bool
	fn       func(n int, ec *a2asrv.ExecutorContext) a2a.Event
}

func (f *fakeExec) Execute(_ context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		f.mu.Lock()
		f.prompts = append(f.prompts, textOf(ec.Message))
		n := len(f.prompts)
		f.mu.Unlock()
		if !yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateWorking, nil), nil) {
			return
		}
		yield(f.fn(n, ec), nil)
	}
}

func (f *fakeExec) Cancel(_ context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	f.mu.Lock()
	f.canceled = append(f.canceled, ec.TaskID)
	f.mu.Unlock()
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCanceled, nil), nil)
	}
}

func (f *fakeExec) Shutdown(context.Context) error {
	f.mu.Lock()
	f.shutdown = true
	f.mu.Unlock()
	return nil
}

func (f *fakeExec) Close() error { return nil }

func failedWith(ec *a2asrv.ExecutorContext, text string) a2a.Event {
	return a2a.NewStatusUpdateEvent(ec, a2a.TaskStateFailed, a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(text)))
}

func TestProcsRestart(t *testing.T) {
	var made []*fakeExec
	p, err := newProcs(func() (agentExec, error) {
		f := &fakeExec{name: "e", fn: func(_ int, ec *a2asrv.ExecutorContext) a2a.Event {
			return a2a.NewStatusUpdateEvent(ec, a2a.TaskStateInputRequired, nil)
		}}
		made = append(made, f)
		return f, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ec := &a2asrv.ExecutorContext{TaskID: "t1", Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))}
	for range p.Execute(context.Background(), ec) {
	}
	// t1 paused for input on the first executor: after a restart, its answer and
	// its cancel still go there, and new tasks go to the new one.
	if err := p.Restart(); err != nil {
		t.Fatal(err)
	}
	for range p.Execute(context.Background(), ec) {
	}
	ec2 := &a2asrv.ExecutorContext{TaskID: "t2", Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("new"))}
	for range p.Execute(context.Background(), ec2) {
	}
	for range p.Cancel(context.Background(), ec) {
	}
	if len(made) != 2 || len(made[0].prompts) != 2 || len(made[1].prompts) != 1 || made[1].prompts[0] != "new" {
		t.Fatalf("prompts %v / %v", made[0].prompts, made[1].prompts)
	}
	if len(made[0].canceled) != 1 || len(made[1].canceled) != 0 {
		t.Fatalf("canceled %v / %v", made[0].canceled, made[1].canceled)
	}
	time.Sleep(10 * time.Millisecond)
	made[0].mu.Lock()
	defer made[0].mu.Unlock()
	if !made[0].shutdown {
		t.Fatal("the old executor wasn't shut down")
	}
}

// TestLeadPausesOnLimit: the lead's turn and a worker hit the usage limit; the run
// pauses instead of failing, and when the person continues, the agents restart,
// the worker continues in its session and the lead in its own.
func TestLeadPausesOnLimit(t *testing.T) {
	l, r, relayed := humanLead(t)
	h := l.h
	asked := make(chan limitHit, 1)
	h.limitAsk = func(_ context.Context, _ *run, hit limitHit) (string, error) {
		asked <- hit
		return "", nil
	}
	var execs []*fakeExec
	var mu sync.Mutex
	l.exec, _ = newProcs(func() (agentExec, error) {
		f := &fakeExec{fn: func(n int, ec *a2asrv.ExecutorContext) a2a.Event {
			mu.Lock()
			first := len(execs) == 1
			mu.Unlock()
			if first {
				return failedWith(ec, realLimit)
			}
			return a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, nil)
		}}
		mu.Lock()
		execs = append(execs, f)
		mu.Unlock()
		return f, nil
	})

	// S2's worker hits the limit first: paused, not failed, and the lead hears nothing.
	r.workEnded("S2", "w-sonnet", a2a.TaskStateFailed, realLimit)
	if s := r.plan.Get("S2"); s.Status != plan.Paused {
		t.Fatalf("S2 %s", s.Status)
	}
	noEvent(t, r)
	if r.plan.Paused == nil || !strings.Contains(r.plan.Paused.Reason, "5-hour limit") {
		t.Fatalf("plan pause %+v", r.plan.Paused)
	}
	select {
	case hit := <-asked:
		if !strings.Contains(hit.Text, "5-hour") {
			t.Fatalf("asked about %+v", hit)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the person wasn't asked")
	}

	// The lead's turn hits it too: the root task stays working.
	ec := &a2asrv.ExecutorContext{TaskID: "task-1", ContextID: "c1", Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("build it"))}
	l.runs["task-1"] = r
	evs := make(chan a2a.Event, 64)
	go func() {
		defer close(evs)
		for ev := range l.Execute(context.Background(), ec) {
			evs <- ev
		}
	}()
	var paused bool
	for !paused {
		select {
		case ev := <-evs:
			if s, ok := ev.(*a2a.TaskStatusUpdateEvent); ok {
				if s.Status.State.Terminal() {
					t.Fatalf("the run ended: %s", s.Status.State)
				}
				paused = s.Status.Message != nil && strings.HasPrefix(textOf(s.Status.Message), "⏸ Paused")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the lead didn't pause")
		}
	}

	// The person logged in to another account and continues now.
	if !h.decideLimit(resumeNow) {
		t.Fatal("no pause to decide")
	}
	var end a2a.TaskState
	for ev := range evs {
		if s, ok := ev.(*a2a.TaskStatusUpdateEvent); ok && s.Status.State.Terminal() {
			end = s.Status.State
		}
	}
	if end != a2a.TaskStateCompleted {
		t.Fatalf("the run ended %s", end)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(execs) != 2 {
		t.Fatalf("%d executors: the lead wasn't restarted", len(execs))
	}
	if p := execs[1].prompts; len(p) != 1 || !strings.Contains(p[0], "continues now") || !strings.Contains(p[0], "S2") {
		t.Fatalf("the lead's next prompt %q", p)
	}
	if len(*relayed) != 1 || (*relayed)[0].id != "S2" || (*relayed)[0].text != resumeWorker {
		t.Fatalf("relayed %+v", *relayed)
	}
	if r.plan.Paused != nil || h.limit.on {
		t.Fatal("still paused")
	}
}

// TestPauseStop: stopping fails what paused, as before the pause existed.
func TestPauseStop(t *testing.T) {
	l, r, relayed := humanLead(t)
	l.h.limitAsk = func(context.Context, *run, limitHit) (string, error) { return "", nil }
	r.workEnded("S2", "w-sonnet", a2a.TaskStateFailed, realLimit)
	l.h.decideLimit(resumeStop)
	status := func() string {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.plan.Get("S2").Status
	}
	deadline := time.Now().Add(2 * time.Second)
	for status() == plan.Paused && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s := status(); s != plan.Failed {
		t.Fatalf("S2 %s", s)
	}
	if e := nextEvent(t, r); e.Kind != "subtask_failed" || !strings.Contains(e.Text, "usage limit") {
		t.Fatalf("event %+v", e)
	}
	if len(*relayed) != 0 {
		t.Fatalf("relayed %+v", *relayed)
	}
}
