// Package plan is the lead's plan: a DAG of subtasks, each for one model. The lead
// keeps the full plan; it publishes a compact copy (without specs) as the halo.plan
// artifact on the run's root task, which is how the TUI sees it.
package plan

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ArtifactName names the artifact that carries the plan on the root task.
const ArtifactName = "halo.plan"

// Statuses of a subtask.
const (
	Pending  = "pending"  // waiting for approval or dependencies
	Running  = "running"  // a worker has it
	Asking   = "asking"   // its worker asked the lead something
	Ready    = "ready"    // the worker finished; the lead decides what next
	Review   = "review"   // the reviewer has it
	Changes  = "changes"  // the reviewer asked for changes
	Done     = "done"     // merged into the run's branch
	Failed   = "failed"   // the worker failed; escalate or retry
	Conflict = "conflict" // merging failed
)

// Ladder orders the models from cheapest to strongest.
var Ladder = []string{"haiku", "sonnet", "opus"}

// Next returns the model one step up the ladder.
func Next(model string) (string, bool) {
	i := slices.Index(Ladder, model)
	if i < 0 || i == len(Ladder)-1 {
		return "", false
	}
	return Ladder[i+1], true
}

// Plan is a run's plan.
type Plan struct {
	Goal     string `json:"goal"`
	Branch   string `json:"branch,omitempty"`
	Approved bool   `json:"approved,omitempty"`
	// MergedInto is the base branch the run branch was merged into.
	MergedInto string     `json:"merged_into,omitempty"`
	Subtasks   []*Subtask `json:"subtasks"`
}

// Subtask is one piece of the plan.
type Subtask struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Model      string   `json:"model"`
	Deps       []string `json:"deps,omitempty"`
	Spec       string   `json:"spec,omitempty"`
	Acceptance string   `json:"acceptance,omitempty"`

	Status    string `json:"status"`
	Worker    string `json:"worker,omitempty"`
	Task      string `json:"task,omitempty"`
	Round     int    `json:"round,omitempty"`     // review rounds so far
	Attempts  int    `json:"attempts,omitempty"`  // dispatches so far, escalations included
	Escalated bool   `json:"escalated,omitempty"` // moved up the ladder at least once
	StartedAt int64  `json:"startedAt,omitempty"` // unix ms
	EndedAt   int64  `json:"endedAt,omitempty"`   // unix ms
	Note      string `json:"note,omitempty"`      // the latest outcome, one line
}

// Get returns the subtask with id, or nil.
func (p *Plan) Get(id string) *Subtask {
	for _, s := range p.Subtasks {
		if strings.EqualFold(s.ID, id) {
			return s
		}
	}
	return nil
}

// Public returns a copy without specs and acceptance criteria, small enough for an
// activity entry (Lagrange keeps 4 KB of an artifact).
func (p *Plan) Public() *Plan {
	out := *p
	out.Subtasks = make([]*Subtask, len(p.Subtasks))
	for i, s := range p.Subtasks {
		c := *s
		c.Spec, c.Acceptance = "", ""
		out.Subtasks[i] = &c
	}
	return &out
}

// DepsDone reports whether all of s's dependencies are done.
func (p *Plan) DepsDone(s *Subtask) bool {
	for _, d := range s.Deps {
		if dep := p.Get(d); dep == nil || dep.Status != Done {
			return false
		}
	}
	return true
}

// Validate checks IDs, dependencies, models and cycles.
func (p *Plan) Validate(models []string) error {
	if strings.TrimSpace(p.Goal) == "" {
		return errors.New("the plan needs a goal")
	}
	if len(p.Subtasks) == 0 {
		return errors.New("the plan needs at least one subtask")
	}
	if len(p.Subtasks) > 12 {
		return errors.New("keep the plan to 12 subtasks or fewer; merge small ones")
	}
	seen := map[string]bool{}
	for _, s := range p.Subtasks {
		if s.ID == "" || s.Title == "" || s.Spec == "" {
			return fmt.Errorf("subtask %q needs an id, a title and a spec", s.ID)
		}
		if seen[strings.ToUpper(s.ID)] {
			return fmt.Errorf("duplicate subtask id %q", s.ID)
		}
		seen[strings.ToUpper(s.ID)] = true
		if !slices.Contains(models, s.Model) {
			return fmt.Errorf("subtask %s: model %q isn't one of %v", s.ID, s.Model, models)
		}
	}
	for _, s := range p.Subtasks {
		for _, d := range s.Deps {
			if !seen[strings.ToUpper(d)] {
				return fmt.Errorf("subtask %s depends on unknown %q", s.ID, d)
			}
		}
	}
	// Cycle check: depth-first search with colors.
	color := map[string]int{}
	var visit func(id string) error
	visit = func(id string) error {
		switch color[id] {
		case 1:
			return fmt.Errorf("the dependencies have a cycle through %s", id)
		case 2:
			return nil
		}
		color[id] = 1
		for _, d := range p.Get(id).Deps {
			if err := visit(p.Get(d).ID); err != nil {
				return err
			}
		}
		color[id] = 2
		return nil
	}
	for _, s := range p.Subtasks {
		if err := visit(s.ID); err != nil {
			return err
		}
	}
	return nil
}

// Tag is the prefix of a delegated message, "[S3] ", which lets anyone reading the
// activity log tie a task to its subtask.
func Tag(id string) string { return "[" + id + "] " }

// ParseTag returns the subtask ID at the start of s ("[S3] ..."), or "".
func ParseTag(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range []string{"↺ ", "↑ "} {
		s = strings.TrimPrefix(s, p)
	}
	if !strings.HasPrefix(s, "[") {
		return ""
	}
	end := strings.IndexByte(s, ']')
	if end < 2 || end > 12 {
		return ""
	}
	return s[1:end]
}
