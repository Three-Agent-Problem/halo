package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/three-agent-problem/halo/internal/ask"
	"github.com/three-agent-problem/halo/internal/config"
	"github.com/three-agent-problem/halo/internal/lg"
	"github.com/three-agent-problem/halo/internal/plan"
)

// out is every tool's result: one readable string.
type out struct {
	Result string `json:"result"`
}

func ok(format string, a ...any) (*mcp.CallToolResult, out, error) {
	return nil, out{Result: fmt.Sprintf(format, a...)}, nil
}

// await polls a task through the Lagrange API until it ends, and returns its final
// state and answer.
func (h *Host) await(ctx context.Context, id string) (a2a.TaskState, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Hour)
	defer cancel()
	for {
		t, err := h.api.Task(ctx, id)
		if err == nil && a2a.TaskState(t.State).Terminal() {
			return a2a.TaskState(t.State), lg.TaskText(t.Task), nil
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ── worker tools ─────────────────────────────────────────────────────────────

func (h *Host) workerMCP() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		agent, key := r.Header.Get("Halo-Agent"), r.Header.Get("Halo-Session")
		s := mcp.NewServer(&mcp.Implementation{Name: "halo", Version: "0.1.0"}, nil)
		type askIn struct {
			Question string `json:"question" jsonschema:"one specific question, with the options you see and the one you'd pick"`
		}
		mcp.AddTool(s, &mcp.Tool{Name: "ask_lead", Description: "Ask the lead engineer a question that blocks your subtask, and wait for the answer (it can take a few minutes)."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in askIn) (*mcp.CallToolResult, out, error) {
				w := h.workers[agent]
				if w == nil {
					return nil, out{}, errors.New("unknown agent")
				}
				w.mu.Lock()
				b := w.work[key]
				w.mu.Unlock()
				if b == nil || b.meta.Run == "" {
					return ok("There is no lead for this task. Decide yourself and say what you decided in your summary.")
				}
				m := meta{Kind: "question", Run: b.meta.Run, Subtask: b.meta.Subtask, From: w.handle}
				res, err := h.caller(w.handle).Send(ctx, lg.Call{To: h.lead.handle, Parent: b.platform, Text: in.Question, Meta: m.asMap(), Immediate: true})
				if err != nil {
					return nil, out{}, err
				}
				t, isTask := res.(*a2a.Task)
				if !isTask {
					return ok("%s", lg.Text(res))
				}
				_, ans, err := h.await(ctx, string(t.ID))
				if err != nil {
					return nil, out{}, err
				}
				return ok("%s", ans)
			})
		return s
	}, nil)
}

// ── lead tools ───────────────────────────────────────────────────────────────

type subIn struct {
	ID         string   `json:"id" jsonschema:"S1, S2, ..."`
	Title      string   `json:"title" jsonschema:"3-5 words"`
	Spec       string   `json:"spec" jsonschema:"what to build: files, functions, behavior; enough for someone new to the repo"`
	Acceptance string   `json:"acceptance" jsonschema:"the tests or checks that prove it works"`
	Model      string   `json:"model" jsonschema:"haiku, sonnet or opus"`
	Deps       []string `json:"deps,omitempty" jsonschema:"ids of subtasks whose code this one needs"`
}

type planIn struct {
	Goal     string  `json:"goal" jsonschema:"one or two sentences: what the run delivers"`
	Subtasks []subIn `json:"subtasks"`
}

type idIn struct {
	SubtaskID string `json:"subtask_id"`
}

type delegateIn struct {
	SubtaskID string `json:"subtask_id"`
	Context   string `json:"context,omitempty" jsonschema:"anything the worker should know beyond the spec"`
}

type followIn struct {
	SubtaskID string `json:"subtask_id"`
	Message   string `json:"message" jsonschema:"what to change, as concrete points"`
}

type escalateIn struct {
	SubtaskID string `json:"subtask_id"`
	Reason    string `json:"reason" jsonschema:"why the last attempt fell short, for the next worker"`
}

