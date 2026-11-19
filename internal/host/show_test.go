package host

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/halo/internal/config"
	"github.com/three-agent-problem/halo/internal/plan"
)

// docRun is a run with an attached execution, so what it publishes can be read.
func docRun(t *testing.T) (*run, <-chan a2a.Event) {
	t.Helper()
	h := &Host{cfg: &config.Config{Agents: []config.Agent{{Name: "h", Model: "haiku", Role: config.RoleWorker}, {Name: "s", Model: "sonnet", Role: config.RoleWorker}}},
		st: &config.State{Human: "kaan"}}
	r := &run{l: &lead{h: h}, id: "task-abc123", short: "abc123", events: make(chan event, 16), questions: map[string]*question{}}
	emit := r.attach(&a2asrv.ExecutorContext{TaskID: "task-abc123", ContextID: "ctx"})
	return r, emit
}

// lastDoc returns the newest halo.plan.doc among what was published.
func lastDoc(t *testing.T, emit <-chan a2a.Event) *plan.Doc {
	t.Helper()
	var doc *plan.Doc
	for {
		select {
		case ev := <-emit:
			au, ok := ev.(*a2a.TaskArtifactUpdateEvent)
			if !ok || au.Artifact.Name != plan.DocName {
				continue
			}
			raw, _ := json.Marshal(au.Artifact.Parts[0].Data())
			doc = &plan.Doc{}
			if err := json.Unmarshal(raw, doc); err != nil {
				t.Fatal(err)
			}
		default:
			return doc
		}
	}
}

func TestShowAndPlanVersions(t *testing.T) {
	r, emit := docRun(t)
	in := planIn{Goal: "tasks", Subtasks: []subIn{
		{ID: "s1", Title: "schema", Spec: "add the tables", Acceptance: "migrate", Model: "haiku"},
		{ID: "s2", Title: "store", Spec: "implement it", Acceptance: "tests", Model: "sonnet", Deps: []string{"s1"}},
	}}
	if _, _, err := r.publishPlan(in); err != nil {
		t.Fatal(err)
	}
	doc := lastDoc(t, emit)
	if doc == nil || doc.Plan.Version != 1 || doc.Plan.Get("S1").Spec != "add the tables" {
		t.Fatalf("the doc after the first plan: %+v", doc)
	}

	arch := plan.Block{Kind: plan.KindArchitecture, Nodes: []plan.Node{{ID: "a", Label: "store", Change: plan.New, Subtask: "s2"}}}
	if _, _, err := r.show(showIn{Blocks: []plan.Block{arch}}); err != nil {
		t.Fatal(err)
	}
	doc = lastDoc(t, emit)
	if doc == nil || len(doc.Blocks) != 1 || doc.Shown != 1 || doc.Blocks[0].Nodes[0].Subtask != "S2" {
		t.Fatalf("the doc after show: %+v", doc)
	}
	bad := plan.Block{Kind: plan.KindArchitecture, Nodes: []plan.Node{{ID: "a", Label: "x", Subtask: "S9"}}}
	if _, _, err := r.show(showIn{Blocks: []plan.Block{bad}}); err == nil || !strings.Contains(err.Error(), "S9") {
		t.Errorf("a node of an unknown subtask: %v", err)
	}

	_, res, err := r.publishPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Result, "version 2") || !strings.Contains(res.Result, "show again") {
		t.Errorf("result: %s", res.Result)
	}
	doc = lastDoc(t, emit)
	if doc.Plan.Version != 2 || len(doc.Blocks) != 1 {
		t.Errorf("a revision keeps the visuals and counts up: %+v", doc)
	}
	// The status feed stays small: no specs in halo.plan.
	r.mu.Lock()
	pub := r.plan.Public()
	r.mu.Unlock()
	if pub.Get("S1").Spec != "" || pub.Version != 2 {
		t.Errorf("public plan: %+v", pub.Get("S1"))
	}
}
