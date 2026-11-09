package host

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/halo/internal/plan"
)

// stopSubtask stops the worker of a subtask at the person's request: its task is
// canceled (which cancels the Claude turn), the subtask fails with "stopped by",
// and the lead hears it was on purpose. Like humanMessage, it never starts a
// Claude turn of the lead.
func (l *lead) stopSubtask(ctx context.Context, ec *a2asrv.ExecutorContext, m meta) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		state, text := a2a.TaskStateCompleted, ""
		ack, err := l.stop(ctx, m)
		if err != nil {
			state, text = a2a.TaskStateFailed, err.Error()
		} else {
			text = ack
		}
		msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(text))
		yield(a2a.NewStatusUpdateEvent(ec, state, msg), nil)
	}
}

func (l *lead) stop(ctx context.Context, m meta) (string, error) {
	sid := strings.ToUpper(strings.TrimSpace(m.Subtask))
	if sid == "" {
		return "", errors.New("which subtask? none was given")
	}
	human := "@" + strings.TrimPrefix(nonEmpty(m.From, l.h.st.Human, "human"), "@")
	var r *run
	for _, c := range l.active() {
		if c.hasSubtask(sid) {
			r = c
			break
		}
	}
	if r == nil {
		return "", fmt.Errorf("there is no subtask %s in the active run", sid)
	}
	r.mu.Lock()
	s := r.planGet(sid)
	rt := r.subs[sid]
	if s == nil || rt == nil || rt.worker == "" || s.Task == "" {
		r.mu.Unlock()
		return "", fmt.Errorf("%s isn't delegated, so no worker runs it", sid)
	}
	if s.Status != plan.Running && s.Status != plan.Asking {
		st := s.Status
		r.mu.Unlock()
		return "", fmt.Errorf("%s isn't running (it is %s)", sid, st)
	}
	worker, task := rt.worker, s.Task
	rt.stoppedBy = human
	r.mu.Unlock()

	cancel := l.cancel
	if cancel == nil {
		cancel = func(ctx context.Context, worker, task string) error {
			return l.h.caller(l.handle).Cancel(ctx, worker, task)
		}
	}
	if err := cancel(ctx, worker, task); err != nil {
		r.mu.Lock()
		rt.stoppedBy = ""
		r.mu.Unlock()
		return "", err
	}
	return fmt.Sprintf("Stopping @%s on %s; the lead will ask you what next.", worker, sid), nil
}