type askHumanIn struct {
	Question    string         `json:"question" jsonschema:"short and self-contained; for a plan, list the subtasks with their models; with questions, one line of context"`
	ApprovePlan bool           `json:"approve_plan,omitempty" jsonschema:"true when this asks to approve the published plan"`
	Questions   []ask.Question `json:"questions,omitempty" jsonschema:"1-4 multiple-choice questions, only when the goal is genuinely ambiguous (never with approve_plan); the human picks options or writes their own answer"`
}

type waitIn struct {
	TimeoutSeconds int `json:"timeout_seconds,omitempty" jsonschema:"how long to wait for an event, 5-120 (default 90)"`
}

type answerIn struct {
	QuestionID string `json:"question_id"`
	Text       string `json:"text"`
}

func (h *Host) leadMCP() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		runID := req.Header.Get("Halo-Run")
		s := mcp.NewServer(&mcp.Implementation{Name: "halo", Version: "0.1.0"}, nil)
		get := func() (*run, error) {
			if r := h.lead.run(runID); r != nil {
				return r, nil
			}
			return nil, errors.New("this session has no run")
		}
		// open is get for the tools that change the run: they stop working once
		// the finished run is being merged into its base branch.
		open := func() (*run, error) {
			r, err := get()
			if err != nil {
				return nil, err
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if err := r.openLocked(); err != nil {
				return nil, err
			}
			return r, nil
		}
		mcp.AddTool(s, &mcp.Tool{Name: "publish_plan", Description: "Publish (or, before approval, replace) the run's plan. Everyone watching sees it."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in planIn) (*mcp.CallToolResult, out, error) {
				r, err := open()
				if err != nil {
					return nil, out{}, err
				}
				return r.publishPlan(in)
			})
		mcp.AddTool(s, &mcp.Tool{Name: "ask_human", Description: "Ask the person a question in their Lagrange inbox. With questions, it is a multiple-choice form (Halo shows it like Claude Code's plan questions; the answer comes back as one line per question). Returns at once; the answer arrives through wait_events (kind human)."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in askHumanIn) (*mcp.CallToolResult, out, error) {
				r, err := open()
				if err != nil {
					return nil, out{}, err
				}
				return r.askHuman(ctx, in)
			})
		mcp.AddTool(s, &mcp.Tool{Name: "delegate", Description: "Send a subtask to a free worker of its model, in a fresh git worktree of the run branch. Returns at once; the result arrives through wait_events."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in delegateIn) (*mcp.CallToolResult, out, error) {
				r, err := open()
				if err != nil {
					return nil, out{}, err
				}
				return r.delegate(ctx, in.SubtaskID, in.Context)
			})
		mcp.AddTool(s, &mcp.Tool{Name: "follow_up", Description: "Send more work to the worker that did a subtask, in the same Claude session and worktree (it remembers the subtask)."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in followIn) (*mcp.CallToolResult, out, error) {
				r, err := open()
				if err != nil {
					return nil, out{}, err
				}
				return r.followUp(ctx, in.SubtaskID, in.Message)
			})
		mcp.AddTool(s, &mcp.Tool{Name: "request_review", Description: "Send a finished subtask's diff to the reviewer. APPROVE merges it into the run branch; CHANGES comes back as a review event."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, out, error) {
				r, err := open()
				if err != nil {
					return nil, out{}, err
				}
				return r.requestReview(ctx, in.SubtaskID)
			})
		mcp.AddTool(s, &mcp.Tool{Name: "accept", Description: "Merge a finished subtask into the run branch without (more) review."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, out, error) {
				r, err := open()
				if err != nil {
					return nil, out{}, err
				}
				return r.accept(ctx, in.SubtaskID)
			})
		mcp.AddTool(s, &mcp.Tool{Name: "escalate", Description: "Move a subtask one model up (haiku → sonnet → opus) and send it to a worker of that model in a fresh worktree."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in escalateIn) (*mcp.CallToolResult, out, error) {
				r, err := open()
				if err != nil {
					return nil, out{}, err
				}
				return r.escalate(ctx, in.SubtaskID, in.Reason)
			})
		mcp.AddTool(s, &mcp.Tool{Name: "wait_events", Description: "Wait for what happened next: worker questions, finished or failed subtasks, reviews, merge conflicts, answers from the person, and finally the merged event (halo merging the finished run branch into the base branch, or why it couldn't). Call it in a loop until the merged event arrives."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in waitIn) (*mcp.CallToolResult, out, error) {
				r, err := get()
				if err != nil {
					return nil, out{}, err
				}
				return r.wait(ctx, in.TimeoutSeconds)
			})
		mcp.AddTool(s, &mcp.Tool{Name: "answer", Description: "Answer a worker's question (from a question event)."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in answerIn) (*mcp.CallToolResult, out, error) {
				r, err := get()
				if err != nil {
					return nil, out{}, err
				}
				if !r.reply(in.QuestionID, in.Text) {
					return nil, out{}, fmt.Errorf("no open question %s", in.QuestionID)
				}
				return ok("Sent to the worker.")
			})
		mcp.AddTool(s, &mcp.Tool{Name: "status", Description: "The plan with every subtask's status, worker, branch and latest note."},
			func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, out, error) {
				r, err := get()
				if err != nil {
					return nil, out{}, err
				}
				return ok("%s", r.statusText())
			})
		return s
	}, nil)
}

