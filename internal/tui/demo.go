package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/three-agent-problem/halo/internal/lg"
	"github.com/three-agent-problem/halo/internal/plan"
)

// Demo is a Backend that plays a scripted run, shaped exactly like Lagrange's
// activity log, so the TUI can be tried (and screenshotted) without a server or
// tokens. Answering the plan approval moves it on; it approves itself after 8 s.
type Demo struct {
	Human, Prefix string

	mu      sync.Mutex
	seq     int64
	emit    func(lg.Entry)
	tasks   map[string]demoTask
	waiting *Wait
	answer  chan string
}

type demoTask struct{ parent, from, to string }

func (d *Demo) h(name string) string { return d.Prefix + "-" + name }

// Stream implements Backend.
func (d *Demo) Stream(ctx context.Context, fn func(lg.Entry), connected func(bool, error)) error {
	d.mu.Lock()
	d.emit, d.tasks, d.answer = fn, map[string]demoTask{}, make(chan string, 1)
	d.mu.Unlock()
	connected(true, nil)
	for _, n := range []string{"lead", "haiku-1", "sonnet-1", "sonnet-2", "opus-1", "reviewer"} {
		d.send(lg.Entry{Kind: "presence", Recipient: d.h(n), Data: map[string]any{"sessions": 1.0}})
	}
	for ctx.Err() == nil {
		d.run(ctx)
		sleep(ctx, 6*time.Second)
	}
	return ctx.Err()
}

// Send implements Backend: the demo ignores the prompt and plays its run.
func (d *Demo) Send(context.Context, string) (string, error) { return "", nil }

// Waiting implements Backend.
func (d *Demo) Waiting(context.Context) ([]Wait, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.waiting == nil {
		return nil, nil
	}
	return []Wait{*d.waiting}, nil
}

// Answer implements Backend.
func (d *Demo) Answer(_ context.Context, _ string, text, decision string) error {
	select {
	case d.answer <- strings.TrimSpace(decision + " " + text):
	default:
	}
	return nil
}

// Cancel implements Backend.
func (d *Demo) Cancel(context.Context, string) error { return nil }

// Online implements Backend.
func (d *Demo) Online(context.Context, []string) (int, error) { return 6, nil }

