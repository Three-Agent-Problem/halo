package tui

import (
	"testing"
	"time"

	"github.com/three-agent-problem/halo/internal/lg"
)

// A watch-only Halo builds the transcript, the roster's cost and the commits from
// what Lagrange records.
func TestFromLog(t *testing.T) {
	st := demoState()
	now := time.Now()
	seq := int64(0)
	apply := func(e lg.Entry) {
		seq++
		e.Seq, e.At = seq, now
		st.Apply(e, now)
	}
	w := "halo-sonnet-1"
	apply(lg.Entry{Kind: "task", TaskID: "root", Sender: "ada", Recipient: "halo-lead", State: "TASK_STATE_SUBMITTED", Summary: "build it"})
	apply(lg.Entry{Kind: "task", TaskID: "t1", ParentTaskID: "root", ContextID: "c1", Sender: "halo-lead", Recipient: w,
		State: "TASK_STATE_SUBMITTED", Summary: "Subtask S2: the store", Data: map[string]any{"tags": map[string]any{"subtask": "S2", "run": "root"}}})
	if st.Tasks["t1"].Sub != "S2" {
		t.Fatalf("subtask from tags = %q", st.Tasks["t1"].Sub)
	}
	tool := func(status string, extra map[string]any) map[string]any {
		tc := map[string]any{"id": "call-1", "title": "go test ./...", "kind": "execute", "status": status}
		for k, v := range extra {
			tc[k] = v
		}
		return map[string]any{"toolCall": tc}
	}
	apply(lg.Entry{Kind: "task", TaskID: "t1", ContextID: "c1", Recipient: w, State: "TASK_STATE_WORKING", Summary: "Running go test ./...",
		Data: tool("in_progress", map[string]any{"input": map[string]any{"command": "go test ./..."}})})
	apply(lg.Entry{Kind: "artifact", TaskID: "t1", ContextID: "c1", Recipient: w, Summary: "thoughts", Data: map[string]any{"artifactId": "th", "content": "the store needs an index"}})
	apply(lg.Entry{Kind: "artifact", TaskID: "t1", ContextID: "c1", Recipient: w, Data: map[string]any{"artifactId": "th", "append": true, "content": " on context_id"}})
	apply(lg.Entry{Kind: "task", TaskID: "t1", ContextID: "c1", Recipient: w, State: "TASK_STATE_WORKING", Summary: "Finished go test ./...",
		Data: tool("completed", map[string]any{"output": "ok  ./store"})})
	apply(lg.Entry{Kind: "usage", TaskID: "t1", ContextID: "c1", Recipient: w, Summary: "usage: $0.40",
		Data: map[string]any{"usage": map[string]any{"used": 50000.0, "size": 200000.0, "cost": map[string]any{"amount": 0.4, "currency": "USD"}}}})
	apply(lg.Entry{Kind: "artifact", TaskID: "t1", ContextID: "c1", Recipient: w, Summary: "commit 3f2a1c9 halo: S2 the store (+4 −1)",
		Data: map[string]any{"artifactId": "commit-3f2a1c9e00", "commit": map[string]any{
			"sha": "3f2a1c9e00", "subject": "halo: S2 the store", "branch": "halo/abc-S2", "at": now.Format(time.RFC3339),
			"insertions": 4.0, "deletions": 1.0, "files": []any{map[string]any{"path": "store.go"}}}}})
	apply(lg.Entry{Kind: "artifact", TaskID: "t1", ContextID: "c1", Recipient: w, Data: map[string]any{"artifactId": "resp", "content": "Done: added the index."}})

	feed := st.Agents[w].Feed
	var kinds []string
	for _, b := range feed {
		kinds = append(kinds, b.Kind)
	}
	if len(feed) != 3 || kinds[0] != "tool" || kinds[1] != "think" || kinds[2] != "say" {
		t.Fatalf("transcript kinds = %v", kinds)
	}
	if b := feed[0]; b.Status != "completed" || b.Output != "ok  ./store" || b.input("command") != "go test ./..." {
		t.Fatalf("tool block = %+v", b)
	}
	if feed[1].Text != "the store needs an index on context_id" {
		t.Fatalf("thoughts = %q", feed[1].Text)
	}
	if got := st.Agents[w].Usage.Cost(); got != 0.4 {
		t.Fatalf("cost = %v", got)
	}
	if len(st.Commits) != 1 || st.Commits[0].Sub != "S2" || st.Commits[0].Task != "t1" || st.Commits[0].Add != 4 || st.Commits[0].Files != 1 {
		t.Fatalf("commits = %+v", st.Commits)
	}
	if ans := st.Tasks["t1"].answer; ans["th"] != nil || ans["commit-3f2a1c9e00"] != nil || ans["resp"] == nil {
		t.Fatalf("the task's answer took thoughts or the commit: %v", st.Tasks["t1"].answerOrder)
	}

	// The Halo that hosts the team has all of this from Claude Code: the log's copy is skipped.
	hosted := demoState()
	hosted.Hosted = true
	seq, st = 0, hosted
	apply(lg.Entry{Kind: "task", TaskID: "root", Sender: "ada", Recipient: "halo-lead", State: "TASK_STATE_SUBMITTED", Summary: "build it"})
	apply(lg.Entry{Kind: "task", TaskID: "t1", ParentTaskID: "root", ContextID: "c1", Sender: "halo-lead", Recipient: w, State: "TASK_STATE_SUBMITTED", Summary: "[S2] old style"})
	apply(lg.Entry{Kind: "task", TaskID: "t1", ContextID: "c1", Recipient: w, State: "TASK_STATE_WORKING", Data: tool("in_progress", nil)})
	if len(hosted.Agents[w].Feed) != 0 || hosted.Tasks["t1"].Sub != "S2" {
		t.Fatalf("hosted: feed %d, sub %q", len(hosted.Agents[w].Feed), hosted.Tasks["t1"].Sub)
	}
}
