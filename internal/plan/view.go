package plan

import (
	"fmt"
	"slices"
	"strings"
)

// DocName names the artifact that carries the full plan (specs included) and the
// lead's visuals on the root task. It can outgrow what the activity log keeps of
// an artifact, so clients fetch it from the task.
const DocName = "halo.plan.doc"

// Doc is the plan studio's content: the full plan and the blocks the lead showed.
type Doc struct {
	Plan   *Plan   `json:"plan,omitempty"`
	Blocks []Block `json:"blocks,omitempty"`
	// Shown counts the lead's show calls, so a client can tell new visuals apart.
	Shown int `json:"shown,omitempty"`
}

// Block kinds.
const (
	KindArchitecture = "architecture"
	KindFlow         = "flow"
	KindFiles        = "files"
	KindCompare      = "compare"
	KindNote         = "note"
)

// Kinds lists the block kinds.
var Kinds = []string{KindArchitecture, KindFlow, KindFiles, KindCompare, KindNote}

// What a run does to a part of the system.
const (
	Existing = "existing"
	New      = "new"
	Changed  = "changed"
	Removed  = "removed"
)

var changes = []string{"", Existing, New, Changed, Removed}

// Limits keep the visuals small: cheap for the lead to write, readable on a
// terminal.
const (
	MaxBlocks = 6
	MaxNodes  = 14
	MaxEdges  = 24
	MaxGroups = 6
	MaxSteps  = 16
	MaxActors = 6
	MaxPaths  = 40
	MaxCols   = 6
	MaxRows   = 10
	MaxNote   = 500
)

// Block is one visual. Kind says which fields it uses.
type Block struct {
	Kind  string `json:"kind" jsonschema:"architecture | flow | files | compare | note"`
	Title string `json:"title,omitempty" jsonschema:"a few words"`
	// architecture
	Groups []Group `json:"groups,omitempty" jsonschema:"architecture: areas of the system (packages, services, layers), drawn as columns left to right in this order"`
	Nodes  []Node  `json:"nodes,omitempty" jsonschema:"architecture: the components, at most 14"`
	Edges  []Edge  `json:"edges,omitempty" jsonschema:"architecture: who calls or feeds whom"`
	// flow
	Steps []Step `json:"steps,omitempty" jsonschema:"flow: what happens in order, like a sequence diagram"`
	// files
	Paths []Path `json:"paths,omitempty" jsonschema:"files: the files the run adds, changes or removes"`
	// compare
	Columns []string   `json:"columns,omitempty" jsonschema:"compare: the header row"`
	Rows    [][]string `json:"rows,omitempty" jsonschema:"compare: the rows, one cell per column"`
	// note
	Tone string `json:"tone,omitempty" jsonschema:"note: info | warn | question (a question the person can answer inline)"`
	Text string `json:"text,omitempty" jsonschema:"note: one short paragraph"`
}

// Group is an area of an architecture block.
type Group struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Node is a component of an architecture block.
type Node struct {
	ID      string `json:"id"`
	Label   string `json:"label" jsonschema:"a short name, like 'pg store' or 'POST /api/tasks'"`
	Group   string `json:"group,omitempty"`
	Change  string `json:"change,omitempty" jsonschema:"existing (default) | new | changed | removed"`
	Subtask string `json:"subtask,omitempty" jsonschema:"the subtask that builds or changes it"`
}

// Edge connects two nodes.
type Edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Label  string `json:"label,omitempty"`
	Change string `json:"change,omitempty" jsonschema:"existing (default) | new | changed | removed"`
}

// Step is one step of a flow: from calls or hands over to to.
type Step struct {
	From   string `json:"from"`
	To     string `json:"to,omitempty" jsonschema:"empty for something from does by itself"`
	Text   string `json:"text"`
	Change string `json:"change,omitempty" jsonschema:"existing (default) | new | changed | removed"`
}

// Path is a file of a files block.
type Path struct {
	Path    string `json:"path"`
	Change  string `json:"change,omitempty" jsonschema:"new | changed | removed"`
	Subtask string `json:"subtask,omitempty"`
}

