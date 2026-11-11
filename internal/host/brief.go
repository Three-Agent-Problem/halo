package host

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/three-agent-problem/halo/internal/plan"
)

// Sizes of the team context a worker gets with a subtask.
const (
	maxBrief   = 3000 // the whole brief
	maxSummary = 600  // one teammate's summary, as kept on the subtask
)

// brief is the team context a worker gets with subtask self: the run's goal, every
// subtask with its state, and what finished teammates reported, newest first. When
// it runs over maxBrief the oldest summaries go first. It lets a worker skip what a
// teammate already found, and tells it whom to ask_peer. Callers hold r.mu.
func brief(p *plan.Plan, subs map[string]*subRT, self string, know map[string]subKnow, now time.Time) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Team context (the run's plan, and what each teammate knows; ask_peer a teammate instead of exploring what it covered, even while it works):\nGoal: %s\n", p.Goal)
	for _, s := range p.Subtasks {
		who := ""
		if s.Worker != "" {
			who = " @" + s.Worker
		}
		mark := ""
		if s.ID == self {
			mark = " (you)"
		}
		fmt.Fprintf(&b, "- %s %s: %s%s%s\n", s.ID, s.Title, s.Status, who, mark)
		if k, ok := know[s.ID]; ok && s.ID != self {
			fmt.Fprintf(&b, "  knows: %s\n", knowLine(k, now))
		}
	}
	head := b.String()

	type done struct {
		id, text string
		ended    int64
	}
	var ds []done
	for _, s := range p.Subtasks {
		rt := subs[s.ID]
		if s.ID == self || rt == nil || rt.summary == "" {
			continue
		}
		ds = append(ds, done{s.ID, rt.summary, s.EndedAt})
	}
	slices.SortStableFunc(ds, func(a, b done) int { return int(b.ended - a.ended) })
	out := head
	for i, d := range ds {
		if i == 0 {
			out += "What finished teammates reported:\n"
		}
		line := fmt.Sprintf("- %s: %s\n", d.id, oneLine(d.text))
		if len(out)+len(line) > maxBrief {
			break
		}
		out += line
	}
	return strings.TrimRight(out, "\n")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
