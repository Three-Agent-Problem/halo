package host

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/three-agent-problem/lagrange/report"

	"github.com/three-agent-problem/halo/internal/plan"
)

// The run's situation report (lagrange/report) rides on the root task next to the
// plan: Lagrange shows it on the Runs page, and sends its notices and the final one
// to the person's Telegram.

// reportLocked builds the run's report now. Callers hold r.mu.
func (r *run) reportLocked() report.Report {
	rep := report.Report{Headline: "Halo run " + r.short, At: time.Now()}
	if r.plan == nil {
		rep.Status = "planning"
		return rep
	}
	p := r.plan
	rep.Headline = firstLine(nonEmpty(p.Goal, rep.Headline))
	done, rounds, escalated, failed := 0, 0, 0, 0
	for _, s := range p.Subtasks {
		rounds += s.Round
		if s.Escalated {
			escalated++
		}
		switch s.Status {
		case plan.Done:
			done++
			rep.Built = append(rep.Built, report.Item{Title: s.ID + " " + s.Title, Detail: subtaskDetail(s)})
		case plan.Failed, plan.Conflict:
			failed++
		}
	}
	rep.Progress = &report.Progress{Done: done, Total: len(p.Subtasks)}
	switch {
	case r.landing == landMerged:
		rep.Status = "landed on " + nonEmpty(p.MergedInto, r.base)
	case r.landing == landFailed:
		rep.Status = "merge failed"
	case r.landing == landRunning:
		rep.Status = "landing"
	case !p.Approved:
		rep.Status = "waiting for approval"
	case failed > 0:
		rep.Status = fmt.Sprintf("working · %d need attention", failed)
	default:
		rep.Status = "working"
	}
	if !r.started.IsZero() {
		rep.Stats = append(rep.Stats, report.Stat{Label: "Wall time", Value: shortDur(time.Since(r.started))})
	}
	rep.Stats = append(rep.Stats, report.Stat{Label: "Review rounds", Value: fmt.Sprint(rounds)})
	if escalated > 0 {
		rep.Stats = append(rep.Stats, report.Stat{Label: "Escalations", Value: fmt.Sprint(escalated)})
	}
	if r.waited > 0 {
		rep.Stats = append(rep.Stats, report.Stat{Label: "Waited on you", Value: shortDur(r.waited)})
	}
	if r.changes != "" {
		rep.Stats = append(rep.Stats, report.Stat{Label: "Changes", Value: r.changes})
	}
	return rep
}

func subtaskDetail(s *plan.Subtask) string {
	parts := []string{s.Model}
	if s.Escalated {
		parts[0] = "escalated to " + s.Model
	}
	switch s.Round {
	case 0:
	case 1:
		parts = append(parts, "1 review round")
	default:
		parts = append(parts, fmt.Sprintf("%d review rounds", s.Round))
	}
	if s.StartedAt > 0 && s.EndedAt > s.StartedAt {
		parts = append(parts, shortDur(time.Duration(s.EndedAt-s.StartedAt)*time.Millisecond))
	}
	return strings.Join(parts, " · ")
}

func shortDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// emitLocked streams an event into the root task, if the lead's turn is attached.
// Callers hold r.mu.
func (r *run) emitLocked(ev a2a.Event) {
	if r.ec == nil {
		return
	}
	select {
	case r.emit <- ev:
	default:
	}
}

// publishReportLocked publishes the report, with notice for the person who started
// the run (empty: no message). Callers hold r.mu.
func (r *run) publishReportLocked(notice string) {
	if r.ec == nil {
		return
	}
	rep := r.reportLocked()
	rep.Notice = notice
	r.emitLocked(rep.Event(r.ec))
}

// notice publishes the report with a notice.
func (r *run) notice(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishReportLocked(fmt.Sprintf(format, args...))
}

// finalReportLocked builds the report of the run as it ended and keeps it; publish
// it with sendFinal after unlocking. Callers hold r.mu.
func (r *run) finalReportLocked(outcome string) (report.Report, chan a2a.Event, a2a.Event) {
	rep := r.reportLocked()
	rep.Outcome = outcome
	rep.Notice = outcome
	r.final = &rep
	if r.ec == nil {
		return rep, nil, nil
	}
	return rep, r.emit, rep.Event(r.ec)
}

// sendFinal publishes the final report. Unlike other updates it waits for room
// (briefly): it is the one that tells the person the run is done.
func sendFinal(emit chan a2a.Event, ev a2a.Event) {
	if emit == nil || ev == nil {
		return
	}
	select {
	case emit <- ev:
	case <-time.After(5 * time.Second):
	}
}

// diffStat says how much a branch changes against base: "12 files +340 −52".
func diffStat(ctx context.Context, repo, base, branch string) string {
	if base == "" || branch == "" {
		return ""
	}
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "diff", "--shortstat", base+"..."+branch).Output()
	if err != nil {
		return ""
	}
	var files, ins, del int
	for _, f := range strings.Split(strings.TrimSpace(string(out)), ",") {
		var n int
		f = strings.TrimSpace(f)
		if _, err := fmt.Sscanf(f, "%d", &n); err != nil {
			continue
		}
		switch {
		case strings.Contains(f, "file"):
			files = n
		case strings.Contains(f, "insertion"):
			ins = n
		case strings.Contains(f, "deletion"):
			del = n
		}
	}
	if files == 0 {
		return ""
	}
	unit := "files"
	if files == 1 {
		unit = "file"
	}
	return fmt.Sprintf("%d %s +%d −%d", files, unit, ins, del)
}