// ValidateBlocks checks blocks against the limits. subtasks, when not nil, are the
// plan's subtask IDs that blocks may point at.
func ValidateBlocks(bs []Block, subtasks []string) error {
	if len(bs) > MaxBlocks {
		return fmt.Errorf("show at most %d blocks; keep the ones that help decide", MaxBlocks)
	}
	sub := func(where, id string) error {
		if id == "" || subtasks == nil || slices.Contains(subtasks, strings.ToUpper(id)) {
			return nil
		}
		return fmt.Errorf("%s points at subtask %q, which isn't in the plan", where, id)
	}
	change := func(where, ch string) error {
		if slices.Contains(changes, ch) {
			return nil
		}
		return fmt.Errorf("%s: change %q isn't one of existing, new, changed, removed", where, ch)
	}
	for i, b := range bs {
		where := fmt.Sprintf("block %d (%s)", i+1, b.Kind)
		switch b.Kind {
		case KindArchitecture:
			if len(b.Nodes) == 0 {
				return fmt.Errorf("%s needs nodes", where)
			}
			if len(b.Nodes) > MaxNodes || len(b.Edges) > MaxEdges || len(b.Groups) > MaxGroups {
				return fmt.Errorf("%s is too big (at most %d nodes, %d edges, %d groups); show only the part this run touches", where, MaxNodes, MaxEdges, MaxGroups)
			}
			groups := map[string]bool{}
			for _, g := range b.Groups {
				if g.ID == "" || groups[g.ID] {
					return fmt.Errorf("%s: every group needs a unique id", where)
				}
				groups[g.ID] = true
			}
			nodes := map[string]bool{}
			for _, n := range b.Nodes {
				if n.ID == "" || n.Label == "" || nodes[n.ID] {
					return fmt.Errorf("%s: every node needs a unique id and a label", where)
				}
				nodes[n.ID] = true
				if n.Group != "" && !groups[n.Group] {
					return fmt.Errorf("%s: node %s is in unknown group %q", where, n.ID, n.Group)
				}
				if err := change(where+" node "+n.ID, n.Change); err != nil {
					return err
				}
				if err := sub(where+" node "+n.ID, n.Subtask); err != nil {
					return err
				}
			}
			for _, e := range b.Edges {
				if !nodes[e.From] || !nodes[e.To] {
					return fmt.Errorf("%s: edge %s→%s needs two known nodes", where, e.From, e.To)
				}
				if err := change(where+" edge", e.Change); err != nil {
					return err
				}
			}
		case KindFlow:
			if len(b.Steps) == 0 || len(b.Steps) > MaxSteps {
				return fmt.Errorf("%s needs 1-%d steps", where, MaxSteps)
			}
			actors := map[string]bool{}
			for _, s := range b.Steps {
				if s.From == "" || s.Text == "" {
					return fmt.Errorf("%s: every step needs from and text", where)
				}
				actors[s.From] = true
				if s.To != "" {
					actors[s.To] = true
				}
				if err := change(where, s.Change); err != nil {
					return err
				}
			}
			if len(actors) > MaxActors {
				return fmt.Errorf("%s has %d actors; at most %d", where, len(actors), MaxActors)
			}
		case KindFiles:
			if len(b.Paths) == 0 || len(b.Paths) > MaxPaths {
				return fmt.Errorf("%s needs 1-%d paths", where, MaxPaths)
			}
			for _, p := range b.Paths {
				if p.Path == "" {
					return fmt.Errorf("%s: a path is empty", where)
				}
				if err := change(where+" "+p.Path, p.Change); err != nil {
					return err
				}
				if err := sub(where+" "+p.Path, p.Subtask); err != nil {
					return err
				}
			}
		case KindCompare:
			if len(b.Columns) < 2 || len(b.Columns) > MaxCols || len(b.Rows) == 0 || len(b.Rows) > MaxRows {
				return fmt.Errorf("%s needs 2-%d columns and 1-%d rows", where, MaxCols, MaxRows)
			}
			for _, r := range b.Rows {
				if len(r) > len(b.Columns) {
					return fmt.Errorf("%s: a row has more cells than columns", where)
				}
			}
		case KindNote:
			if strings.TrimSpace(b.Text) == "" || len(b.Text) > MaxNote {
				return fmt.Errorf("%s needs a text of at most %d characters", where, MaxNote)
			}
			if !slices.Contains([]string{"", "info", "warn", "question"}, b.Tone) {
				return fmt.Errorf("%s: tone %q isn't one of info, warn, question", where, b.Tone)
			}
		default:
			return fmt.Errorf("block %d: kind %q isn't one of %s", i+1, b.Kind, strings.Join(Kinds, ", "))
		}
	}
	return nil
}

// IDs returns the plan's subtask IDs, upper case.
func (p *Plan) IDs() []string {
	if p == nil {
		return nil
	}
	out := make([]string, len(p.Subtasks))
	for i, s := range p.Subtasks {
		out[i] = strings.ToUpper(s.ID)
	}
	return out
}

// Diff marks how each subtask of p differs from old: "+" new, "~" its title, model,
// deps, spec or acceptance changed. Subtasks only in old come back under "−".
func Diff(old, p *Plan) (marks map[string]string, removed []string) {
	marks = map[string]string{}
	if old == nil || p == nil {
		return marks, nil
	}
	for _, s := range p.Subtasks {
		was := old.Get(s.ID)
		switch {
		case was == nil:
			marks[s.ID] = "+"
		case was.Title != s.Title || was.Model != s.Model || !slices.Equal(was.Deps, s.Deps) ||
			was.Spec != s.Spec || was.Acceptance != s.Acceptance:
			marks[s.ID] = "~"
		}
	}
	for _, s := range old.Subtasks {
		if p.Get(s.ID) == nil {
			removed = append(removed, s.ID)
		}
	}
	return marks, removed
}
