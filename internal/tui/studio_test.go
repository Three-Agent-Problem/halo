package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/three-agent-problem/halo/internal/plan"
)

func TestDrawArch(t *testing.T) {
	b := demoBlocks()[0]
	d := drawArch(b, 200, sel{item: 2, subtask: "S2"})
	if len(d.rows) != len(b.Nodes) {
		t.Fatalf("%d rows for %d nodes", len(d.rows), len(b.Nodes))
	}
	text := stripANSI(strings.Join(d.lines, "\n"))
	for _, n := range b.Nodes {
		if !strings.Contains(text, n.Label) {
			t.Errorf("node %q isn't drawn", n.Label)
		}
	}
	// Every arrow between neighbouring columns ends in a head.
	heads := strings.Count(text, "▶")
	adjacent := 0
	_, cols := archColumns(b)
	col := map[string]int{}
	for ci, c := range cols {
		for _, n := range c {
			col[n.ID] = ci
		}
	}
	for _, e := range b.Edges {
		if col[e.To] == col[e.From]+1 {
			adjacent++
		}
	}
	if heads < adjacent {
		t.Errorf("%d arrow heads for %d drawn edges", heads, adjacent)
	}
	// Edges inside a column are listed under the picture.
	if !strings.Contains(text, "pg task store ⤳ store.Store") {
		t.Errorf("the edge inside the store column isn't listed:\n%s", text)
	}
	// No box overlaps another: each node's label row and column are its own.
	seen := map[[2]int]string{}
	for i, n := range archOrder(b) {
		key := [2]int{d.rows[i], col[n.ID]}
		if other, dup := seen[key]; dup {
			t.Errorf("%s and %s share a box", n.Label, other)
		}
		seen[key] = n.Label
	}
	for _, l := range d.lines {
		if w := ansiWidth(l); w > 200 {
			t.Errorf("a line is %d cells wide", w)
		}
	}
}

func TestDrawArchNarrow(t *testing.T) {
	b := demoBlocks()[0]
	d := drawArch(b, 40, sel{item: -1})
	text := stripANSI(strings.Join(d.lines, "\n"))
	if strings.Contains(text, "╔") {
		t.Error("a narrow panel should list the nodes, not draw boxes")
	}
	for _, n := range b.Nodes {
		if !strings.Contains(text, n.Label) {
			t.Errorf("node %q isn't listed", n.Label)
		}
	}
	if len(d.rows) != len(b.Nodes) {
		t.Errorf("%d rows for %d nodes", len(d.rows), len(b.Nodes))
	}
}

func TestDrawBlocks(t *testing.T) {
	for _, b := range demoBlocks() {
		d := drawBlock(b, 120, sel{item: 0})
		if len(d.lines) == 0 {
			t.Errorf("%s: nothing drawn", b.Kind)
		}
		if n := blockItems(b); len(d.rows) != n {
			t.Errorf("%s: %d rows for %d items", b.Kind, len(d.rows), n)
		}
		for _, l := range d.lines {
			if w := ansiWidth(l); w > 120 {
				t.Errorf("%s: a line is %d cells wide: %q", b.Kind, w, stripANSI(l))
			}
		}
	}
}

// studioModel is a model with the demo's plan doc and a plan approval waiting.
func studioModel(t *testing.T) (*model, *answerRec) {
	t.Helper()
	rec := &answerRec{Demo: &Demo{}}
	m := newModel(context.Background(), Options{Backend: rec, State: demoState()})
	m.w, m.h = 200, 50
	p := &plan.Plan{Goal: "tasks", Version: 1, Subtasks: []*plan.Subtask{
		{ID: "S1", Title: "schema", Model: "haiku", Spec: "add tables", Status: plan.Pending},
		{ID: "S2", Title: "pg store", Model: "sonnet", Spec: "implement", Status: plan.Pending},
	}}
	m.st.Root, m.st.Plan = "root", p.Public()
	m.st.SetDoc(&plan.Doc{Plan: p, Blocks: demoBlocks()[:1]})
	m.waits = []Wait{{TaskID: "apr", Asker: "halo-lead", Question: "Approve?", Approval: true, Since: time.Now()}}
	m.openStudio(m.waits)
	return m, rec
}

