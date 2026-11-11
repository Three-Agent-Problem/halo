package host

import (
	"fmt"
	"slices"
	"strings"

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
func brief(p *plan.Plan, subs map[string]*subRT, self string) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Team context (the run's plan; ask_peer a finished subtask's worker instead of re-reading what it covered):\nGoal: %s\n", p.Goal)
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

// peerTarget checks that the worker of subtask target can answer a teammate now, and
// returns its runtime: it must have a session (it worked on the subtask) and be idle
// on it, since a Claude session takes one prompt at a time.
func (r *run) peerTarget(from, target string) (subRT, error) {
	target = strings.ToUpper(strings.TrimSpace(target))
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.planGet(target)
	switch {
	case s == nil:
		return subRT{}, fmt.Errorf("there is no subtask %s in this run", target)
	case target == strings.ToUpper(from):
		return subRT{}, fmt.Errorf("%s is your own subtask", target)
	}
	rt := r.subs[target]
	if rt == nil || rt.session == "" || rt.worker == "" {
		return subRT{}, fmt.Errorf("%s hasn't been worked on yet, so nobody knows it: read the code yourself or ask_lead", target)
	}
	if slices.Contains([]string{plan.Running, plan.Asking, plan.Review}, s.Status) {
		return subRT{}, fmt.Errorf("@%s is busy with %s (%s): read it yourself, ask_lead, or try again later", rt.worker, target, s.Status)
	}
	return *rt, nil
}

// peerText is the message a worker gets when a teammate asks it about its subtask.
func peerText(from, fromSubtask, target, question string) string {
	return fmt.Sprintf("Question from @%s (working on %s) about your subtask %s:\n\n%s\n\n"+
		"Answer from what you already know about the code you worked on, in at most 8 lines: file paths, function names, gotchas. "+
		"Don't edit files and don't start new work. If you don't know, say so.", from, fromSubtask, target, question)
}
