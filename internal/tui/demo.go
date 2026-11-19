package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/three-agent-problem/halo/internal/lg"
	"github.com/three-agent-problem/halo/internal/plan"
	"github.com/three-agent-problem/lagrange/ask"
	"github.com/three-agent-problem/lagrange/report"
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
	commits []Commit
	diffs   map[string]string
	tips    map[string]string  // branch → its newest fake commit
	spent   map[string]float64 // task → its fake session's cost
	ctxUsed map[string]int64   // task → its fake context fill
	feed    chan Update
	shells  []Shell
	outs    map[string]string
	pid     int
	msgs    int        // fake human messages sent
	history []lg.Entry // what was emitted, for Runs and History
	doc     *plan.Doc  // the run's plan doc
}

// PlanDoc implements Backend.
func (d *Demo) PlanDoc(context.Context, string) (*plan.Doc, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.doc, nil
}

// demoHistory caps the entries the demo keeps for Runs and History.
const demoHistory = 20000

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
func (d *Demo) Send(context.Context, string, []string) (string, error) { return "", nil }

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
func (d *Demo) Online(_ context.Context, handles []string) ([]string, error) { return handles, nil }

// Runs implements Backend: the demo keeps no history.
func (d *Demo) Runs(context.Context) ([]RunInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return runsOf(d.history, d.Human, d.h("lead")), nil
}

// History implements Backend.
func (d *Demo) History(context.Context) ([]lg.Entry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]lg.Entry(nil), d.history...), nil
}

// runsOf lists the runs in an activity log, newest first: the person's root
// tasks to the lead, with their latest state and how many tasks they grew.
func runsOf(es []lg.Entry, human, lead string) []RunInfo {
	byID := map[string]*RunInfo{}
	var order []string
	parent := map[string]string{}
	for _, e := range es {
		if e.Kind != "task" || e.TaskID == "" {
			continue
		}
		if e.ParentTaskID == "" && e.Sender == human && e.Recipient == lead && !isBtw(e) {
			r := byID[e.TaskID]
			if r == nil {
				r = &RunInfo{ID: e.TaskID, Title: e.Summary, Started: e.At}
				byID[e.TaskID] = r
				order = append(order, e.TaskID)
			}
			r.State, r.Updated = e.State, e.At
			continue
		}
		if _, ok := parent[e.TaskID]; !ok && e.ParentTaskID != "" {
			parent[e.TaskID] = e.ParentTaskID
			root := e.ParentTaskID
			for parent[root] != "" {
				root = parent[root]
			}
			if r := byID[root]; r != nil {
				r.Tasks++
				r.Updated = e.At
			}
		}
	}
	out := make([]RunInfo, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		out = append(out, *byID[order[i]])
	}
	return out
}

// Continue implements Backend.
func (d *Demo) Continue(string) {}

// Message implements Backend: the demo has no agents to reach, so it shows the
// message as a fake human_message task to the lead that completes at once.
func (d *Demo) Message(_ context.Context, to, subtask, text string, direct bool) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("the message is empty")
	}
	d.mu.Lock()
	ready := d.emit != nil
	d.msgs++
	id := fmt.Sprintf("demo-msg-%d", d.msgs)
	d.mu.Unlock()
	if !ready {
		return nil
	}
	to = strings.TrimPrefix(to, "@")
	if to == "" {
		to = d.h("lead")
	}
	how := "via the lead"
	if direct {
		how = "direct"
	}
	d.task(id, "", d.Human, d.h("lead"), strings.TrimSpace(fmt.Sprintf("message to @%s %s (%s): %s", to, subtask, how, text)))
	d.state(id, "TASK_STATE_COMPLETED", "Sent.")
	return nil
}