func TestStudioOpensForApproval(t *testing.T) {
	m, _ := studioModel(t)
	if m.ui.Focus != pPlan || m.ui.Size[pPlan] != sizeStudio {
		t.Fatalf("focus %d size %v", m.ui.Focus, m.ui.Size[pPlan])
	}
	out := stripANSI(m.render(time.Now()))
	for _, want := range []string{"PLAN STUDIO", "pg task store", "S2", "waiting for your approval"} {
		if !strings.Contains(out, want) {
			t.Errorf("the studio lacks %q", want)
		}
	}
	// Closing it and growing back the same approval doesn't reopen it by itself.
	m.ui.Size[pPlan] = sizeNormal
	m.openStudio(m.waits)
	if m.ui.Size[pPlan] == sizeStudio {
		t.Error("the studio opened twice for one approval")
	}
}

func TestStudioNotesGoWithTheRejection(t *testing.T) {
	m, rec := studioModel(t)
	// A note on the selected node, a model change and a drop.
	typeKeys(m, "j", "j", "j", "c")
	if m.mode != modeStudioNote {
		t.Fatalf("mode = %v", m.mode)
	}
	m.Update(tea.PasteMsg{Content: "keep it under /api"})
	typeKeys(m, "enter", "l", "m", "m", "h", "x")
	st := m.ui.Studio
	if len(st.notes) != 3 {
		t.Fatalf("notes = %+v", st.notes)
	}
	cmd := typeKeys(m, "s")
	if cmd == nil {
		t.Fatal("s sent nothing")
	}
	if msg, ok := cmd().(doneMsg); !ok || msg.err != nil {
		t.Fatalf("send: %+v", msg)
	}
	if rec.task != "apr" || rec.decision != "reject" {
		t.Errorf("answered %q with %q", rec.task, rec.decision)
	}
	for _, want := range []string{"version 1", "task store › /api/tasks: keep it under /api", "S2 pg store: use haiku", "S1 schema: drop it"} {
		if !strings.Contains(rec.text, want) {
			t.Errorf("the notes lack %q:\n%s", want, rec.text)
		}
	}
	if len(st.notes) != 0 || m.approvalWait() != nil {
		t.Error("sending should clear the notes and the wait")
	}
}

func TestStudioApprove(t *testing.T) {
	m, rec := studioModel(t)
	typeKeys(m, "x") // a pending note asks first
	if cmd := typeKeys(m, "y"); cmd != nil || m.confirm == nil {
		t.Fatal("approving with notes pending should ask first")
	}
	cmd := typeKeys(m, "y")
	if cmd == nil {
		t.Fatal("y in the dialog approved nothing")
	}
	cmd()
	if rec.decision != "approve" || rec.task != "apr" {
		t.Errorf("answered %q with %q", rec.task, rec.decision)
	}
}

func TestSetDocMarksRevisions(t *testing.T) {
	s := demoState()
	v1 := &plan.Plan{Version: 1, Subtasks: []*plan.Subtask{{ID: "S1", Title: "a", Spec: "x"}, {ID: "S2", Title: "b", Spec: "y"}}}
	v2 := &plan.Plan{Version: 2, Subtasks: []*plan.Subtask{{ID: "S1", Title: "a", Spec: "x2"}, {ID: "S3", Title: "c", Spec: "z"}}}
	s.SetDoc(&plan.Doc{Plan: v1})
	if len(s.DocMarks) != 0 {
		t.Fatalf("the first version has marks: %v", s.DocMarks)
	}
	s.SetDoc(&plan.Doc{Plan: v1, Blocks: demoBlocks()}) // new visuals, same version
	if len(s.DocMarks) != 0 {
		t.Fatalf("visuals alone made marks: %v", s.DocMarks)
	}
	s.SetDoc(&plan.Doc{Plan: v2})
	if s.DocMarks["S1"] != "~" || s.DocMarks["S3"] != "+" || len(s.DocRemoved) != 1 {
		t.Errorf("marks %v removed %v", s.DocMarks, s.DocRemoved)
	}
}
