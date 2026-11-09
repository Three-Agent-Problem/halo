package host

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/three-agent-problem/halo/internal/config"
	"github.com/three-agent-problem/halo/internal/plan"
)

func testPlan(statuses ...string) *plan.Plan {
	p := &plan.Plan{Goal: "g", Approved: true}
	for i, st := range statuses {
		p.Subtasks = append(p.Subtasks, &plan.Subtask{ID: "S" + string(rune('1'+i)), Status: st})
	}
	return p
}

func TestCanLand(t *testing.T) {
	cases := []struct {
		name string
		p    *plan.Plan
		base string
		want string // substring of the error; "" for nil
	}{
		{"all done", testPlan(plan.Done, plan.Done), "main", ""},
		{"one running", testPlan(plan.Done, plan.Running), "main", "every subtask is done (S2 running)"},
		{"one ready", testPlan(plan.Ready, plan.Done), "main", "S1 ready"},
		{"no base", testPlan(plan.Done, plan.Done), "", "detached HEAD"},
		{"nil plan", nil, "main", "isn't approved"},
		{"not approved", &plan.Plan{Subtasks: testPlan(plan.Done).Subtasks}, "main", "isn't approved"},
		{"empty", &plan.Plan{Approved: true}, "main", "every subtask is done"},
	}
	for _, c := range cases {
		err := canLand(c.p, c.base)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// testRun builds a run on a fresh repository, with an approved plan of the given
// subtasks, without a server.
func testRun(t *testing.T, ids ...string) (*run, string) {
	t.Helper()
	repo := newRepo(t)
	wt, err := newWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{cfg: &config.Config{Repo: repo}, st: &config.State{Human: "kaan"}, wt: wt, ctx: context.Background(),
		log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	r := &run{l: &lead{h: h}, id: "task-abc123", short: "abc123", events: make(chan event, 16), questions: map[string]*question{}}
	r.plan = &plan.Plan{Goal: "g"}
	for _, id := range ids {
		r.plan.Subtasks = append(r.plan.Subtasks, &plan.Subtask{ID: id, Status: plan.Ready})
	}
	if err := r.approve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.base != "main" {
		t.Fatalf("base = %q", r.base)
	}
	for _, id := range ids {
		branch, dir, err := wt.subtask(context.Background(), r.short, r.branch, id, 1)
		if err != nil {
			t.Fatal(err)
		}
		commit(t, dir, strings.ToLower(id)+".txt", id+"\n")
		r.mu.Lock()
		r.rt(id).branch, r.rt(id).dir = branch, dir
		r.mu.Unlock()
	}
	return r, repo
}

func TestMergeSubLandsWhenAllDone(t *testing.T) {
	r, repo := testRun(t, "S1", "S2")
	ctx := context.Background()

	if _, kind, land := r.mergeSub(ctx, "S1"); kind != "review" || land {
		t.Fatalf("S1: kind %s, land %v", kind, land)
	}
	msg, kind, land := r.mergeSub(ctx, "S2")
	if kind != "review" || !land {
		t.Fatalf("S2: kind %s, land %v: %s", kind, land, msg)
	}
	r.mu.Lock()
	err := r.openLocked()
	r.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "wait for the merged event") {
		t.Errorf("tools open while landing: %v", err)
	}
	r.land(ctx)

	e := <-r.events
	if e.Kind != "merged" || e.Approved == nil || !*e.Approved || !strings.Contains(e.Hint, "merged into main") {
		t.Fatalf("event: %+v", e)
	}
	for _, f := range []string{"s1.txt", "s2.txt"} {
		if _, err := os.Stat(filepath.Join(repo, f)); err != nil {
			t.Errorf("%s not in the checkout", f)
		}
	}
	if r.plan.MergedInto != "main" || r.runDir != "" {
		t.Errorf("merged_into %q, runDir %q", r.plan.MergedInto, r.runDir)
	}
	if b := gitT(t, repo, "branch", "--list", "halo/*"); b != "" {
		t.Errorf("branches left: %s", b)
	}
	r.mu.Lock()
	err = r.openLocked()
	r.mu.Unlock()
	if err == nil || err.Error() != "the run is merged and finished" {
		t.Errorf("open after merge: %v", err)
	}
	if msg, kind, land := r.mergeSub(ctx, "S1"); kind != "conflict" || land || !strings.Contains(msg, "finished") {
		t.Errorf("mergeSub after merge: %s %s %v", msg, kind, land)
	}
}

func TestLandFailureKeepsRun(t *testing.T) {
	r, repo := testRun(t, "S1")
	ctx := context.Background()
	write(t, repo, "a.txt", "dirty\n")

	if _, _, land := r.mergeSub(ctx, "S1"); !land {
		t.Fatal("expected land")
	}
	r.land(ctx)
	e := <-r.events
	if e.Kind != "merged" || e.Approved == nil || *e.Approved || !strings.Contains(e.Text, "uncommitted") ||
		!strings.Contains(e.Hint, "halo merge abc123") || !strings.Contains(e.Hint, "halo/abc123") {
		t.Fatalf("event: %+v", e)
	}
	if r.plan.MergedInto != "" {
		t.Error("merged_into set")
	}
	if _, err := os.Stat(r.runDir); err != nil {
		t.Errorf("run worktree removed: %v", err)
	}
	r.mu.Lock()
	err := r.openLocked()
	r.mu.Unlock()
	if err != nil {
		t.Errorf("tools closed after a failed merge: %v", err)
	}
	// It lands only once per run.
	r.plan.Subtasks[0].Status = plan.Ready
	if _, _, land := r.mergeSub(ctx, "S1"); land {
		t.Error("landed twice")
	}
}
