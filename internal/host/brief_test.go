package host

import (
	"strings"
	"testing"
	"time"

	"github.com/three-agent-problem/halo/internal/config"
	"github.com/three-agent-problem/halo/internal/plan"
)

func TestBrief(t *testing.T) {
	p := &plan.Plan{Goal: "device login", Subtasks: []*plan.Subtask{
		{ID: "S1", Title: "store", Status: plan.Done, Worker: "w-sonnet", EndedAt: 10},
		{ID: "S2", Title: "api", Status: plan.Ready, Worker: "w-haiku", EndedAt: 20},
		{ID: "S3", Title: "web", Status: plan.Pending},
	}}
	subs := map[string]*subRT{
		"S1": {summary: "Added pg table device_codes.\nTests need postgres on PATH."},
		"S2": {summary: "POST /api/device in platform/device.go"},
		"S3": {summary: "should never show"},
	}
	got := brief(p, subs, "S3", nil, time.Now())
	for _, want := range []string{
		"Goal: device login",
		"- S1 store: done @w-sonnet",
		"- S3 web: pending (you)",
		"- S1: Added pg table device_codes. Tests need postgres on PATH.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "should never show") {
		t.Errorf("brief has the worker's own summary:\n%s", got)
	}
	if strings.Index(got, "- S2: POST") > strings.Index(got, "- S1: Added") {
		t.Errorf("newest summary should come first:\n%s", got)
	}
	if brief(nil, subs, "S1", nil, time.Now()) != "" {
		t.Error("no plan, no brief")
	}
}

func TestBriefCap(t *testing.T) {
	p := &plan.Plan{Goal: "g"}
	subs := map[string]*subRT{}
	for i := range 20 {
		id := "S" + string(rune('A'+i))
		p.Subtasks = append(p.Subtasks, &plan.Subtask{ID: id, Title: "t", Status: plan.Done, EndedAt: int64(i)})
		subs[id] = &subRT{summary: strings.Repeat("x", maxSummary)}
	}
	got := brief(p, subs, "", nil, time.Now())
	if len(got) > maxBrief {
		t.Fatalf("brief is %d bytes, cap %d", len(got), maxBrief)
	}
	if !strings.Contains(got, "- ST: ") || strings.Contains(got, "- SA: ") {
		t.Fatalf("the newest summaries should stay and the oldest go:\n%s", got)
	}
}

func TestPeerAnswerDoesNotCommit(t *testing.T) {
	if !commits(meta{Worktree: "/wt", Subtask: "S1"}, config.RoleWorker) {
		t.Error("a subtask turn commits")
	}
	if commits(meta{Kind: "peer", Worktree: "/wt"}, config.RoleWorker) {
		t.Error("an answer to a teammate doesn't commit")
	}
	if commits(meta{Worktree: "/wt"}, config.RoleReviewer) {
		t.Error("the reviewer doesn't commit")
	}
	if !strings.Contains(peerText("w-haiku", "S3", "S1", "where is the store?"), "Don't edit files") {
		t.Error("the question says not to edit")
	}
}
