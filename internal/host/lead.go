package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"
	"github.com/three-agent-problem/lagrange/delegate"

	"github.com/three-agent-problem/halo/internal/config"
	"github.com/three-agent-problem/lagrange/report"

	"github.com/three-agent-problem/halo/internal/plan"
)

// lead serves the lead's address. A prompt from the person starts (or continues) the
// lead's Claude session, which orchestrates through the halo MCP tools. A question
// from a worker (ask_lead) is not a new session: it is held open as a task until the
// lead answers it with the answer tool. Nor is a message from the person to an agent
// during a run (human_message): it is routed to the worker or the lead at once.
type lead struct {
	h      *Host
	agent  config.Agent
	handle string
	exec   *a2aacp.Executor

	mu    sync.Mutex
	runs  map[string]*run   // by root platform task
	byCtx map[string]*run   // the newest run of each of the lead's A2A contexts
	ctxOf map[string]string // the context of each run
	nruns int               // runs created so far, to order them

	// relay sends a human message to a subtask's worker; nil uses run.followUp.
	// Tests replace it.
	relay func(ctx context.Context, r *run, id, text string) error
	// cancel cancels a worker's task; nil cancels it through Lagrange as the
	// lead. Tests replace it.
	cancel func(ctx context.Context, worker, task string) error
}

func (h *Host) newLead(ctx context.Context, a config.Agent, logw io.Writer) (*lead, error) {
	l := &lead{h: h, agent: a, handle: h.handle(a), runs: map[string]*run{}, byCtx: map[string]*run{}, ctxOf: map[string]string{}}
	workers := map[string][]string{}
	for _, w := range h.cfg.ByRole(config.RoleWorker) {
		workers[w.Model] = append(workers[w.Model], "@"+h.handle(w))
	}
	preface := leadPrompt(l.handle, h.st.Human, workers, h.handle(h.cfg.Reviewer()))
	exec, err := a2aacp.NewExecutor(ctx, h.agent(a, logw), &a2aacp.Config{
		Converter: h.tap(l.handle),
		// The lead reads and runs commands (git log, tests) but never edits.
		AutoAnswer: a2aacp.ToolRules{Allow: []string{"*"}, Deny: []string{"edit", "delete", "move"}},
		Settings: func(context.Context, *a2asrv.ExecutorContext) (map[string]any, error) {
			return map[string]any{"model": a.Model}, nil
		},
		Session: func(ctx context.Context, ec *a2asrv.ExecutorContext) (a2aacp.SessionParams, error) {
			srv, _ := json.Marshal(map[string]any{"type": "http", "name": "halo", "url": h.mcpURL + "/lead",
				"headers": []map[string]string{{"name": "Halo-Run", "value": delegate.Task(ctx)}}})
			return a2aacp.SessionParams{Cwd: h.cfg.Repo, MCPServers: []json.RawMessage{srv}}, nil
		},
		SessionStore: h.sessions.scope("lead"),
		Prompt: func(_ context.Context, ec *a2asrv.ExecutorContext) ([]*a2a.Part, error) {
			if ec.StoredTask != nil {
				return ec.Message.Parts, nil
			}
			return append([]*a2a.Part{a2a.NewTextPart(preface + "\n\nThe request:\n")}, ec.Message.Parts...), nil
		},
	})
	if err != nil {
		return nil, err
	}
	l.exec = exec
	return l, nil
}

// Execute implements a2asrv.AgentExecutor.
func (l *lead) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	switch m := metaOf(ec.Message); m.Kind {
	case "question":
		return l.question(ctx, ec, m)
	case "human_message":
		return l.humanMessage(ctx, ec, m)
	case "stop_subtask":
		return l.stopSubtask(ctx, ec, m)
	}
	r := l.runFor(ctx, ec)
	inner := l.exec.Execute(ctx, ec)
	return func(yield func(a2a.Event, error) bool) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		type item struct {
			ev  a2a.Event
			err error
		}
		evs := make(chan item)
		go func() {
			defer close(evs)
			for ev, err := range inner {
				select {
				case evs <- item{ev, err}:
				case <-ctx.Done():
					return
				}
			}
		}()
		emit := r.attach(ec)
		defer r.detach()
		done := false
		for {
			select {
			case it, ok := <-evs:
				if !ok {
					return
				}
				if !yield(it.ev, it.err) {
					return
				}
				done = done || final(it.ev)
			case ev := <-emit:
				if done {
					continue
				}
				if !yield(ev, nil) {
					return
				}
			}
		}
	}
}

// Cancel implements a2asrv.AgentExecutor.
func (l *lead) Cancel(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return l.exec.Cancel(ctx, ec)
}

func final(ev a2a.Event) bool {
	switch e := ev.(type) {
	case *a2a.TaskStatusUpdateEvent:
		return e.Status.State.Terminal() || e.Status.State == a2a.TaskStateInputRequired
	case *a2a.Task:
		return e.Status.State.Terminal()
	}
	return false
}

func (l *lead) runFor(ctx context.Context, ec *a2asrv.ExecutorContext) *run {
	l.mu.Lock()
	defer l.mu.Unlock()
	id := delegate.Task(ctx)
	if id == "" {
		id = string(ec.TaskID)
	}
	if r, ok := l.runs[id]; ok {
		return r
	}
	// A new prompt in a context the lead knows (a resumed conversation) is a new
	// run: its own plan and branch, but the same Claude session, which remembers.
	l.nruns++
	r := &run{l: l, id: id, short: shortID(id), seq: l.nruns, events: make(chan event, 256), questions: map[string]*question{}, started: time.Now()}
	l.runs[id], l.byCtx[ec.ContextID], l.ctxOf[id] = r, r, ec.ContextID
	return r
}