func (d *Demo) send(e lg.Entry) {
	d.mu.Lock()
	d.seq++
	e.Seq, e.At = d.seq+1180, time.Now()
	emit := d.emit
	d.mu.Unlock()
	emit(e)
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (d *Demo) task(id, parent, from, to, summary string) {
	d.mu.Lock()
	d.tasks[id] = demoTask{parent, from, to}
	d.mu.Unlock()
	d.state(id, "TASK_STATE_SUBMITTED", summary)
}

func (d *Demo) state(id, state, summary string) {
	d.mu.Lock()
	t := d.tasks[id]
	d.mu.Unlock()
	d.send(lg.Entry{Kind: "task", TaskID: id, ParentTaskID: t.parent, Sender: t.from, Recipient: t.to, State: state, Summary: summary})
}

func (d *Demo) art(id, artID, name, content string, appendIt bool) {
	d.mu.Lock()
	t := d.tasks[id]
	d.mu.Unlock()
	d.send(lg.Entry{Kind: "artifact", TaskID: id, ParentTaskID: t.parent, Sender: t.from, Recipient: t.to, Summary: name,
		Data: map[string]any{"artifactId": artID, "append": appendIt, "content": content}})
}

// work plays a worker's tool calls over about d.
func (d *Demo) work(ctx context.Context, id string, tools []string, total time.Duration) {
	step := total / time.Duration(len(tools)+1)
	for _, t := range tools {
		sleep(ctx, step+time.Duration(rand.IntN(400))*time.Millisecond)
		d.state(id, "TASK_STATE_WORKING", "Finished "+t)
		if rand.IntN(2) == 0 {
			d.art(id, id+"-r", "", "…", true)
		}
	}
}

func (d *Demo) reply(id, text string) {
	words := strings.Fields(text)
	for i := 0; i < len(words); i += 4 {
		d.art(id, id+"-r", "response", strings.Join(words[i:min(i+4, len(words))], " ")+" ", i > 0)
	}
	d.state(id, "TASK_STATE_COMPLETED", "")
}

func (d *Demo) run(ctx context.Context) {
	root := fmt.Sprintf("demo-%04x", rand.IntN(0xffff))
	lead, rev := d.h("lead"), d.h("reviewer")
	h1, s1, s2, o1 := d.h("haiku-1"), d.h("sonnet-1"), d.h("sonnet-2"), d.h("opus-1")
	p := &plan.Plan{
		Goal: "Durable tasks in Postgres behind store.Store, exposed at /api/tasks, with a web form. Tests stay green.",
		Subtasks: []*plan.Subtask{
			{ID: "S1", Title: "schema migration", Model: "haiku", Status: plan.Pending},
			{ID: "S2", Title: "pg task store", Model: "sonnet", Deps: []string{"S1"}, Status: plan.Pending},
			{ID: "S3", Title: "/api/tasks routes", Model: "sonnet", Deps: []string{"S1"}, Status: plan.Pending},
			{ID: "S4", Title: "web task form", Model: "haiku", Deps: []string{"S3"}, Status: plan.Pending},
			{ID: "S5", Title: "integrate + checks", Model: "opus", Deps: []string{"S2", "S3", "S4"}, Status: plan.Pending},
		},
	}
	publish := func() {
		raw, _ := json.Marshal(p.Public())
		d.art(root, root+"-plan", plan.ArtifactName, string(raw), false)
	}
	now := func() int64 { return time.Now().UnixMilli() }
	set := func(id, status, worker, task string) {
		st := p.Get(id)
		st.Status = status
		if worker != "" {
			st.Worker, st.Task = worker, task
		}
		if status == plan.Running && st.StartedAt == 0 {
			st.StartedAt = now()
		}
		if status == plan.Done {
			st.EndedAt = now()
		}
		publish()
	}
	dispatch := func(id, worker, text string) string {
		t := root + "-" + strings.ToLower(id) + fmt.Sprint(rand.IntN(99))
		d.task(t, root, lead, worker, plan.Tag(id)+text)
		set(id, plan.Running, worker, t)
		return t
	}
	review := func(id, verdict string, tools []string) {
		set(id, plan.Review, "", "")
		r := root + "-rv-" + strings.ToLower(id) + fmt.Sprint(rand.IntN(99))
		d.task(r, root, lead, rev, plan.Tag(id)+"review against the spec and acceptance criteria")
		d.work(ctx, r, tools, 3*time.Second)
		d.reply(r, verdict)
	}

	d.task(root, "", d.Human, lead, "add a postgres task store with /api/tasks routes, a web form and tests")
	d.work(ctx, root, []string{"Read README.md", "Glob **/*.go", "Read store/store.go", "mcp__halo__publish_plan"}, 3*time.Second)
	publish()
	sleep(ctx, time.Second)
	ask := root + "-ask"
	d.task(ask, root, lead, d.Human, "Approve this plan? 5 subtasks: S1 schema (haiku), S2 pg store (sonnet), S3 routes (sonnet), S4 web form (haiku), S5 integrate (opus).")
	d.state(ask, "TASK_STATE_INPUT_REQUIRED", "Waiting for you to answer.")
	d.mu.Lock()
	d.waiting = &Wait{TaskID: ask, Asker: lead, Question: "Approve this plan? 5 subtasks: S1 schema (haiku), S2 pg store (sonnet), S3 routes (sonnet), S4 web form (haiku), S5 integrate (opus).", Since: time.Now()}
	d.mu.Unlock()
	answer := "approve keep the API under /api"
	select {
	case answer = <-d.answer:
	case <-time.After(8 * time.Second):
	case <-ctx.Done():
		return
	}
	d.mu.Lock()
	d.waiting = nil
	d.mu.Unlock()
	if strings.HasPrefix(answer, "reject") {
		d.send(lg.Entry{Kind: "task", TaskID: ask, ParentTaskID: root, Sender: lead, Recipient: d.Human, State: "TASK_STATE_REJECTED", Summary: answer})
		d.state(root, "TASK_STATE_COMPLETED", "")
		return
	}
	d.send(lg.Entry{Kind: "task", TaskID: ask, ParentTaskID: root, Sender: lead, Recipient: d.Human, State: "TASK_STATE_COMPLETED", Summary: strings.TrimPrefix(answer, "approve ")})
	p.Approved, p.Branch = true, "halo/run-"+root[len(root)-4:]
	publish()
	d.state(root, "TASK_STATE_WORKING", "Finished mcp__halo__delegate S1")

	t1 := dispatch("S1", h1, "schema migration: tasks + task_events, with down")
	d.work(ctx, t1, []string{"Read migrations/0006_links.sql", "Write migrations/0007_tasks.sql", "Bash go run ./cmd/migrate -dry"}, 4*time.Second)
	d.reply(t1, "Migration 0007 adds tasks and task_events, with a down migration.")
	review("S1", "APPROVE. Up and down are symmetric.", []string{"Bash git diff halo/run..halo/S1", "Read migrations/0007_tasks.sql"})
	set("S1", plan.Done, "", "")

	t2 := dispatch("S2", s1, "pg task store behind store.Store, SKIP LOCKED queue")
	t3 := dispatch("S3", s2, "GET/POST /api/tasks, follow platform/api.go")
	go d.work(ctx, t2, []string{"Read store/store.go", "Grep \"type Store interface\"", "Edit store/pg.go", "Bash go test ./store/..."}, 6*time.Second)
	d.work(ctx, t3, []string{"Read platform/api.go"}, time.Second)
	q := t3 + "-q"
	set("S3", plan.Asking, "", "")
	d.task(q, t3, s2, lead, "Should the routes live under /api/v1 or /api?")
	d.state(t3, "TASK_STATE_WORKING", "Finished mcp__halo__ask_lead")
	sleep(ctx, 2500*time.Millisecond)
	d.reply(q, "/api. Match platform/api.go; kaan asked for it.")
	set("S3", plan.Running, "", "")
	d.work(ctx, t3, []string{"Edit api/tasks.go", "Bash go test ./api/..."}, 3*time.Second)
	d.reply(t2, "Store uses FOR UPDATE SKIP LOCKED for the queue. 12 tests pass.")
	review("S2", "CHANGES: tasks.context_id is filtered without an index.", []string{"Bash git diff halo/run..halo/S2", "Read store/pg.go"})
	p.Get("S2").Round = 1
	set("S2", plan.Changes, "", "")
	f2 := t2 + "-f"
	d.task(f2, root, lead, s1, plan.Tag("S2")+"reviewer: add an index on tasks.context_id, then re-run the tests")
	set("S2", plan.Running, s1, f2)
	d.reply(t3, "GET and POST /api/tasks are wired, 9 tests pass.")
	review("S3", "APPROVE. Matches the platform conventions.", []string{"Bash git diff halo/run..halo/S3", "Read api/tasks.go"})
	set("S3", plan.Done, "", "")
	d.work(ctx, f2, []string{"Edit migrations/0007_tasks.sql", "Bash go test ./store/..."}, 3*time.Second)
	d.reply(f2, "Added tasks_context_id_idx. 13 tests pass.")
	review("S2", "APPROVE. Index added.", []string{"Bash git diff halo/S2~1..halo/S2"})
	set("S2", plan.Done, "", "")

	t4 := dispatch("S4", h1, "web form for new tasks, validate the title")
	d.work(ctx, t4, []string{"Read web/src/routes.tsx", "Write web/src/TaskForm.tsx", "Bash npm test"}, 4*time.Second)
	d.art(t4, t4+"-r", "response", "npm test: 2 failed (empty title)", false)
	d.state(t4, "TASK_STATE_FAILED", "npm test: 2 failed (empty title)")
	set("S4", plan.Failed, "", "")
	sleep(ctx, time.Second)
	st4 := p.Get("S4")
	st4.Model, st4.Escalated, st4.Attempts = "sonnet", true, 2
	publish()
	t4b := dispatch("S4", s2, "web form; haiku failed on empty-title validation, see its branch")
	d.work(ctx, t4b, []string{"Read web/src/TaskForm.tsx", "Read api/tasks.go", "Edit web/src/TaskForm.tsx", "Bash npm test"}, 5*time.Second)
	d.reply(t4b, "The form validates with the API's schema. 31 tests pass.")
	review("S4", "APPROVE. Errors are accessible.", []string{"Bash git diff halo/run..halo/S4", "Read web/src/TaskForm.tsx"})
	set("S4", plan.Done, "", "")

	t5 := dispatch("S5", o1, "run every check on the run branch, fix what breaks")
	d.work(ctx, t5, []string{"Bash go vet ./...", "Bash go test ./...", "Bash npm test", "Edit go.sum"}, 6*time.Second)
	d.reply(t5, "All checks pass on "+p.Branch+": 48 Go tests, 31 web tests.")
	set("S5", plan.Done, "", "")
	d.state(root, "TASK_STATE_WORKING", "Finished mcp__halo__wait_events")
	d.reply(root, "Run complete: 5/5 merged into "+p.Branch+". 2 review rounds on S2, S4 moved up from haiku to sonnet.")
}
