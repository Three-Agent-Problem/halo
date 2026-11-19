package host

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/three-agent-problem/lagrange/ask"

	"github.com/three-agent-problem/halo/internal/lg"
	"github.com/three-agent-problem/halo/internal/plan"
)

// Usage limits. When Claude's usage limit stops an agent's turn, the turn fails
// with Claude's words ("You've hit your 5-hour limit · resets 7:50pm
// (Europe/Rome)"). Halo doesn't fail the run: it pauses what stopped (the lead's
// turn, a subtask's worker, a review), asks the person whether to wait for the
// reset or continue now (after logging in to another account), and then
// restarts the agents' processes, so a new login takes effect, and continues
// each paused piece in its own Claude session and worktree. Nobody answering
// means waiting: the run continues on its own when the limit resets.

// limitPrefixes start Claude Code's usage-limit messages (claude-agent-sdk's
// USAGE_LIMIT_ERROR_PREFIXES, the ones a login to another account can fix).
var limitPrefixes = []string{
	"You've hit your", "You've reached your", "You're out of usage credits", "You're out of extra usage",
	"Your org is out of usage",
}

// limitHit is a usage limit an agent hit.
type limitHit struct {
	Text   string    // Claude's words
	Resets time.Time // when the limit resets; zero when unknown
}

// unknownWait is how long a pause waits when Claude doesn't say when the limit
// resets, and rateWait when it was a plain rate limit (Claude Code already
// retried before failing the turn).
const (
	unknownWait = 30 * time.Minute
	rateWait    = 5 * time.Minute
)

// usageLimit reports whether a failed turn's text is a usage limit.
func usageLimit(text string, now time.Time) (limitHit, bool) {
	for _, p := range limitPrefixes {
		i := strings.Index(text, p)
		if i < 0 {
			continue
		}
		msg := text[i:]
		// claude-agent-acp's JSON-RPC error, as a2aacp prints it: "<msg> (code -32603): {...}".
		if j := strings.Index(msg, " (code "); j >= 0 {
			msg = msg[:j]
		}
		msg = strings.TrimSpace(firstLine(msg))
		return limitHit{Text: msg, Resets: resetTime(msg, now)}, true
	}
	if strings.Contains(text, `"errorKind":"rate_limit"`) {
		return limitHit{Text: "Claude is rate limited", Resets: now.Add(rateWait)}, true
	}
	return limitHit{}, false
}

var resetsRe = regexp.MustCompile(`resets (?:at |on )?([^()·]+?)\s*(?:\(([^)]+)\))?\s*$`)

// resetTime reads when the limit resets from "… · resets 7:50pm (Europe/Rome)"
// or "… · resets Nov 21, 8am (Europe/Rome)"; zero when it can't.
func resetTime(msg string, now time.Time) time.Time {
	m := resetsRe.FindStringSubmatch(msg)
	if m == nil {
		return time.Time{}
	}
	loc := now.Location()
	if m[2] != "" {
		if l, err := time.LoadLocation(strings.TrimSpace(m[2])); err == nil {
			loc = l
		}
	}
	now = now.In(loc)
	// Month names parse in any case; am/pm only in lower case, as Claude writes them.
	when := strings.Replace(strings.TrimSpace(m[1]), " at ", ", ", 1)
	when = strings.NewReplacer("AM", "am", "PM", "pm").Replace(when)
	for _, layout := range []string{"3:04pm", "3pm", "15:04"} {
		if t, err := time.ParseInLocation(layout, when, loc); err == nil {
			r := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if !r.After(now) {
				r = r.AddDate(0, 0, 1)
			}
			return r
		}
	}
	for _, layout := range []string{"Jan 2, 3:04pm", "Jan 2, 3pm", "Jan 2 3:04pm", "Jan 2 3pm", "Jan 2, 15:04", "Jan 2"} {
		if t, err := time.ParseInLocation(layout, when, loc); err == nil {
			r := time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if r.Before(now.AddDate(0, 0, -1)) {
				r = r.AddDate(1, 0, 0)
			}
			return r
		}
	}
	return time.Time{}
}

// What the person decides about a pause.
const (
	resumeNow  = "now"  // continue at once: they logged in to another account, or the limit is gone
	resumeWait = "wait" // continue when the limit resets
	resumeStop = "stop" // give up: what paused fails, as before
)

