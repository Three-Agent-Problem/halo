package host

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"sort"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/halo/internal/plan"
)

// humanMessage routes a message the person sent an agent during a run. It never
// starts a run or a Claude turn of the lead: the task completes at once with an
// acknowledgement, or fails with why the message couldn't be delivered.
func (l *lead) humanMessage(ctx context.Context, ec *a2asrv.ExecutorContext, m meta) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		state, text := a2a.TaskStateCompleted, ""
		ack, err := l.route(ctx, m, textOf(ec.Message))
		if err != nil {
			state, text = a2a.TaskStateFailed, err.Error()
		} else {
			text = ack
		}
		msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(text))
		yield(a2a.NewStatusUpdateEvent(ec, state, msg), nil)
	}
}

// route delivers a human message and returns the acknowledgement.
//
//   - To the lead (to is the lead or empty): a human_message event for the newest
//     active run.
//   - To a worker (by subtask, or the worker's current subtask when subtask is
//     empty), direct: the text goes to the worker in the subtask's session and
//     worktree, like follow_up, and the lead gets an informational event. If the
//     worker is busy with the subtask, the lead gets it to relay instead.
//   - To a worker, not direct: a human_message event asks the lead to relay it.
func (l *lead) route(ctx context.Context, m meta, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("the message is empty")
	}
	human := "@" + strings.TrimPrefix(nonEmpty(m.From, l.h.st.Human, "human"), "@")
	to := strings.TrimPrefix(strings.TrimSpace(m.To), "@")
	sid := strings.ToUpper(strings.TrimSpace(m.Subtask))
	toLead := to == "" || to == l.handle

	runs := l.active()
	if len(runs) == 0 {
		return "", errors.New("there is no active run to message: start one with a prompt to the lead")
	}
	var r *run
	var worker string
	switch {
	case sid != "":
		for _, c := range runs {
			if c.hasSubtask(sid) {
				r = c
				break
			}
		}
		if r == nil {
			return "", fmt.Errorf("there is no subtask %s in the active run", sid)
		}
		worker = r.workerOf(sid)
		if !toLead {
			if worker == "" {
				return "", fmt.Errorf("%s isn't delegated yet, so there is no worker to message; message the lead instead", sid)
			}
			if worker != to {
				return "", fmt.Errorf("%s is with @%s, not @%s", sid, worker, to)
			}
		}
	case toLead:
		r = runs[0]
	default:
		for _, c := range runs {
			if id := c.currentOf(to); id != "" {
				r, sid, worker = c, id, to
				break
			}
		}
		if r == nil {
			return "", fmt.Errorf("@%s has no subtask in the active run", to)
		}
	}

	e := event{Kind: "human_message", Subtask: sid, From: human, Text: text}
	if !toLead {
		e.To = "@" + worker
	}
	switch {
	case toLead && sid == "":
		e.Hint = "act on it"
		r.push(e)
		return fmt.Sprintf("Sent to the lead, @%s.", l.handle), nil
	case toLead || !m.Direct:
		e.Hint = "relay it to the worker with follow_up (or act on it)"
		r.push(e)
		return fmt.Sprintf("Sent to the lead, @%s, to relay to %s.", l.handle, sid), nil
	}

	if busy, status := r.busy(sid); busy {
		e.Hint = fmt.Sprintf("%s meant this for @%s, who is busy (%s %s): relay it with follow_up when it is free (or act on it)", human, worker, sid, status)
		r.push(e)
		return fmt.Sprintf("@%s is busy with %s (%s), so the lead got it to relay when it's free.", worker, sid, status), nil
	}
	if err := l.relayTo(ctx, r, sid, "Message from "+human+": "+text); err != nil {
		return "", err
	}
	e.Hint = fmt.Sprintf("%s sent this straight to @%s, which is working on it in the same session; its result arrives through wait_events. Nothing to relay", human, worker)
	r.push(e)
	return fmt.Sprintf("Sent to @%s (%s), in the same session.", worker, sid), nil
}

// relayTo sends text to the worker of subtask id, in its session and worktree.
func (l *lead) relayTo(ctx context.Context, r *run, id, text string) error {
	if l.relay != nil {
		return l.relay(ctx, r, id, text)
	}
	r.mu.Lock()
	err := r.openLocked()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	_, _, err = r.followUp(ctx, id, text)
	return err
}

// active lists the runs the lead is working on now (its Claude turn is attached and
// the run isn't merged yet), newest first.
func (l *lead) active() []*run {
	l.mu.Lock()
	all := make([]*run, 0, len(l.runs))
	for _, r := range l.runs {
		all = append(all, r)
	}
	l.mu.Unlock()
	var out []*run
	for _, r := range all {
		r.mu.Lock()
		ok := r.ec != nil && r.landing != landMerged
		r.mu.Unlock()
		if ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq > out[j].seq })
	return out
}

func (r *run) hasSubtask(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.planGet(id) != nil
}

// workerOf returns the handle of the worker of subtask id's current attempt, or ""
// before it is delegated.
func (r *run) workerOf(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rt := r.subs[strings.ToUpper(id)]; rt != nil {
		return rt.worker
	}
	return ""
}

// currentOf returns the subtask worker works on in this run: the latest started
// one that isn't merged yet, or "".
func (r *run) currentOf(worker string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	best, started := "", int64(-1)
	for id, rt := range r.subs {
		s := r.planGet(id)
		if rt.worker != worker || s == nil || s.Status == plan.Done {
			continue
		}
		if s.StartedAt > started {
			best, started = s.ID, s.StartedAt
		}
	}
	return best
}

// busy reports whether subtask id's worker (or the reviewer) has it now, so that
// a follow-up can't be sent yet.
func (r *run) busy(id string) (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.planGet(id)
	if s == nil {
		return false, ""
	}
	return slices.Contains([]string{plan.Running, plan.Asking, plan.Review}, s.Status), s.Status
}
