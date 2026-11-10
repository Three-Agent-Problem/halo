package host

import (
	"context"
	"encoding/json"
	"io"
	"iter"
	"os"
	"path/filepath"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"
	"github.com/three-agent-problem/lagrange/code"
	"github.com/three-agent-problem/lagrange/delegate"

	"github.com/three-agent-problem/halo/internal/config"
)

// worker is a Claude Code agent (a worker or the reviewer) served over A2A.
type worker struct {
	h      *Host
	agent  config.Agent
	handle string
	exec   *a2aacp.Executor

	mu       sync.Mutex
	keys     map[string]string   // A2A context -> session key
	work     map[string]*binding // session key -> what it works on
	sessions map[string]a2aacp.Session
	prefaced map[string]bool
}

// binding is what a Claude session is working on now.
type binding struct {
	meta     meta
	platform string // the platform task of the current message, for ask_lead
}

func (h *Host) newWorker(ctx context.Context, a config.Agent, logw io.Writer) (*worker, error) {
	w := &worker{h: h, agent: a, handle: h.handle(a), keys: map[string]string{}, work: map[string]*binding{},
		sessions: map[string]a2aacp.Session{}, prefaced: map[string]bool{}}
	rules := a2aacp.ToolRules{Allow: []string{"*"}}
	if a.Role == config.RoleReviewer {
		rules = a2aacp.ToolRules{Allow: []string{"*"}, Deny: []string{"edit", "delete", "move"}}
	}
	exec, err := a2aacp.NewExecutor(ctx, h.agent(w.handle, logw), &a2aacp.Config{
		Converter:  h.tap(w.handle),
		AutoAnswer: rules,
		Settings: func(context.Context, *a2asrv.ExecutorContext) (map[string]any, error) {
			return map[string]any{"model": a.Model}, nil
		},
		Session:      w.session,
		SessionStore: (*workerSessions)(w),
		Prompt:       w.prompt,
	})
	if err != nil {
		return nil, err
	}
	w.exec = exec
	return w, nil
}

// Execute implements a2asrv.AgentExecutor: it binds the context to its Claude
// session, runs the turn, and commits the worktree when the turn completes.
func (w *worker) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	m := metaOf(ec.Message)
	key := m.Session
	if key == "" {
		key = ec.ContextID
	}
	w.mu.Lock()
	w.keys[ec.ContextID] = key
	w.work[key] = &binding{meta: m, platform: delegate.Task(ctx)}
	w.mu.Unlock()
	inner := w.exec.Execute(ctx, ec)
	return func(yield func(a2a.Event, error) bool) {
		for ev, err := range inner {
			if err == nil && completed(ev) && m.Worktree != "" && w.agent.Role == config.RoleWorker {
				stat, cerr := commitAll(ctx, m.Worktree, "halo: "+m.Subtask+" "+m.Title)
				w.h.log.Info("commit", "agent", w.handle, "subtask", m.Subtask, "stat", stat, "error", cerr)
				if cerr == nil && stat != "" {
					// The commit goes to Lagrange as an artifact of the task, so every
					// client sees it (and its diff), not only this machine.
					if c, patch, gerr := code.FromGit(ctx, m.Worktree, "HEAD"); gerr == nil {
						c.Branch = m.Branch
						if !yield(code.Event(ec, c, patch), nil) {
							return
						}
					}
				}
			}
			if !yield(ev, err) {
				return
			}
		}
	}
}

// Cancel implements a2asrv.AgentExecutor.
func (w *worker) Cancel(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return w.exec.Cancel(ctx, ec)
}

func completed(ev a2a.Event) bool {
	switch e := ev.(type) {
	case *a2a.TaskStatusUpdateEvent:
		return e.Status.State == a2a.TaskStateCompleted
	case *a2a.Task:
		return e.Status.State == a2a.TaskStateCompleted
	}
	return false
}

func (w *worker) keyOf(contextID string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if k, ok := w.keys[contextID]; ok {
		return k
	}
	return contextID
}

// session puts a new Claude session in the subtask's worktree and gives it the halo
// MCP server.
func (w *worker) session(_ context.Context, ec *a2asrv.ExecutorContext) (a2aacp.SessionParams, error) {
	key := w.keyOf(ec.ContextID)
	m := metaOf(ec.Message)
	cwd := m.Worktree
	if cwd == "" {
		// Called outside a run (say, from the Lagrange web app): a scratch directory.
		cwd = filepath.Join(w.h.wt.root, "scratch", w.agent.Name)
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			return a2aacp.SessionParams{}, err
		}
	}
	srv, _ := json.Marshal(map[string]any{"type": "http", "name": "halo", "url": w.h.mcpURL + "/worker",
		"headers": []map[string]string{{"name": "Halo-Agent", "value": w.handle}, {"name": "Halo-Session", "value": key}}})
	return a2aacp.SessionParams{Cwd: cwd, MCPServers: []json.RawMessage{srv}}, nil
}

// prompt prepends the role to the first message of each session, and to every review.
func (w *worker) prompt(_ context.Context, ec *a2asrv.ExecutorContext) ([]*a2a.Part, error) {
	key := w.keyOf(ec.ContextID)
	m := metaOf(ec.Message)
	w.mu.Lock()
	first := !w.prefaced[key]
	w.prefaced[key] = true
	w.mu.Unlock()
	parts := ec.Message.Parts
	var pre string
	switch {
	case w.agent.Role == config.RoleReviewer:
		pre = reviewerPrompt(w.handle)
	case first:
		pre = workerPrompt(w.handle, w.agent.Model, w.h.cfg.Handle(w.h.cfg.Lead().Name), nonEmpty(m.Branch, "(none)"))
	default:
		return parts, nil
	}
	return append([]*a2a.Part{a2a.NewTextPart(pre)}, parts...), nil
}

// workerSessions is the a2aacp.SessionStore of a worker, keyed by Halo's session key
// instead of the A2A context: a follow-up arrives as a new task in a new context, but
// continues the same Claude session, which remembers the subtask.
type workerSessions worker

func (s *workerSessions) Get(_ context.Context, contextID string) (a2aacp.Session, bool, error) {
	w := (*worker)(s)
	key := w.keyOf(contextID)
	w.mu.Lock()
	defer w.mu.Unlock()
	if sess, ok := w.sessions[key]; ok {
		return sess, true, nil
	}
	return w.h.sessions.get(w.handle + " " + key)
}

func (s *workerSessions) Put(_ context.Context, contextID string, sess a2aacp.Session) error {
	w := (*worker)(s)
	key := w.keyOf(contextID)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sessions[key] = sess
	return w.h.sessions.put(w.handle+" "+key, sess)
}

func nonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