// Btw implements Backend: a fake btw task that the lead answers a few seconds
// later.
func (d *Demo) Btw(ctx context.Context, root, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("the question is empty")
	}
	d.mu.Lock()
	ready := d.emit != nil
	d.msgs++
	id := fmt.Sprintf("demo-btw-%d", d.msgs)
	if ready {
		d.tasks[id] = demoTask{root, d.Human, d.h("lead")}
	}
	d.mu.Unlock()
	if !ready {
		return nil
	}
	tags := map[string]any{"tags": map[string]any{"run": root, BtwTag: "1"}}
	// Off the caller's goroutine, like a real server's stream.
	go func() {
		d.send(lg.Entry{Kind: "task", TaskID: id, ParentTaskID: root, Sender: d.Human, Recipient: d.h("lead"), State: "TASK_STATE_SUBMITTED", Summary: text, Data: tags})
		sleep(context.WithoutCancel(ctx), 3*time.Second)
		d.send(lg.Entry{Kind: "task", TaskID: id, ParentTaskID: root, Sender: d.Human, Recipient: d.h("lead"), State: "TASK_STATE_COMPLETED",
			Summary: "(demo) The lead answers asides between its steps, without touching the plan. A real lead would answer “" + firstLine(text) + "” here.",
			Data:    tags})
	}()
	return nil
}

// Resume implements Backend: the demo never hits a usage limit.
func (d *Demo) Resume(context.Context, string) error {
	return errors.New("nothing is paused (the demo never hits a usage limit)")
}

// StopSubtask implements Backend: the scripted run goes on, but the request shows.
func (d *Demo) StopSubtask(_ context.Context, subtask string) error {
	d.mu.Lock()
	d.msgs++
	id := fmt.Sprintf("demo-stop-%d", d.msgs)
	d.mu.Unlock()
	d.task(id, "", d.Human, d.h("lead"), "stop "+subtask)
	d.state(id, "TASK_STATE_COMPLETED", "Stopping "+subtask+" (the demo's script goes on).")
	return nil
}

// Commits implements Backend: the commits the scripted run made.
func (d *Demo) Commits(context.Context, string, time.Time) ([]Commit, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Commit(nil), d.commits...), nil
}

// Diff implements Backend.
func (d *Demo) Diff(_ context.Context, sha, _ string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.diffs[sha], nil
}

// commit records a fake commit with a plausible diff of files.
func (d *Demo) commit(subject string, files ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.diffs == nil {
		d.diffs = map[string]string{}
	}
	sha := fmt.Sprintf("%016x%016x%08x", rand.Uint64(), rand.Uint64(), rand.Uint32())
	c := Commit{SHA: sha, At: time.Now(), Author: "halo", Subject: subject, Files: len(files)}
	if m := subRe.FindStringSubmatch(subject); m != nil {
		c.Sub = m[1]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "commit %s\nAuthor: halo\nDate:   just now\n\n    %s\n\n", sha, subject)
	for _, f := range files {
		add, del := 4+rand.IntN(30), rand.IntN(6)
		c.Add, c.Del = c.Add+add, c.Del+del
		fmt.Fprintf(&b, " %s | %d %s\n", f, add+del, strings.Repeat("+", min(add, 20))+strings.Repeat("-", del))
	}
	fmt.Fprintf(&b, " %d files changed, %d insertions(+), %d deletions(-)\n", len(files), c.Add, c.Del)
	for _, f := range files {
		fmt.Fprintf(&b, "\ndiff --git a/%s b/%s\nindex 3f2a1c0..9b8e7d4 100644\n--- a/%s\n+++ b/%s\n@@ -12,7 +12,14 @@ package main\n", f, f, f, f)
		b.WriteString(" // existing code stays as it was\n-\treturn nil\n+\tif err := validate(in); err != nil {\n+\t\treturn fmt.Errorf(\"invalid input: %w\", err)\n+\t}\n+\treturn store.Save(ctx, in)\n }\n")
	}
	c.SHA = sha
	// Branches like a real run: each subtask on its own branch off the run
	// branch, merged back into it; the run landing on main.
	if d.tips == nil {
		d.tips = map[string]string{"run": "base", "main": "base"}
	}
	tip := func(b string) string { return nonEmpty(d.tips[b], d.tips["run"]) }
	switch {
	case strings.HasPrefix(subject, "halo: merge halo/"):
		c.Parents, d.tips["main"] = []string{d.tips["main"], d.tips["run"]}, sha
	case strings.HasPrefix(subject, "halo: merge "):
		c.Parents, d.tips["run"] = []string{d.tips["run"], tip(c.Sub)}, sha
		delete(d.tips, c.Sub)
	default:
		c.Parents, d.tips[c.Sub] = []string{tip(c.Sub)}, sha
	}
	d.commits = append(d.commits, c)
	d.diffs[sha] = b.String()
}

