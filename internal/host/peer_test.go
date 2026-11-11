package host

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"
	"github.com/three-agent-problem/lagrange/activity"

	"github.com/three-agent-problem/halo/internal/config"
	"github.com/three-agent-problem/halo/internal/plan"
)

var t0 = time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)

func TestSubKnowledge(t *testing.T) {
	ks := []activity.Knowledge{
		{TaskID: "t1", Agent: "w-haiku", LastActive: t0, Context: 30000, Files: []activity.FileTouch{
			{Path: "/wt/S1/store/store.go", Read: true, At: t0},
		}},
		{TaskID: "t2", Agent: "w-haiku", LastActive: t0.Add(time.Minute), Context: 34000, Files: []activity.FileTouch{
			{Path: "/wt/S1/store/store.go", Changed: true, At: t0.Add(time.Minute)},
			{Path: "/etc/hosts", Read: true, At: t0},
		}},
		{TaskID: "t9", Agent: "w-other", LastActive: t0},
	}
	subs := map[string]*subRT{
		"S1": {dir: "/wt/S1", tasks: []string{"t1", "t2"}},
		"S2": {dir: "/wt/S2"}, // never worked
	}
	know := subKnowledge(ks, subs)
	if _, ok := know["S2"]; ok || len(know) != 1 {
		t.Fatalf("know = %+v", know)
	}
	k := know["S1"]
	if k.Context != 34000 || !k.LastActive.Equal(t0.Add(time.Minute)) || len(k.Files) != 2 {
		t.Fatalf("S1 = %+v", k)
	}
	if f := k.Files[1]; f.Path != "store/store.go" || !f.Read || !f.Changed {
		t.Fatalf("store.go = %+v", f)
	}
	if f := k.Files[0]; f.Path != "/etc/hosts" {
		t.Fatalf("a file outside the worktree keeps its path: %+v", f)
	}
	line := knowLine(k, t0.Add(3*time.Minute))
	if line != "active 2m ago (warm), 34k tokens in context; read /etc/hosts; changed store/store.go" {
		t.Fatalf("knowLine = %q", line)
	}
	if !strings.Contains(knowLine(k, t0.Add(time.Hour)), "(cold)") {
		t.Fatal("an hour later the session is cold")
	}
}

func TestBriefShowsKnowledge(t *testing.T) {
	p := &plan.Plan{Goal: "g", Subtasks: []*plan.Subtask{
		{ID: "S1", Title: "backend", Status: plan.Running, Worker: "w-sonnet"},
		{ID: "S2", Title: "frontend", Status: plan.Pending},
	}}
	know := map[string]subKnow{"S1": {LastActive: t0, Context: 40000, Files: []activity.FileTouch{{Path: "server/api.go", Changed: true}}}}
	got := brief(p, map[string]*subRT{}, "S2", know, t0.Add(30*time.Second))
	if !strings.Contains(got, "- S1 backend: running @w-sonnet\n  knows: active just now (warm), 40k tokens in context; changed server/api.go") {
		t.Fatalf("brief:\n%s", got)
	}
}

func TestPickPeer(t *testing.T) {
	_, r, _ := humanLead(t) // S1 ready on w-opus, S2 running on w-sonnet, S3 pending
	r.rt("S1").session = "run/S1/1"
	r.rt("S2").session = "run/S2/1"
	know := map[string]subKnow{
		"S1": {Files: []activity.FileTouch{{Path: "store/store.go", Read: true, At: t0}}},
		"S2": {Files: []activity.FileTouch{{Path: "store/store.go", Changed: true, At: t0.Add(time.Minute)}, {Path: "api/api.go", Changed: true, At: t0}}},
	}
	for _, tc := range []struct{ from, target, about, want string }{
		{"S3", "S9", "", "no subtask S9"},
		{"S1", "s1", "", "your own subtask"},
		{"S1", "S3", "", "hasn't been worked on"},
		{"S3", "", "", "say whom to ask"},
		{"S3", "", "web/app.ts", "no teammate in this run has read or changed web/app.ts"},
	} {
		if _, err := r.pickPeer(tc.from, tc.target, tc.about, know); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v, want %q", tc, err, tc.want)
		}
	}
	// By file: the teammate that touched it last, busy or not.
	p, err := r.pickPeer("S3", "", "store.go", know)
	if err != nil || p.id != "S2" || !p.busy || p.rt.worker != "w-sonnet" {
		t.Fatalf("about store.go: %+v %v", p, err)
	}
	// S2 asking about store.go can't pick itself.
	if p, err := r.pickPeer("S2", "", "store.go", know); err != nil || p.id != "S1" || p.busy {
		t.Fatalf("S2 about store.go: %+v %v", p, err)
	}
}

func TestForkPeer(t *testing.T) {
	dir := t.TempDir()
	h := &Host{cfg: &config.Config{ACP: "claude-agent-acp"}, sessions: openSessions(dir + "/sessions.json"),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		workers: map[string]*worker{
			"w-sonnet": {handle: "w-sonnet", agent: config.Agent{Name: "sonnet", Model: "sonnet"}},
			"w-agy":    {handle: "w-agy", agent: config.Agent{Name: "agy", ACP: "agy acp"}},
		}}
	if err := h.sessions.put("w-sonnet run/S2/1", a2aacp.Session{ID: "sess-123"}); err != nil {
		t.Fatal(err)
	}
	var got []string
	h.fork = func(_ context.Context, dir, sessionID, model, question string) (forkAnswer, error) {
		got = append(got, dir, sessionID, model)
		return forkAnswer{Text: "POST /notes", Cost: 0.001, CacheRead: 40000}, nil
	}
	ans, err := h.forkPeer(context.Background(), peerPick{id: "S2", rt: subRT{worker: "w-sonnet", session: "run/S2/1", dir: "/wt/S2"}, busy: true}, "q")
	if err != nil || ans.Text != "POST /notes" || strings.Join(got, " ") != "/wt/S2 sess-123 sonnet" {
		t.Fatalf("fork: %+v %v %v", ans, err, got)
	}
	if _, err := h.forkPeer(context.Background(), peerPick{rt: subRT{worker: "w-sonnet", session: "run/S9/1"}}, "q"); err == nil || !strings.Contains(err.Error(), "isn't known yet") {
		t.Fatalf("unknown session: %v", err)
	}
	if _, err := h.forkPeer(context.Background(), peerPick{rt: subRT{worker: "w-agy", session: "x"}}, "q"); err == nil || !strings.Contains(err.Error(), "isn't Claude Code") {
		t.Fatalf("agy: %v", err)
	}
}