// run returns the run the lead's MCP session id belongs to. A session keeps the
// run it was created for in its headers, so this follows its context to the
// newest run.
func (l *lead) run(id string) *run {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.runs[id]
	if r == nil {
		return nil
	}
	if n := l.byCtx[l.ctxOf[id]]; n != nil {
		return n
	}
	return r
}

func shortID(id string) string {
	id = strings.ReplaceAll(id, "-", "")
	if len(id) > 6 {
		return id[len(id)-6:]
	}
	return id
}

// question holds a worker's ask_lead open until the lead answers.
func (l *lead) question(ctx context.Context, ec *a2asrv.ExecutorContext, m meta) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		r := l.run(m.Run)
		if r == nil {
			msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart("No such run here; decide yourself and say so in your summary."))
			yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, msg), nil)
			return
		}
		if !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
			return
		}
		q := r.ask(m.From, m.Subtask, textOf(ec.Message))
		var ans string
		select {
		case ans = <-q.answer:
		case <-ctx.Done():
			r.forget(q.id)
			return
		case <-time.After(30 * time.Minute):
			r.forget(q.id)
			ans = "The lead didn't answer in time. Make the most reasonable choice and say which in your summary."
		}
		msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(ans))
		yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, msg), nil)
	}
}

// event is something wait_events hands the lead.
type event struct {
	// question, human, human_message (the person messaged an agent during the run),
	// subtask_done, subtask_failed, review, conflict, merged
	Kind     string `json:"kind"`
	ID       string `json:"id,omitempty"`
	Subtask  string `json:"subtask,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"` // human_message: who the person addressed
	Text     string `json:"text,omitempty"`
	Approved *bool  `json:"approved,omitempty"`
	Hint     string `json:"next,omitempty"`
}

type question struct {
	id, from, subtask, text string
	answer                  chan string
}

// run is one prompt's orchestration state.
type run struct {
	l     *lead
	id    string // the root platform task
	short string
	seq   int // the order runs were created in

	mu        sync.Mutex
	plan      *plan.Plan
	branch    string
	base      string // the branch checked out when the plan was approved; "" on a detached HEAD
	runDir    string
	landing   landState
	ec        *a2asrv.ExecutorContext
	emit      chan a2a.Event
	events    chan event
	questions map[string]*question
	qseq      int
	approval  string // the task asking for plan approval
	subs      map[string]*subRT
	started   time.Time     // when the run started
	waited    time.Duration // how long it waited on the person
	changes   string        // the run branch's diff stat, once it lands
	final     *report.Report
}

// landState is how far merging the finished run branch into base got. It only ever
// moves forward, so the run is landed at most once.
type landState int

const (
	landNone    landState = iota
	landRunning           // every subtask is done; Land and Cleanup are running
	landFailed            // Land failed; the run branch and worktrees are kept
	landMerged            // merged into base and cleaned up; the run is finished
)

// openLocked refuses tools that change the run once it is being merged or merged.
// Callers hold r.mu.
func (r *run) openLocked() error {
	switch r.landing {
	case landRunning:
		return fmt.Errorf("every subtask is done and halo is merging %s into %s; wait for the merged event", r.branch, r.base)
	case landMerged:
		return errors.New("the run is merged and finished")
	}
	return nil
}

// attach makes ec the execution plan updates are streamed into.
func (r *run) attach(ec *a2asrv.ExecutorContext) <-chan a2a.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ec = ec
	r.emit = make(chan a2a.Event, 64)
	return r.emit
}

func (r *run) detach() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ec, r.emit = nil, nil
}

// publish streams the plan, as the halo.plan artifact of the root task. Callers
// hold r.mu.
func (r *run) publishLocked() {
	if r.plan == nil || r.ec == nil {
		return
	}
	raw, _ := json.Marshal(r.plan.Public())
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	ev := &a2a.TaskArtifactUpdateEvent{TaskID: r.ec.TaskID, ContextID: r.ec.ContextID,
		Artifact: &a2a.Artifact{ID: "halo-plan", Name: plan.ArtifactName, Parts: a2a.ContentParts{a2a.NewDataPart(data)}}}
	r.emitLocked(ev)
	r.publishReportLocked("")
}

// update changes a subtask under the lock and publishes the plan.
func (r *run) update(id string, f func(s *plan.Subtask)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return
	}
	if s := r.plan.Get(id); s != nil {
		f(s)
		r.publishLocked()
	}
}

func (r *run) push(e event) {
	select {
	case r.events <- e:
	default:
		r.l.h.log.Warn("event queue full", "run", r.id, "event", e.Kind)
	}
}

func (r *run) ask(from, subtask, text string) *question {
	r.mu.Lock()
	r.qseq++
	q := &question{id: fmt.Sprintf("Q%d", r.qseq), from: from, subtask: subtask, text: text, answer: make(chan string, 1)}
	r.questions[q.id] = q
	r.mu.Unlock()
	r.update(subtask, func(s *plan.Subtask) {
		if s.Status == plan.Running {
			s.Status = plan.Asking
		}
	})
	r.push(event{Kind: "question", ID: q.id, Subtask: subtask, From: "@" + from, Text: text,
		Hint: "answer it with answer(question_id, text)"})
	return q
}

func (r *run) forget(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.questions, id)
}

// reply answers a question; it reports false if there is no such question.
func (r *run) reply(id, text string) bool {
	r.mu.Lock()
	q, ok := r.questions[strings.ToUpper(id)]
	delete(r.questions, strings.ToUpper(id))
	r.mu.Unlock()
	if !ok {
		return false
	}
	q.answer <- text
	r.update(q.subtask, func(s *plan.Subtask) {
		if s.Status == plan.Asking {
			s.Status = plan.Running
		}
	})
	return true
}