func (d *Demo) send(e lg.Entry) {
	d.mu.Lock()
	d.seq++
	e.Seq, e.At = d.seq+1180, time.Now()
	d.history = append(d.history, e)
	if len(d.history) > demoHistory {
		d.history = append([]lg.Entry(nil), d.history[len(d.history)-demoHistory/2:]...)
	}
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

// report publishes a situation report on task id, as the relay records it.
func (d *Demo) report(id string, r report.Report) {
	d.mu.Lock()
	t := d.tasks[id]
	d.mu.Unlock()
	r.At = time.Now()
	d.send(lg.Entry{Kind: "artifact", TaskID: id, ParentTaskID: t.parent, Sender: t.from, Recipient: t.to, Summary: "report: " + r.Headline,
		Data: map[string]any{"artifactId": report.ArtifactName, "report": r.Map()}})
}

// work plays a worker's tool calls over about d.
func (d *Demo) work(ctx context.Context, id string, tools []string, total time.Duration) {
	step := total / time.Duration(len(tools)+1)
	d.mu.Lock()
	agent := d.tasks[id].to
	d.mu.Unlock()
	for _, t := range tools {
		sleep(ctx, step/3)
		d.tool(ctx, agent, id, t, step*2/3+time.Duration(rand.IntN(400))*time.Millisecond)
		d.state(id, "TASK_STATE_WORKING", "Finished "+t)
		d.spend(agent, id)
		if rand.IntN(2) == 0 {
			d.art(id, id+"-r", "", "…", true)
		}
	}
}

// spend reports a usage update for the agent's session on task id, priced
// roughly like its model.
func (d *Demo) spend(agent, id string) {
	price := map[string]float64{"haiku": .004, "sonnet": .03, "opus": .12}
	step := .03
	for m, p := range price {
		if strings.Contains(agent, m) {
			step = p
		}
	}
	if agent == d.h("lead") {
		step = price["opus"]
	}
	d.mu.Lock()
	if d.spent == nil {
		d.spent, d.ctxUsed = map[string]float64{}, map[string]int64{}
	}
	d.spent[id] += step * (.6 + rand.Float64())
	d.ctxUsed[id] += 6000 + rand.Int64N(9000)
	cost, used := d.spent[id], d.ctxUsed[id]
	d.mu.Unlock()
	d.raw(agent, id, map[string]any{"sessionUpdate": "usage_update", "used": used, "size": 200000,
		"cost": map[string]any{"amount": cost, "currency": "USD"}})
}

func (d *Demo) reply(id, text string) {
	d.mu.Lock()
	agent := d.tasks[id].to
	d.mu.Unlock()
	d.chunk(agent, id, "agent_message_chunk", text)
	words := strings.Fields(text)
	for i := 0; i < len(words); i += 4 {
		d.art(id, id+"-r", "response", strings.Join(words[i:min(i+4, len(words))], " ")+" ", i > 0)
	}
	d.state(id, "TASK_STATE_COMPLETED", "")
}

// noteBody is the notes without their first line, on one line.
func noteBody(notes string) string {
	_, body, ok := strings.Cut(notes, "\n")
	if !ok {
		body = notes
	}
	return strings.Join(strings.Fields(body), " ")
}

// demoBlocks are the visuals the demo's lead shows with its plan.
func demoBlocks() []plan.Block {
	return []plan.Block{
		{Kind: plan.KindArchitecture, Title: "task store",
			Groups: []plan.Group{{ID: "web", Label: "web"}, {ID: "api", Label: "api"}, {ID: "store", Label: "store"}, {ID: "db", Label: "postgres"}},
			Nodes: []plan.Node{
				{ID: "form", Label: "TaskForm.tsx", Group: "web", Change: plan.New, Subtask: "S4"},
				{ID: "routes", Label: "routes.tsx", Group: "web", Change: plan.Changed, Subtask: "S4"},
				{ID: "tasks", Label: "/api/tasks", Group: "api", Change: plan.New, Subtask: "S3"},
				{ID: "platform", Label: "platform/api.go", Group: "api"},
				{ID: "pg", Label: "pg task store", Group: "store", Change: plan.New, Subtask: "S2"},
				{ID: "iface", Label: "store.Store", Group: "store"},
				{ID: "mem", Label: "memory store", Group: "store"},
				{ID: "tables", Label: "tasks, task_events", Group: "db", Change: plan.New, Subtask: "S1"},
				{ID: "links", Label: "links (0006)", Group: "db"},
			},
			Edges: []plan.Edge{
				{From: "form", To: "tasks", Label: "POST", Change: plan.New},
				{From: "routes", To: "platform"},
				{From: "tasks", To: "pg", Change: plan.New},
				{From: "platform", To: "iface"},
				{From: "pg", To: "tables", Change: plan.New},
				{From: "iface", To: "links"},
				{From: "pg", To: "iface", Label: "implements", Change: plan.New},
			}},
		{Kind: plan.KindFlow, Title: "create a task",
			Steps: []plan.Step{
				{From: "browser", To: "api", Text: "POST /api/tasks {title}", Change: plan.New},
				{From: "api", Text: "validate the title", Change: plan.New},
				{From: "api", To: "store", Text: "Create(ctx, task)", Change: plan.New},
				{From: "store", To: "postgres", Text: "INSERT … RETURNING id", Change: plan.New},
				{From: "api", To: "browser", Text: "201 + the task", Change: plan.New},
			}},
		{Kind: plan.KindFiles, Title: "files",
			Paths: []plan.Path{
				{Path: "migrations/0007_tasks.sql", Change: plan.New, Subtask: "S1"},
				{Path: "migrations/0007_tasks.down.sql", Change: plan.New, Subtask: "S1"},
				{Path: "store/pg.go", Change: plan.New, Subtask: "S2"},
				{Path: "store/pg_test.go", Change: plan.New, Subtask: "S2"},
				{Path: "api/tasks.go", Change: plan.New, Subtask: "S3"},
				{Path: "api/routes.go", Change: plan.Changed, Subtask: "S3"},
				{Path: "web/src/TaskForm.tsx", Change: plan.New, Subtask: "S4"},
				{Path: "web/src/routes.tsx", Change: plan.Changed, Subtask: "S4"},
			}},
		{Kind: plan.KindCompare, Title: "queue",
			Columns: []string{"option", "for", "against"},
			Rows: [][]string{
				{"SKIP LOCKED (picked)", "no new service, one transaction", "polling"},
				{"LISTEN/NOTIFY", "instant wake-ups", "drops events while disconnected"},
			}},
		{Kind: plan.KindNote, Tone: "question", Title: "retention",
			Text: "Finished tasks stay forever in this plan. Should they be deleted after 30 days instead?"},
	}
}

func (d *Demo) run(ctx context.Context) {
	root := fmt.Sprintf("demo-%06x", rand.IntN(0xffffff))
	d.mu.Lock()
	d.commits, d.tips = nil, nil
	d.mu.Unlock()
	lead, rev := d.h("lead"), d.h("reviewer")
	h1, s1, s2, o1 := d.h("haiku-1"), d.h("sonnet-1"), d.h("sonnet-2"), d.h("opus-1")
	p := &plan.Plan{
		Goal: "Durable tasks in Postgres behind store.Store, exposed at /api/tasks, with a web form. Tests stay green.",
		Subtasks: []*plan.Subtask{
			{ID: "S1", Title: "schema migration", Model: "haiku", Status: plan.Pending,
				Spec:       "Add migrations/0007_tasks.sql with tables tasks (id, context_id, title, state, created_at) and task_events (task_id, at, kind, data), and the matching down migration.",
				Acceptance: "go run ./cmd/migrate -dry applies up and down cleanly."},
			{ID: "S2", Title: "pg task store", Model: "sonnet", Deps: []string{"S1"}, Status: plan.Pending,
				Spec:       "Implement store.Store in store/pg.go on the new tables. Claim queued tasks with FOR UPDATE SKIP LOCKED. Keep the memory store for tests.",
				Acceptance: "store/pg_test.go runs the shared store suite against Postgres; go test ./store/... passes."},
			{ID: "S3", Title: "/api/tasks routes", Model: "sonnet", Deps: []string{"S1"}, Status: plan.Pending,
				Spec:       "GET and POST /api/tasks in api/tasks.go, registered in api/routes.go, following platform/api.go (errors, auth, JSON shapes).",
				Acceptance: "api/tasks_test.go covers list, create, a missing title (400) and auth (401)."},
			{ID: "S4", Title: "web task form", Model: "haiku", Deps: []string{"S3"}, Status: plan.Pending,
				Spec:       "web/src/TaskForm.tsx: a title field and a submit button that POSTs /api/tasks and shows the API's validation errors.",
				Acceptance: "npm test covers submit, an empty title and a server error."},
			{ID: "S5", Title: "integrate + checks", Model: "opus", Deps: []string{"S2", "S3", "S4"}, Status: plan.Pending,
				Spec:       "Run every check on the run branch and fix what breaks between the pieces.",
				Acceptance: "go vet, go test ./... and npm test pass on the run branch."},
		},
	}
	full := *p
	full.Subtasks = make([]*plan.Subtask, len(p.Subtasks))
	for i, st := range p.Subtasks {
		c := *st
		full.Subtasks[i] = &c
	}
	p.Version, full.Version = 1, 1
	blocks := demoBlocks()
	publishDoc := func() {
		doc := &plan.Doc{Plan: &full, Blocks: blocks, Shown: 1}
		raw, _ := json.Marshal(doc)
		d.mu.Lock()
		d.doc = doc
		d.mu.Unlock()
		d.art(root, root+"-doc", plan.DocName, string(raw), false)
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
	d.chunk(lead, root, "user_message_chunk", "add a postgres task store with /api/tasks routes, a web form and tests")
	d.chunk(lead, root, "agent_thought_chunk", "The store interface already exists in store/store.go, so the work splits cleanly: a migration first, then the store and the routes in parallel, then the form.")
	d.raw(lead, root, map[string]any{"sessionUpdate": "plan", "entries": []map[string]string{
		{"content": "Read the store and platform API", "status": "in_progress"}, {"content": "Publish a plan of 5 subtasks", "status": "pending"},
		{"content": "Delegate and review each subtask", "status": "pending"}, {"content": "Merge the run branch", "status": "pending"}}})
	d.work(ctx, root, []string{"Read README.md", "Glob **/*.go", "Read store/store.go"}, 2*time.Second)
	// Two questions before the plan, answered in the form (or by themselves).
	qs := []ask.Question{
		{Header: "Store", Question: "Where should tasks live?", Options: []ask.Option{
			{Label: "Postgres (Recommended)", Description: "next to the existing tables, with a migration"},
			{Label: "SQLite", Description: "a file per instance; simpler, no shared state"}}},
		{Header: "Surfaces", Question: "Which surfaces should create tasks?", MultiSelect: true, Options: []ask.Option{
			{Label: "API", Description: "POST /api/tasks"}, {Label: "Web form"}, {Label: "CLI"}}},
	}
	qtext := "Two questions before I plan:\n\n" + ask.Text(qs)
	qid := root + "-q"
	d.task(qid, root, lead, d.Human, qtext)
	d.state(qid, "TASK_STATE_INPUT_REQUIRED", "Waiting for you to answer.")
	d.mu.Lock()
	d.waiting = &Wait{TaskID: qid, Asker: lead, Question: qtext, Since: time.Now(), Questions: qs}
	d.mu.Unlock()
	qans := "Store: Postgres (Recommended)\nSurfaces: API, Web form"
	select {
	case qans = <-d.answer:
	case <-time.After(4 * time.Second):
	case <-ctx.Done():
		return
	}
	d.mu.Lock()
	d.waiting = nil
	d.mu.Unlock()
	d.send(lg.Entry{Kind: "task", TaskID: qid, ParentTaskID: root, Sender: lead, Recipient: d.Human, State: "TASK_STATE_COMPLETED", Summary: qans})
	d.work(ctx, root, []string{"mcp__halo__publish_plan", "mcp__halo__show"}, time.Second)
	publish()
	publishDoc()
	sleep(ctx, time.Second)
	// The approval waits for you (it approves itself after a while). Notes from
	// the studio come back as a rejection with text: the lead revises once and
	// asks again.
	var answer string
	for round := 1; ; round++ {
		apr := fmt.Sprintf("%s-ask%d", root, round)
		q := "Approve this plan? 5 subtasks: S1 schema (haiku), S2 pg store (sonnet), S3 routes (sonnet), S4 web form (haiku), S5 integrate (opus)."
		if round > 1 {
			q = fmt.Sprintf("Revised the plan (version %d) from your notes. Approve?", full.Version)
		}
		d.task(apr, root, lead, d.Human, q)
		d.state(apr, "TASK_STATE_INPUT_REQUIRED", "Waiting for you to answer.")
		d.mu.Lock()
		d.waiting = &Wait{TaskID: apr, Asker: lead, Question: q, Since: time.Now(), Approval: true}
		d.mu.Unlock()
		answer = "approve keep the API under /api"
		select {
		case answer = <-d.answer:
		case <-time.After(20 * time.Second):
		case <-ctx.Done():
			return
		}
		d.mu.Lock()
		d.waiting = nil
		d.mu.Unlock()
		notes := strings.TrimSpace(strings.TrimPrefix(answer, "reject"))
		if !strings.HasPrefix(answer, "reject") || notes == "" || round > 2 {
			if strings.HasPrefix(answer, "reject") {
				d.send(lg.Entry{Kind: "task", TaskID: apr, ParentTaskID: root, Sender: lead, Recipient: d.Human, State: "TASK_STATE_REJECTED", Summary: answer})
				d.state(root, "TASK_STATE_COMPLETED", "")
				return
			}
			d.send(lg.Entry{Kind: "task", TaskID: apr, ParentTaskID: root, Sender: lead, Recipient: d.Human, State: "TASK_STATE_COMPLETED", Summary: strings.TrimPrefix(answer, "approve ")})
			break
		}
		d.send(lg.Entry{Kind: "task", TaskID: apr, ParentTaskID: root, Sender: lead, Recipient: d.Human, State: "TASK_STATE_REJECTED", Summary: notes})
		d.chunk(lead, root, "agent_thought_chunk", "Revising the plan from the notes: "+firstLine(strings.TrimPrefix(notes, "My notes on the plan")))
		d.work(ctx, root, []string{"Read store/store.go", "mcp__halo__publish_plan", "mcp__halo__show"}, 2*time.Second)
		// A scripted revision: S2 gets the index the reviewer would ask for later,
		// and a note answers the notes.
		s2 := full.Get("S2")
		s2.Spec += " Index tasks.context_id: the API filters on it."
		full.Version++
		p.Version = full.Version
		blocks = append(demoBlocks(), plan.Block{Kind: plan.KindNote, Tone: "info", Title: "revised",
			Text: "Your notes: " + noteBody(notes) + " (the demo only adds an index to S2; a real lead revises from each note.)"})
		publish()
		publishDoc()
		sleep(ctx, time.Second)
	}
	p.Approved, p.Branch = true, "halo/run-"+root[len(root)-4:]
	publish()
	d.state(root, "TASK_STATE_WORKING", "Finished mcp__halo__delegate S1")

	t1 := dispatch("S1", h1, "schema migration: tasks + task_events, with down")
	d.work(ctx, t1, []string{"Read migrations/0006_links.sql", "Write migrations/0007_tasks.sql", "Bash go run ./cmd/migrate -dry"}, 4*time.Second)
	d.commit("halo: S1 schema migration", "migrations/0007_tasks.sql", "migrations/0007_tasks.down.sql")
	d.reply(t1, "Migration 0007 adds tasks and task_events, with a down migration.")
	review("S1", "APPROVE. Up and down are symmetric.", []string{"Bash git diff halo/run..halo/S1", "Read migrations/0007_tasks.sql"})
	d.commit("halo: merge S1 schema migration")
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
	d.reply(q, "/api. Match platform/api.go; ada asked for it.")
	set("S3", plan.Running, "", "")
	d.work(ctx, t3, []string{"Edit api/tasks.go", "Bash go test ./api/..."}, 3*time.Second)
	d.commit("halo: S2 pg task store", "store/pg.go", "store/pg_test.go")
	d.reply(t2, "Store uses FOR UPDATE SKIP LOCKED for the queue. 12 tests pass.")
	review("S2", "CHANGES: tasks.context_id is filtered without an index.", []string{"Bash git diff halo/run..halo/S2", "Read store/pg.go"})
	p.Get("S2").Round = 1
	set("S2", plan.Changes, "", "")
	f2 := t2 + "-f"
	d.task(f2, root, lead, s1, plan.Tag("S2")+"reviewer: add an index on tasks.context_id, then re-run the tests")
	set("S2", plan.Running, s1, f2)
	d.commit("halo: S3 /api/tasks routes", "api/tasks.go", "api/tasks_test.go", "api/routes.go")
	d.reply(t3, "GET and POST /api/tasks are wired, 9 tests pass.")
	review("S3", "APPROVE. Matches the platform conventions.", []string{"Bash git diff halo/run..halo/S3", "Read api/tasks.go"})
	d.commit("halo: merge S3 /api/tasks routes")
	set("S3", plan.Done, "", "")
	d.work(ctx, f2, []string{"Edit migrations/0007_tasks.sql", "Bash go test ./store/..."}, 3*time.Second)
	d.commit("halo: S2 pg task store", "migrations/0007_tasks.sql")
	d.reply(f2, "Added tasks_context_id_idx. 13 tests pass.")
	review("S2", "APPROVE. Index added.", []string{"Bash git diff halo/S2~1..halo/S2"})
	d.commit("halo: merge S2 pg task store")
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
	stopDev := make(chan struct{})
	defer close(stopDev)
	go d.devServer(ctx, s2, t4b, stopDev)
	d.work(ctx, t4b, []string{"Read web/src/TaskForm.tsx", "Read api/tasks.go", "Edit web/src/TaskForm.tsx", "Bash npm test"}, 5*time.Second)
	d.commit("halo: S4 web task form", "web/src/TaskForm.tsx", "web/src/TaskForm.test.tsx")
	d.reply(t4b, "The form validates with the API's schema. 31 tests pass.")
	review("S4", "APPROVE. Errors are accessible.", []string{"Bash git diff halo/run..halo/S4", "Read web/src/TaskForm.tsx"})
	d.commit("halo: merge S4 web task form")
	set("S4", plan.Done, "", "")

	t5 := dispatch("S5", o1, "run every check on the run branch, fix what breaks")
	d.work(ctx, t5, []string{"Bash go vet ./...", "Bash go test ./...", "Bash npm test", "Edit go.sum"}, 6*time.Second)
	d.commit("halo: S5 integrate + checks", "go.sum")
	d.commit("halo: merge S5 integrate + checks")
	d.reply(t5, "All checks pass on "+p.Branch+": 48 Go tests, 31 web tests.")
	set("S5", plan.Done, "", "")
	d.state(root, "TASK_STATE_WORKING", "Finished mcp__halo__wait_events")
	d.commit("halo: merge " + p.Branch)
	p.MergedInto = "main"
	publish()
	d.report(root, report.Report{
		Headline: p.Goal, Status: "landed on main", Progress: &report.Progress{Done: 5, Total: 5},
		Built: []report.Item{
			{Title: "S1 schema migration", Detail: "haiku · 1m"},
			{Title: "S2 pg task store", Detail: "sonnet · 2 review rounds · 3m"},
			{Title: "S3 /api/tasks routes", Detail: "sonnet · 2m"},
			{Title: "S4 web task form", Detail: "escalated to sonnet · 2m"},
			{Title: "S5 integrate + checks", Detail: "opus · 1m"},
		},
		Stats: []report.Stat{{Label: "Wall time", Value: "9m"}, {Label: "Review rounds", Value: "2"}, {Label: "Escalations", Value: "1"},
			{Label: "Waited on you", Value: "8s"}, {Label: "Changes", Value: "14 files +612 −48"}},
		Outcome: "Merged " + p.Branch + " into main.", Notice: "Merged " + p.Branch + " into main.",
	})
	d.reply(root, "Run complete: 5/5 merged into "+p.Branch+", then "+p.Branch+" merged into main. 2 review rounds on S2, S4 moved up from haiku to sonnet.")
}