// ── what the lead's tools do ─────────────────────────────────────────────────

// subRT is where a subtask's current attempt lives.
type subRT struct {
	branch, dir, session, worker string
	stoppedBy                    string // the person who stopped the current attempt
}

func (r *run) rt(id string) *subRT {
	if r.subs == nil {
		r.subs = map[string]*subRT{}
	}
	if r.subs[id] == nil {
		r.subs[id] = &subRT{}
	}
	return r.subs[id]
}

func (r *run) publishPlan(in planIn) (*mcp.CallToolResult, out, error) {
	h := r.l.h
	p := &plan.Plan{Goal: strings.TrimSpace(in.Goal)}
	for _, s := range in.Subtasks {
		p.Subtasks = append(p.Subtasks, &plan.Subtask{ID: strings.ToUpper(strings.TrimSpace(s.ID)), Title: s.Title, Spec: s.Spec,
			Acceptance: s.Acceptance, Model: strings.ToLower(s.Model), Deps: upper(s.Deps), Status: plan.Pending})
	}
	if err := p.Validate(h.cfg.WorkerModels()); err != nil {
		return nil, out{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan != nil && r.plan.Approved {
		return nil, out{}, errors.New("the plan is approved and can't be replaced; use follow_up and escalate")
	}
	r.plan = p
	r.publishLocked()
	return ok("Published %d subtasks. Next: ask_human with approve_plan=true, then wait_events.", len(p.Subtasks))
}

func upper(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strings.ToUpper(strings.TrimSpace(id))
	}
	return out
}

func (r *run) askHuman(ctx context.Context, in askHumanIn) (*mcp.CallToolResult, out, error) {
	h := r.l.h
	r.mu.Lock()
	if in.ApprovePlan && r.plan == nil {
		r.mu.Unlock()
		return nil, out{}, errors.New("publish_plan first")
	}
	r.mu.Unlock()
	call := lg.Call{To: h.st.Human, Parent: r.id, Text: in.Question, Immediate: true}
	if len(in.Questions) > 0 {
		if in.ApprovePlan {
			return nil, out{}, errors.New("ask questions and the plan approval separately")
		}
		if err := ask.Validate(in.Questions); err != nil {
			return nil, out{}, err
		}
		call.Text = strings.TrimSpace(nonEmpty(in.Question, "A few questions before I plan:") + "\n\n" + ask.Text(in.Questions))
		call.Meta = map[string]any{ask.MetaKey: in.Questions}
	}
	res, err := h.caller(r.l.handle).Send(ctx, call)
	if err != nil {
		return nil, out{}, err
	}
	t, isTask := res.(*a2a.Task)
	if !isTask {
		return ok("@%s answered: %s", h.st.Human, lg.Text(res))
	}
	id := string(t.ID)
	if in.ApprovePlan {
		r.mu.Lock()
		r.approval = id
		r.mu.Unlock()
	}
	go func() {
		state, text, err := h.await(h.ctx, id)
		if err != nil {
			return
		}
		approved := state == a2a.TaskStateCompleted
		e := event{Kind: "human", ID: id, From: "@" + h.st.Human, Text: text, Approved: &approved}
		if in.ApprovePlan {
			if approved {
				if err := r.approve(h.ctx); err != nil {
					e.Text += "\n(halo couldn't create the run branch: " + err.Error() + ")"
				}
				e.Hint = "the plan is approved: delegate every subtask without dependencies"
			} else {
				e.Hint = "revise the plan with publish_plan and ask again"
			}
		}
		r.push(e)
	}()
	return ok("Asked @%s (task %s). The answer arrives through wait_events.", h.st.Human, id)
}

func (r *run) approve(ctx context.Context) error {
	// The branch checked out now is what the finished run is merged into. On a
	// detached HEAD there is none, and the run branch is left for a manual merge.
	base, _ := r.l.h.wt.base(ctx)
	branch, dir, err := r.l.h.wt.runBranch(ctx, r.short)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.branch, r.runDir, r.base = branch, dir, base
	r.plan.Approved, r.plan.Branch = true, branch
	r.publishLocked()
	return nil
}

// pick chooses the least busy worker of a model.
func (h *Host) pick(model string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	best, load := "", 1<<30
	for _, a := range h.cfg.ByRole(config.RoleWorker) {
		if a.Model != model {
			continue
		}
		hd := h.handle(a)
		if h.busy[hd] < load {
			best, load = hd, h.busy[hd]
		}
	}
	if best == "" {
		return "", fmt.Errorf("no %s worker in the roster", model)
	}
	h.busy[best]++
	return best, nil
}

func (h *Host) release(handle string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.busy[handle] > 0 {
		h.busy[handle]--
	}
}

func (r *run) delegate(ctx context.Context, id, extra string) (*mcp.CallToolResult, out, error) {
	h := r.l.h
	r.mu.Lock()
	if r.plan == nil || !r.plan.Approved {
		r.mu.Unlock()
		return nil, out{}, errors.New("the plan isn't approved yet: ask_human with approve_plan=true and wait for the answer")
	}
	s := r.plan.Get(id)
	if s == nil {
		r.mu.Unlock()
		return nil, out{}, fmt.Errorf("no subtask %s", id)
	}
	if s.Status != plan.Pending && s.Status != plan.Failed {
		r.mu.Unlock()
		return nil, out{}, fmt.Errorf("%s is %s; use follow_up to send its worker more work", s.ID, s.Status)
	}
	if !r.plan.DepsDone(s) {
		r.mu.Unlock()
		return nil, out{}, fmt.Errorf("%s waits for %s to be merged", s.ID, strings.Join(s.Deps, ", "))
	}
	var merged []string
	for _, o := range r.plan.Subtasks {
		if o.Status == plan.Done {
			merged = append(merged, o.ID+" "+o.Title)
		}
	}
	s.Attempts++
	attempt, model, title, spec, acc, sid := s.Attempts, s.Model, s.Title, s.Spec, s.Acceptance, s.ID
	runBranch := r.branch
	r.mu.Unlock()

	branch, dir, err := h.wt.subtask(ctx, r.short, runBranch, sid, attempt)
	if err != nil {
		return nil, out{}, err
	}
	worker, err := h.pick(model)
	if err != nil {
		return nil, out{}, err
	}
	text := "Subtask " + sid + ": " + title + "\n\n" + spec + "\n\nAcceptance: " + acc
	if len(merged) > 0 {
		text += "\n\nAlready merged into your starting point: " + strings.Join(merged, "; ") + "."
	}
	if extra != "" {
		text += "\n\nFrom the lead: " + extra
	}
	session := fmt.Sprintf("%s/%s/%d", r.short, sid, attempt)
	m := meta{Run: r.id, Subtask: sid, Title: title, Worktree: dir, Branch: branch, Session: session}
	task, err := r.send(ctx, worker, text, m)
	if err != nil {
		h.release(worker)
		return nil, out{}, err
	}
	r.mu.Lock()
	rt := r.rt(sid)
	rt.branch, rt.dir, rt.session, rt.worker = branch, dir, session, worker
	r.mu.Unlock()
	r.update(sid, func(s *plan.Subtask) {
		s.Status, s.Worker, s.Task, s.Note = plan.Running, worker, task, ""
		s.StartedAt, s.EndedAt = time.Now().UnixMilli(), 0
	})
	r.watchWork(sid, worker, task)
	return ok("%s went to @%s on %s (branch %s). Its result arrives through wait_events.", sid, worker, model, branch)
}

// send sends a message as the lead, inside the run, and returns the task ID.
func (r *run) send(ctx context.Context, to, text string, m meta) (string, error) {
	res, err := r.l.h.caller(r.l.handle).Send(ctx, lg.Call{To: to, Parent: r.id, Text: text, Meta: m.asMap(), Immediate: true})
	if err != nil {
		return "", err
	}
	t, isTask := res.(*a2a.Task)
	if !isTask {
		return "", fmt.Errorf("@%s answered without a task: %s", to, lg.Text(res))
	}
	return string(t.ID), nil
}

// watchWork waits for a worker's task and reports it to the lead.
func (r *run) watchWork(id, worker, task string) {
	h := r.l.h
	go func() {
		defer h.release(worker)
		state, text, err := h.await(h.ctx, task)
		if err != nil {
			return
		}
		r.workEnded(id, worker, state, text)
	}()
}

// workEnded records how a worker's attempt at subtask id ended and tells the lead.
func (r *run) workEnded(id, worker string, state a2a.TaskState, text string) {
	if state == a2a.TaskStateCompleted {
		r.update(id, func(s *plan.Subtask) { s.Status, s.Note = plan.Ready, firstLine(text) })
		r.push(event{Kind: "subtask_done", Subtask: id, From: "@" + worker, Text: clip(text, 1500), Hint: "request_review, or accept if trivial"})
		return
	}
	r.mu.Lock()
	by := ""
	if rt := r.subs[id]; rt != nil {
		by, rt.stoppedBy = rt.stoppedBy, ""
	}
	r.mu.Unlock()
	if by != "" {
		r.update(id, func(s *plan.Subtask) { s.Status, s.Note = plan.Failed, "stopped by "+by })
		r.push(event{Kind: "subtask_failed", Subtask: id, From: by, Text: by + " stopped @" + worker + "'s work on " + id + " on purpose.",
			Hint: "don't retry or escalate on your own: ask_human what they want (re-delegate with new context, change the plan, or drop it)"})
		return
	}
	r.update(id, func(s *plan.Subtask) { s.Status, s.Note = plan.Failed, firstLine(nonEmpty(text, string(state))) })
	r.push(event{Kind: "subtask_failed", Subtask: id, From: "@" + worker, Text: clip(nonEmpty(text, string(state)), 1500),
		Hint: "escalate, or follow_up with guidance"})
}

func (r *run) followUp(ctx context.Context, id, message string) (*mcp.CallToolResult, out, error) {
	h := r.l.h
	r.mu.Lock()
	s := r.planGet(id)
	if s == nil || r.subs[s.ID] == nil || r.subs[s.ID].worker == "" {
		r.mu.Unlock()
		return nil, out{}, fmt.Errorf("%s has no worker yet: delegate it", id)
	}
	if slices.Contains([]string{plan.Running, plan.Asking, plan.Review}, s.Status) {
		r.mu.Unlock()
		return nil, out{}, fmt.Errorf("%s is %s; wait for it", s.ID, s.Status)
	}
	rt, sid, title := *r.subs[s.ID], s.ID, s.Title
	r.mu.Unlock()
	h.mu.Lock()
	h.busy[rt.worker]++
	h.mu.Unlock()
	m := meta{Run: r.id, Subtask: sid, Title: title, Worktree: rt.dir, Branch: rt.branch, Session: rt.session}
	task, err := r.send(ctx, rt.worker, "↺ "+message, m)
	if err != nil {
		h.release(rt.worker)
		return nil, out{}, err
	}
	r.update(sid, func(s *plan.Subtask) { s.Status, s.Task, s.EndedAt = plan.Running, task, 0 })
	r.watchWork(sid, rt.worker, task)
	return ok("Sent to @%s, in the same session. The result arrives through wait_events.", rt.worker)
}

func (r *run) planGet(id string) *plan.Subtask {
	if r.plan == nil {
		return nil
	}
	return r.plan.Get(id)
}

func (r *run) requestReview(ctx context.Context, id string) (*mcp.CallToolResult, out, error) {
	h := r.l.h
	r.mu.Lock()
	s := r.planGet(id)
	if s == nil || s.Status != plan.Ready {
		r.mu.Unlock()
		return nil, out{}, fmt.Errorf("%s isn't finished (status %s)", id, statusOf(s))
	}
	if s.Round >= 2 {
		r.mu.Unlock()
		return nil, out{}, fmt.Errorf("%s had %d review rounds: accept it or escalate", s.ID, s.Round)
	}
	rt, sid, title, spec, acc, round := *r.rt(s.ID), s.ID, s.Title, s.Spec, s.Acceptance, s.Round
	runBranch := r.branch
	r.mu.Unlock()
	reviewer := h.handle(h.cfg.Reviewer())
	d := diff(ctx, h.cfg.Repo, runBranch, rt.branch, 24000)
	text := "Review of subtask " + sid + ": " + title + "\n\nSpec: " + spec + "\n\nAcceptance: " + acc +
		"\n\nYour directory is the subtask's worktree (branch " + rt.branch + "). Its diff against the run branch:\n\n" + d
	m := meta{Run: r.id, Subtask: sid, Title: title, Worktree: rt.dir, Branch: rt.branch, Session: fmt.Sprintf("review/%s/%s/%d", r.short, sid, round)}
	task, err := r.send(ctx, reviewer, text, m)
	if err != nil {
		return nil, out{}, err
	}
	r.update(sid, func(s *plan.Subtask) { s.Status = plan.Review })
	go func() {
		state, verdict, err := h.await(h.ctx, task)
		if err != nil {
			return
		}
		if state != a2a.TaskStateCompleted {
			r.update(sid, func(s *plan.Subtask) { s.Status = plan.Ready })
			r.push(event{Kind: "review", Subtask: sid, Text: "the review failed: " + nonEmpty(verdict, string(state)), Hint: "request_review again or accept"})
			return
		}
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(verdict)), "APPROVE") {
			approved := true
			msg, kind, land := r.mergeSub(h.ctx, sid)
			hint := map[string]string{"review": "delegate what this unblocked", "conflict": "follow_up the worker: merge the run branch into its branch and resolve"}[kind]
			if land {
				hint = "every subtask is done: wait_events for the merged event"
			}
			r.push(event{Kind: kind, Subtask: sid, From: "@" + reviewer, Text: clip(verdict, 1200) + "\n\n" + msg, Approved: &approved, Hint: hint})
			if land {
				r.land(h.ctx)
			}
			return
		}
		approved := false
		r.update(sid, func(s *plan.Subtask) { s.Status, s.Round, s.Note = plan.Changes, s.Round+1, firstLine(verdict) })
		r.push(event{Kind: "review", Subtask: sid, From: "@" + reviewer, Text: clip(verdict, 1500), Approved: &approved,
			Hint: "follow_up the worker with these points, then request_review again"})
	}()
	return ok("%s is with @%s. The verdict arrives through wait_events.", sid, reviewer)
}