// limiter is the usage limit the team is paused on. The limit is the account's,
// so it pauses every run on this machine at once.
type limiter struct {
	mu     sync.Mutex
	on     bool
	hit    limitHit
	since  time.Time
	runs   []*run
	decide chan string // the person's decision from the TUI (the lead's "resume" message)
}

// hitLimit pauses the team on hit, for run r. The first hit asks the person; later
// ones join the pause.
func (h *Host) hitLimit(r *run, hit limitHit) {
	h.limit.mu.Lock()
	if !slices.Contains(h.limit.runs, r) {
		h.limit.runs = append(h.limit.runs, r)
	}
	if h.limit.on {
		if hit.Resets.After(h.limit.hit.Resets) {
			h.limit.hit = hit
		}
		hit, since := h.limit.hit, h.limit.since
		h.limit.mu.Unlock()
		r.setPause(hit, since)
		return
	}
	now := time.Now()
	dec := make(chan string, 1)
	h.limit.on, h.limit.hit, h.limit.since, h.limit.decide = true, hit, now, dec
	h.limit.mu.Unlock()
	h.log.Info("usage limit: pausing", "run", r.id, "limit", hit.Text, "resets", hit.Resets)
	r.setPause(hit, now)
	go h.sitOut(r, dec)
}

// decideLimit passes the person's decision to the pause; false when there is none.
func (h *Host) decideLimit(mode string) bool {
	h.limit.mu.Lock()
	defer h.limit.mu.Unlock()
	if !h.limit.on {
		return false
	}
	select {
	case h.limit.decide <- mode:
	default:
	}
	return true
}

// limitDeadline is when a pause continues on its own.
func (h *Host) limitDeadline() time.Time {
	h.limit.mu.Lock()
	defer h.limit.mu.Unlock()
	if !h.limit.hit.Resets.IsZero() {
		return h.limit.hit.Resets.Add(time.Minute)
	}
	return h.limit.since.Add(unknownWait)
}

// sitOut asks the person what to do about the pause and waits for their answer,
// or for the limit to reset, then ends the pause.
func (h *Host) sitOut(r *run, dec chan string) {
	ctx := h.ctx
	h.limit.mu.Lock()
	hit := h.limit.hit
	h.limit.mu.Unlock()
	askf := h.limitAsk
	if askf == nil {
		askf = h.askLimit
	}
	qid, err := askf(ctx, r, hit)
	if err != nil {
		h.log.Warn("usage limit: asking the person failed", "error", err)
	}
	mode := ""
	for mode == "" {
		select {
		case <-ctx.Done():
			return
		case m := <-dec:
			if m != resumeWait {
				mode = m
				continue
			}
		case <-time.After(3 * time.Second):
		}
		if qid != "" {
			if t, err := h.api.Task(ctx, qid); err == nil && a2a.TaskState(t.State).Terminal() {
				qid = ""
				if m := limitChoice(a2a.TaskState(t.State), lg.TaskText(t.Task)); m != resumeWait {
					mode = m
					continue
				}
				r.notice("Paused on Claude's usage limit: continues on its own when it resets")
			}
		}
		if time.Now().After(h.limitDeadline()) {
			mode = resumeWait
		}
	}
	if qid != "" {
		// Answered elsewhere (the TUI's /continue) or timed out: the question is moot.
		_ = h.api.Do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(qid)+"/cancel", nil, nil)
	}
	h.endPause(mode)
}

// askLimit asks the person whether to wait or continue now, inside run r.
func (h *Host) askLimit(ctx context.Context, r *run, hit limitHit) (string, error) {
	when := "Claude didn't say when it resets; Halo tries again in 30 minutes"
	if !hit.Resets.IsZero() {
		when = "it resets at " + hit.Resets.Local().Format("15:04 Mon")
	}
	qs := []ask.Question{{Header: "Limit", Question: "Claude's usage limit paused the team (" + hit.Text + "). What now?", Options: []ask.Option{
		{Label: "Wait for the reset (Recommended)", Description: "the run continues on its own when the limit resets; " + when},
		{Label: "Continue now", Description: "you logged in to another Claude account (/login in Halo, or claude auth login): Halo restarts the agents and they carry on"},
		{Label: "Stop the run", Description: "what paused fails, and the lead asks you what next once Claude works again"},
	}}}
	text := "⏸ " + hit.Text + "\n\nThe team is paused, not failed: every agent keeps its session and worktree. " +
		"Nobody answering means waiting: " + when + ".\n\n" + ask.Text(qs)
	res, err := h.caller(h.lead.handle).Send(ctx, lg.Call{To: h.st.Human, Parent: r.id, Text: text, Immediate: true,
		Meta: map[string]any{ask.MetaKey: qs}})
	if err != nil {
		return "", err
	}
	if t, ok := res.(*a2a.Task); ok && !t.Status.State.Terminal() {
		return string(t.ID), nil
	}
	return "", nil
}

