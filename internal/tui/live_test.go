package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func upd(agent, task string, v map[string]any) Update {
	raw, _ := json.Marshal(v)
	return Update{Agent: agent, Task: task, Type: v["sessionUpdate"].(string), Raw: raw, At: time.Now()}
}

func TestFeed(t *testing.T) {
	st := demoState()
	a := "halo-sonnet-1"
	text := func(kind, s string) Update {
		return upd(a, "t1", map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": s}})
	}
	st.Feed(text("agent_thought_chunk", "Let me "))
	st.Feed(text("agent_thought_chunk", "look."))
	st.Feed(upd(a, "t1", map[string]any{"sessionUpdate": "tool_call", "toolCallId": "c1", "title": "`go test ./...`", "kind": "execute", "status": "pending",
		"rawInput": map[string]any{"command": "go test ./...", "description": "Run the tests"}}))
	st.Feed(upd(a, "t1", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "c1", "status": "in_progress", "rawInput": map[string]any{}}))
	st.Feed(upd(a, "t1", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "c1", "status": "completed",
		"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "```console\nok  \tpkg\t0.1s\n\x1b[31mFAIL\x1b[0m x\n```"}}}}))
	st.Feed(text("agent_message_chunk", "All "))
	st.Feed(text("agent_message_chunk", "green."))
	st.Feed(upd(a, "t1", map[string]any{"sessionUpdate": "tool_call", "toolCallId": "c2", "title": "Edit a.go", "kind": "edit", "status": "completed",
		"content": []any{map[string]any{"type": "diff", "path": "a.go", "oldText": "x", "newText": "y\nz"}}}))
	st.Feed(upd(a, "t1", map[string]any{"sessionUpdate": "plan", "entries": []map[string]string{{"content": "one", "status": "completed"}}}))
	st.Feed(upd(a, "t1", map[string]any{"sessionUpdate": "plan", "entries": []map[string]string{{"content": "one", "status": "completed"}, {"content": "two", "status": "pending"}}}))
	st.Feed(upd("nobody", "t1", map[string]any{"sessionUpdate": "plan"}))

	f := st.Agents[a].Feed
	if len(f) != 5 {
		t.Fatalf("want think, tool, say, tool, plan; got %d blocks", len(f))
	}
	if f[0].Kind != "think" || f[0].Text != "Let me look." || f[2].Text != "All green." {
		t.Fatalf("chunks: %q %q", f[0].Text, f[2].Text)
	}
	b := f[1]
	if b.Status != "completed" || b.input("command") != "go test ./..." || b.Ended.IsZero() {
		t.Fatalf("tool: %+v", b)
	}
	if got := clean(b.Output); got != "ok      pkg    0.1s\nFAIL x" {
		t.Fatalf("output %q", got)
	}
	if label, title := b.head(); label != "Bash" || title != "go test ./..." {
		t.Fatalf("head %q %q", label, title)
	}
	if f[3].Output != "@@ a.go\n-x\n+y\n+z" {
		t.Fatalf("diff %q", f[3].Output)
	}
	if len(f[4].Entries) != 2 {
		t.Fatalf("a plan update replaces the plan: %+v", f[4].Entries)
	}
	if l, ti := (&Block{Title: "mcp__halo__delegate", Tool: "other"}).head(); l != "MCP" || ti != "halo delegate" {
		t.Fatalf("mcp head %q %q", l, ti)
	}
}

func TestShellKeys(t *testing.T) {
	d := &Demo{Prefix: "halo"}
	pid := d.startShell("halo-sonnet-2", "npm run dev -- --port 5173", 5173)
	d.write(pid, "ready\n")
	m := newModel(context.Background(), Options{Backend: d, State: demoState(), Procs: d})
	m.w, m.h = 200, 56
	m.Update(shellsMsg(func() []Shell { s, _ := d.Shells(context.Background()); return s }()))
	var opened string
	openURL = func(u string) error { opened = u; return nil }
	press(m, "8", "o")
	if opened != "http://localhost:5173" {
		t.Fatalf("o opened %q", opened)
	}
	_, cmd := m.normalKey("enter")
	if !m.ui.Cur[pShells].detail || cmd == nil {
		t.Fatal("enter opens the shell and loads its output")
	}
	m.Update(cmd())
	out := stripANSI(m.render(time.Now()))
	if !strings.Contains(out, "ready") || !strings.Contains(out, "http://localhost:5173") {
		t.Fatalf("shell detail:\n%s", out)
	}
	m.normalKey("esc")
	if _, cmd := m.normalKey("d"); cmd != nil {
		t.Fatal("the first d only arms")
	}
	_, cmd = m.normalKey("d")
	if cmd == nil {
		t.Fatal("d d stops")
	}
	cmd()
	if ss, _ := d.Shells(context.Background()); len(ss) != 0 {
		t.Fatalf("still running: %+v", ss)
	}
	for _, size := range [][2]int{{200, 56}, {160, 44}, {100, 30}} {
		m.w, m.h = size[0], size[1]
		checkFrame(t, m.render(time.Now()), size[0], size[1], "shells")
	}
}

func TestAgentLiveToggle(t *testing.T) {
	m := newModel(context.Background(), Options{Backend: &Demo{}, State: demoState()})
	m.w, m.h = 200, 56
	m.st.Focus = "halo-opus-1"
	m.st.Agents["halo-opus-1"].Log = []string{"▸ Read a.go"}
	m.st.Feed(upd("halo-opus-1", "t", map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "hello from the raw stream"}}))
	press(m, "3")
	if !strings.Contains(stripANSI(m.render(time.Now())), "hello from the raw stream") {
		t.Fatal("the agent pane shows the live transcript")
	}
	press(m, "v")
	if out := stripANSI(m.render(time.Now())); strings.Contains(out, "hello from the raw stream") || !strings.Contains(out, "Read a.go") {
		t.Fatal("v shows the tool calls Lagrange saw")
	}
	press(m, "v", "enter")
	if !strings.Contains(stripANSI(m.render(time.Now())), "says") {
		t.Fatal("enter opens the entry")
	}
}