func statusOf(s *plan.Subtask) string {
	if s == nil {
		return "unknown"
	}
	return s.Status
}

// mergeSub merges a subtask into the run branch. It returns a message and the
// event kind: "review" when merged, "conflict" when not. land is true when this
// merge finished the last subtask: the caller then calls r.land (after pushing its
// own event, so the lead sees the merged event last). It is true once per run.
func (r *run) mergeSub(ctx context.Context, id string) (msg, kind string, land bool) {
	r.mu.Lock()
	if err := r.openLocked(); err != nil {
		r.mu.Unlock()
		return err.Error(), "conflict", false
	}
	rt, runDir, branch := r.rt(id), r.runDir, r.branch
	title := ""
	if s := r.planGet(id); s != nil {
		title = s.Title
	}
	r.mu.Unlock()
	conflicts, err := merge(ctx, runDir, rt.branch, "halo: merge "+id+" "+title)
	if err != nil || conflicts != "" {
		why := nonEmpty(conflicts, fmt.Sprint(err))
		r.update(id, func(s *plan.Subtask) { s.Status, s.Note = plan.Conflict, "conflict: "+firstLine(why) })
		return "Merge into " + branch + " failed: " + why, "conflict", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.planGet(id); s != nil {
		s.Status, s.EndedAt = plan.Done, time.Now().UnixMilli()
		r.publishLocked()
	}
	msg = "Merged into " + branch + "."
	if r.landing == landNone && allDone(r.plan) {
		r.landing = landRunning
		msg += " Every subtask is done: halo is merging " + branch + " into " + nonEmpty(r.base, "the base branch") + "; the merged event follows."
		return msg, "review", true
	}
	return msg, "review", false
}

// allDone reports whether p is approved and every subtask in it is merged into the
// run branch.
func allDone(p *plan.Plan) bool {
	if p == nil || !p.Approved || len(p.Subtasks) == 0 {
		return false
	}
	for _, s := range p.Subtasks {
		if s.Status != plan.Done {
			return false
		}
	}
	return true
}

// canLand reports why the run branch can't be merged into base yet, or nil when
// every subtask is done and base is known.
func canLand(p *plan.Plan, base string) error {
	if p == nil || !p.Approved {
		return errors.New("the plan isn't approved")
	}
	if !allDone(p) {
		var open []string
		for _, s := range p.Subtasks {
			if s.Status != plan.Done {
				open = append(open, s.ID+" "+s.Status)
			}
		}
		return fmt.Errorf("merge only when every subtask is done (%s)", strings.Join(open, ", "))
	}
	if base == "" {
		return errors.New("there is no base branch: the repository had a detached HEAD when the plan was approved")
	}
	return nil
}

// land merges the finished run branch into base, then removes the run's worktrees
// and branches, and reports it as a merged event. On failure it keeps everything so
// that the person can merge by hand. mergeSub makes sure it runs once per run; it
// runs without r.mu, since Land and Cleanup can take a while.
func (r *run) land(ctx context.Context) {
	h := r.l.h
	r.mu.Lock()
	err := canLand(r.plan, r.base)
	branch, base := r.branch, r.base
	r.mu.Unlock()
	var summary string
	if err == nil {
		summary, err = Land(ctx, h.cfg.Repo, branch, base)
	}
	if err != nil {
		r.mu.Lock()
		r.landing = landFailed
		r.mu.Unlock()
		merged := false
		r.push(event{Kind: "merged", Text: fmt.Sprintf("halo couldn't merge %s into %s: %s", branch, nonEmpty(base, "a base branch"), err), Approved: &merged,
			Hint: fmt.Sprintf("tell @%s the run branch %s is kept; after fixing it they can run `halo merge %s`", h.st.Human, branch, r.short)})
		return
	}
	if cerr := Cleanup(ctx, h.cfg.Repo, r.short); cerr != nil {
		h.log.Warn("cleanup after merge", "run", r.id, "err", cerr)
		summary += "\n(the merge succeeded, but removing the run's worktrees didn't fully: " + cerr.Error() + ")"
	}
	r.mu.Lock()
	r.landing, r.runDir = landMerged, ""
	r.plan.MergedInto = base
	r.publishLocked()
	r.mu.Unlock()
	merged := true
	r.push(event{Kind: "merged", Text: summary, Approved: &merged,
		Hint: "every subtask is done and the run is merged into " + base + "; reply with your summary"})
}

func (r *run) accept(ctx context.Context, id string) (*mcp.CallToolResult, out, error) {
	r.mu.Lock()
	s := r.planGet(id)
	if s == nil || !slices.Contains([]string{plan.Ready, plan.Changes, plan.Conflict}, s.Status) {
		r.mu.Unlock()
		return nil, out{}, fmt.Errorf("%s can't be accepted while %s", id, statusOf(s))
	}
	sid := s.ID
	r.mu.Unlock()
	msg, _, land := r.mergeSub(ctx, sid)
	if land {
		go r.land(r.l.h.ctx)
	}
	return ok("%s", msg)
}

func (r *run) escalate(ctx context.Context, id, reason string) (*mcp.CallToolResult, out, error) {
	h := r.l.h
	r.mu.Lock()
	s := r.planGet(id)
	if s == nil || slices.Contains([]string{plan.Running, plan.Asking, plan.Review, plan.Done}, s.Status) {
		r.mu.Unlock()
		return nil, out{}, fmt.Errorf("%s can't be escalated while %s", id, statusOf(s))
	}
	models := h.cfg.WorkerModels()
	next, found := s.Model, false
	for {
		n, ok := plan.Next(next)
		if !ok {
			break
		}
		next = n
		if slices.Contains(models, n) {
			found = true
			break
		}
	}
	if !found {
		r.mu.Unlock()
		return nil, out{}, fmt.Errorf("%s is already on the strongest model in the roster (%s)", s.ID, s.Model)
	}
	prev, prevBranch, sid := s.Model, r.rt(s.ID).branch, s.ID
	s.Model, s.Escalated, s.Status = next, true, plan.Pending
	r.publishLocked()
	r.mu.Unlock()
	return r.delegate(ctx, sid, fmt.Sprintf("A %s worker tried this before you and fell short: %s. Its work is on branch %s if it helps.", prev, reason, prevBranch))
}

func (r *run) wait(ctx context.Context, secs int) (*mcp.CallToolResult, out, error) {
	if secs <= 0 {
		secs = 90
	}
	secs = max(5, min(secs, 120))
	var got []event
	select {
	case e := <-r.events:
		got = append(got, e)
	case <-time.After(time.Duration(secs) * time.Second):
	case <-ctx.Done():
		return nil, out{}, ctx.Err()
	}
	for len(got) < 20 {
		select {
		case e := <-r.events:
			got = append(got, e)
			continue
		default:
		}
		break
	}
	status := r.statusLine()
	if len(got) == 0 {
		return ok("No events yet. %s Call wait_events again.", status)
	}
	raw, _ := json.MarshalIndent(got, "", "  ")
	return ok("%s\n\n%s", raw, status)
}

func (r *run) statusLine() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return "No plan yet."
	}
	var parts []string
	for _, s := range r.plan.Subtasks {
		parts = append(parts, s.ID+" "+s.Status)
	}
	return "Status: " + strings.Join(parts, ", ") + "."
}

func (r *run) statusText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return "No plan yet."
	}
	type row struct {
		*plan.Subtask
		Branch string `json:"branch,omitempty"`
	}
	var rows []row
	for _, s := range r.plan.Subtasks {
		c := *s
		c.Spec, c.Acceptance = "", ""
		rows = append(rows, row{&c, r.rt(s.ID).branch})
	}
	raw, _ := json.MarshalIndent(map[string]any{"goal": r.plan.Goal, "approved": r.plan.Approved, "branch": r.branch, "subtasks": rows}, "", "  ")
	return string(raw)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return clip(s, 140)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