// limitChoice reads the person's answer to askLimit.
func limitChoice(state a2a.TaskState, text string) string {
	if state != a2a.TaskStateCompleted {
		return resumeWait
	}
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "continue now") || strings.Contains(t, "switched") || strings.Contains(t, "logged in"):
		return resumeNow
	case strings.Contains(t, "stop"):
		return resumeStop
	}
	return resumeWait
}

// endPause ends the pause: unless it stops, the agents restart (so a new login
// takes effect) and every paused piece of every paused run continues.
func (h *Host) endPause(mode string) {
	h.limit.mu.Lock()
	runs, hit := h.limit.runs, h.limit.hit
	h.limit.on, h.limit.runs, h.limit.decide = false, nil, nil
	h.limit.mu.Unlock()
	h.log.Info("usage limit: pause ends", "mode", mode, "runs", len(runs))
	if mode != resumeStop {
		h.restartAgents()
	}
	for _, r := range runs {
		r.unpause(h.ctx, mode, hit)
	}
}

// restartAgents restarts every agent's process; tasks in progress finish first.
func (h *Host) restartAgents() {
	var all []*procs
	if h.lead != nil && h.lead.exec != nil {
		all = append(all, h.lead.exec)
	}
	h.mu.Lock()
	for _, w := range h.workers {
		if w.exec != nil {
			all = append(all, w.exec)
		}
	}
	h.mu.Unlock()
	for _, p := range all {
		if err := p.Restart(); err != nil {
			h.log.Warn("restarting an agent failed", "error", err)
			h.notify(Event{Kind: "error", Text: "restarting an agent after the usage limit failed: " + err.Error()})
		}
	}
}

// pauseSub pauses subtask id: what is "work" (its worker's turn) or "review".
func (r *run) pauseSub(id, what string, hit limitHit) {
	r.mu.Lock()
	if r.paused == nil {
		r.paused = map[string]string{}
	}
	r.paused[id] = what
	r.mu.Unlock()
	r.update(id, func(s *plan.Subtask) { s.Status, s.Note = plan.Paused, hit.Text })
	r.l.h.hitLimit(r, hit)
}

// setPause shows the pause on the plan and in the run's report.
func (r *run) setPause(hit limitHit, since time.Time) {
	p := &plan.Pause{Reason: hit.Text, Since: since.UnixMilli()}
	if !hit.Resets.IsZero() {
		p.Resets = hit.Resets.UnixMilli()
	}
	r.mu.Lock()
	if r.plan != nil {
		r.plan.Paused = p
		r.publishLocked()
	}
	r.publishReportLocked("⏸ Paused: " + hit.Text)
	r.mu.Unlock()
}

