package host

import (
	"context"
	"errors"
	"iter"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// agentExec is what runs an agent's turns: an a2aacp.Executor, or a fake in tests.
type agentExec interface {
	a2asrv.AgentExecutor
	Shutdown(ctx context.Context) error
	Close() error
}

// procs is an agent's executor, which Halo can replace to restart the agent's
// process. A running claude keeps the login it started with, so after a usage
// limit Halo restarts the agents: a login to another Claude account then takes
// effect, and the sessions are loaded again (session/load). Tasks in progress
// finish on the old executor, which then stops.
type procs struct {
	start func() (agentExec, error)

	mu   sync.Mutex
	cur  agentExec
	of   map[a2a.TaskID]agentExec // tasks not finished yet, on the executor they started on
	old  []agentExec
	idle time.Duration // how long a replaced executor may finish its tasks
}

func newProcs(start func() (agentExec, error)) (*procs, error) {
	e, err := start()
	if err != nil {
		return nil, err
	}
	return &procs{start: start, cur: e, of: map[a2a.TaskID]agentExec{}, idle: 3 * time.Hour}, nil
}

// Execute runs the task on the executor it started on, or the current one.
func (p *procs) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	p.mu.Lock()
	e := p.of[ec.TaskID]
	if e == nil {
		e = p.cur
		p.of[ec.TaskID] = e
	}
	p.mu.Unlock()
	inner := e.Execute(ctx, ec)
	return func(yield func(a2a.Event, error) bool) {
		ended := false
		defer func() {
			if ended {
				p.mu.Lock()
				delete(p.of, ec.TaskID)
				p.mu.Unlock()
			}
		}()
		for ev, err := range inner {
			ended = ended || terminalEvent(ev)
			if !yield(ev, err) {
				return
			}
		}
	}
}

// Cancel cancels the task on the executor it runs on.
func (p *procs) Cancel(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	p.mu.Lock()
	e := p.of[ec.TaskID]
	if e == nil {
		e = p.cur
	}
	p.mu.Unlock()
	return e.Cancel(ctx, ec)
}

// Restart starts a new executor for new tasks and shuts the old one down once
// its tasks end.
func (p *procs) Restart() error {
	n, err := p.start()
	if err != nil {
		return err
	}
	p.mu.Lock()
	old := p.cur
	p.cur = n
	p.old = append(p.old, old)
	idle := p.idle
	p.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), idle)
		defer cancel()
		_ = old.Shutdown(ctx)
	}()
	return nil
}

// Close stops every executor.
func (p *procs) Close() error {
	p.mu.Lock()
	all := append([]agentExec{p.cur}, p.old...)
	p.mu.Unlock()
	var errs []error
	for _, e := range all {
		errs = append(errs, e.Close())
	}
	return errors.Join(errs...)
}

func terminalEvent(ev a2a.Event) bool {
	switch e := ev.(type) {
	case *a2a.TaskStatusUpdateEvent:
		return e.Status.State.Terminal()
	case *a2a.Task:
		return e.Status.State.Terminal()
	}
	return false
}