// unpause continues (or, for resumeStop, fails) what the pause stopped in r.
func (r *run) unpause(ctx context.Context, mode string, hit limitHit) {
	r.mu.Lock()
	subs, wait := r.paused, r.leadWait
	r.paused, r.leadWait = nil, nil
	if r.plan != nil {
		r.plan.Paused = nil
		r.publishLocked()
	}
	r.mu.Unlock()
	ids := make([]string, 0, len(subs))
	for id := range subs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	failSub := func(id, why string) {
		r.update(id, func(s *plan.Subtask) { s.Status, s.Note = plan.Failed, why })
		r.push(event{Kind: "subtask_failed", Subtask: id, Text: why, Hint: "ask_human what to do with it"})
	}
	var went []string
	for _, id := range ids {
		r.mu.Lock()
		st := statusOf(r.planGet(id))
		r.mu.Unlock()
		if st != plan.Paused {
			continue // the lead moved it on meanwhile
		}
		if mode == resumeStop {
			failSub(id, "stopped by the person after Claude's usage limit: "+hit.Text)
			continue
		}
		var err error
		if subs[id] == "review" {
			r.update(id, func(s *plan.Subtask) { s.Status = plan.Ready })
			_, _, err = r.requestReview(ctx, id)
		} else {
			err = r.l.relayTo(ctx, r, id, resumeWorker)
		}
		if err != nil {
			failSub(id, "couldn't continue after the usage limit: "+err.Error())
			continue
		}
		went = append(went, id)
	}
	switch {
	case wait != nil:
		wait <- resumption{mode: mode, hit: hit, ids: went}
	case len(went) > 0:
		r.push(event{Kind: "resumed", Text: "Claude's usage limit paused " + strings.Join(went, ", ") + "; they continue now in their own sessions.",
			Hint: "wait_events for their results"})
	}
	if mode == resumeStop {
		r.notice("Stopped after Claude's usage limit")
	} else {
		r.notice("▶ Continuing after Claude's usage limit")
	}
}

// resumption is how a pause ended, for the paused lead.
type resumption struct {
	mode string
	hit  limitHit
	ids  []string // the subtasks that continued
}

// resumeWorker is what a paused worker hears when the run continues.
const resumeWorker = "Claude's usage limit stopped your last turn mid-way; it is lifted now. " +
	"Continue the subtask where you left off (your worktree still has your changes), then finish and summarize as before."

// resumeLead is what the paused lead hears when the run continues.
func resumeLead(mode string, hit limitHit, ids []string, status string) string {
	why := "the limit reset"
	if mode == resumeNow {
		why = "the person continued it (they may have logged in to another Claude account)"
	}
	b := fmt.Sprintf("Halo paused this run when Claude's usage limit stopped your last turn (%q). It continues now: %s. ", hit.Text, why)
	b += "Pick up where you left off; the run's state is unchanged. "
	if len(ids) > 0 {
		b += "Halo already restarted the paused subtasks (" + strings.Join(ids, ", ") + ") in their workers' own sessions and worktrees; their results arrive through wait_events. "
	}
	return b + "\n\nWhere the run stands:\n" + status
}

// continued is ec with a new message for the next turn of the same task.
func continued(ec *a2asrv.ExecutorContext, text string) *a2asrv.ExecutorContext {
	c := *ec
	c.Message = a2a.NewMessageForTask(a2a.MessageRoleUser, ec, a2a.NewTextPart(text))
	c.StoredTask = &a2a.Task{ID: ec.TaskID, ContextID: ec.ContextID, Status: a2a.TaskStatus{State: a2a.TaskStateWorking}}
	return &c
}

// failure returns the text of a failed final event.
func failure(ev a2a.Event) (string, bool) {
	switch e := ev.(type) {
	case *a2a.TaskStatusUpdateEvent:
		if e.Status.State == a2a.TaskStateFailed {
			return lg.MessageText(e.Status.Message), true
		}
	case *a2a.Task:
		if e.Status.State == a2a.TaskStateFailed {
			return lg.MessageText(e.Status.Message), true
		}
	}
	return "", false
}

// resume handles the person's decision about a pause from the TUI (/continue, or
// /login, which logs in to another account and continues): the message text is
// "now", "wait" or "stop". It never starts a Claude turn.
func (l *lead) resume(ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		mode := strings.ToLower(strings.TrimSpace(textOf(ec.Message)))
		if !slices.Contains([]string{resumeNow, resumeWait, resumeStop}, mode) {
			mode = resumeNow
		}
		state, text := a2a.TaskStateCompleted, map[string]string{
			resumeNow:  "Continuing now: Halo restarts the agents, so they use the Claude login you have now.",
			resumeWait: "The team waits for Claude's usage limit to reset, then continues.",
			resumeStop: "Stopped: what paused fails, and the lead asks you what next.",
		}[mode]
		if !l.h.decideLimit(mode) {
			state, text = a2a.TaskStateFailed, "Nothing is paused on a usage limit."
		}
		msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(text))
		yield(a2a.NewStatusUpdateEvent(ec, state, msg), nil)
	}
}
