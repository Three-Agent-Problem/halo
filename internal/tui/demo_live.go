package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

// The demo's agents also stream raw output and run shells, like hosted ones.

// Updates is the demo's raw agent output, for Options.Feed.
func (d *Demo) Updates() <-chan Update {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.feed == nil {
		d.feed = make(chan Update, 4096)
	}
	return d.feed
}

// Shells implements Procs.
func (d *Demo) Shells(context.Context) ([]Shell, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Shell(nil), d.shells...), nil
}

// Output implements Procs.
func (d *Demo) Output(_ context.Context, path string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.outs[path], nil
}

// Stop implements Procs.
func (d *Demo) Stop(_ context.Context, pid int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, sh := range d.shells {
		if sh.PID == pid {
			d.shells = append(d.shells[:i], d.shells[i+1:]...)
			return nil
		}
	}
	return errors.New("no such shell")
}

func (d *Demo) raw(agent, task string, v map[string]any) {
	d.mu.Lock()
	feed := d.feed
	d.mu.Unlock()
	if feed == nil {
		return
	}
	raw, _ := json.Marshal(v)
	select {
	case feed <- Update{Agent: agent, Task: task, Type: v["sessionUpdate"].(string), Raw: raw, At: time.Now()}:
	default:
	}
}

func (d *Demo) chunk(agent, task, kind, text string) {
	words := strings.Fields(text)
	for i := 0; i < len(words); i += 3 {
		part := strings.Join(words[i:min(i+3, len(words))], " ")
		if i > 0 {
			part = " " + part
		}
		d.raw(agent, task, map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": part}})
	}
}

// startShell adds a running shell with output at a fake file.
func (d *Demo) startShell(agent, command string, ports ...int) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pid++
	pid := 41000 + d.pid
	out := fmt.Sprintf("/tmp/claude/tasks/demo%d.output", pid)
	if d.outs == nil {
		d.outs = map[string]string{}
	}
	d.outs[out] = ""
	cwd := "/Users/kaan/Library/Caches/halo/app-3f2a1c/" + strings.TrimPrefix(agent, d.Prefix+"-")
	d.shells = append(d.shells, Shell{PID: pid, Agent: agent, Command: command, Kind: "shell", Cwd: cwd, Started: time.Now(), Ports: ports, Out: out, Procs: 2 + len(ports)})
	return pid
}

func (d *Demo) write(pid int, text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, sh := range d.shells {
		if sh.PID == pid {
			d.outs[sh.Out] += text
		}
	}
}

func (d *Demo) endShell(pid int) { _ = d.Stop(context.Background(), pid) }

var demoOut = map[string]string{
	"go test":     "ok  \tgithub.com/acme/app/store\t0.84s\nok  \tgithub.com/acme/app/api\t1.21s\nok  \tgithub.com/acme/app/platform\t2.07s",
	"go vet":      "",
	"go run":      "migrate: 0007_tasks.sql (dry run)\n  CREATE TABLE tasks (id uuid PRIMARY KEY, context_id text NOT NULL, …)\n  CREATE TABLE task_events (…)\nok, nothing applied",
	"npm test":    " ✓ src/routes.test.tsx (6 tests) 88ms\n ✓ src/TaskForm.test.tsx (25 tests) 412ms\n\n Test Files  4 passed (4)\n      Tests  31 passed (31)\n   Duration  1.94s",
	"git diff":    "diff --git a/store/pg.go b/store/pg.go\n@@ -40,6 +40,18 @@ func (s *PG) Next(ctx context.Context) (*Task, error) {\n+\trow := s.db.QueryRowContext(ctx, `SELECT id FROM tasks\n+\t\tWHERE state = 'queued' ORDER BY created_at\n+\t\tFOR UPDATE SKIP LOCKED LIMIT 1`)\n-\treturn nil, nil",
	"Read":        "package store\n\nimport (\n\t\"context\"\n\t\"time\"\n)\n\n// Store keeps tasks.\ntype Store interface {\n\tPut(ctx context.Context, t *Task) error\n\tNext(ctx context.Context) (*Task, error)\n}",
	"Grep":        "store/store.go:9:type Store interface {\nstore/memory.go:14:var _ Store = (*Memory)(nil)",
	"Glob":        "store/store.go\nstore/memory.go\napi/routes.go\nplatform/api.go\ncmd/app/main.go",
	"mcp__halo__": `{"ok":true}`,
}

func demoOutput(tool string) string {
	for k, v := range demoOut {
		if strings.HasPrefix(tool, k) || strings.HasPrefix(strings.TrimPrefix(tool, "Bash "), k) {
			return v
		}
	}
	return ""
}

var demoKind = map[string]string{"Bash": "execute", "Read": "read", "Write": "edit", "Edit": "edit", "Grep": "search", "Glob": "search"}

// tool plays one tool call of agent over step: the call, its shell if it is a
// command, its output.
func (d *Demo) tool(ctx context.Context, agent, task, tool string, step time.Duration) {
	label, arg, _ := strings.Cut(tool, " ")
	kind := demoKind[label]
	if kind == "" {
		kind = "other"
	}
	if rand.IntN(3) == 0 {
		d.chunk(agent, task, "agent_thought_chunk", "Next I need to "+strings.ToLower(label)+" "+arg+" to check how this fits with what is there.")
	}
	id := fmt.Sprintf("toolu_%08x", rand.Uint32())
	input := map[string]any{}
	title := tool
	switch kind {
	case "execute":
		input = map[string]any{"command": arg, "description": demoDescribe(arg)}
		title = "`" + arg + "`"
	case "read", "edit":
		input = map[string]any{"file_path": arg}
	case "search":
		input = map[string]any{"pattern": arg}
	}
	d.raw(agent, task, map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": title, "kind": kind, "status": "in_progress", "rawInput": input})
	out := demoOutput(tool)
	pid := 0
	if kind == "execute" {
		pid = d.startShell(agent, arg)
		for _, l := range strings.Split(out, "\n") {
			sleep(ctx, step/time.Duration(len(strings.Split(out, "\n"))+1))
			d.write(pid, l+"\n")
		}
	} else {
		sleep(ctx, step/2)
	}
	content := []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "```\n" + out + "\n```"}}}
	if kind == "edit" {
		content = []any{map[string]any{"type": "diff", "path": arg, "oldText": "\treturn nil", "newText": "\tif err := validate(in); err != nil {\n\t\treturn fmt.Errorf(\"invalid input: %w\", err)\n\t}\n\treturn store.Save(ctx, in)"}}
	}
	if pid != 0 {
		d.endShell(pid)
	}
	d.raw(agent, task, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "completed", "content": content})
}

func demoDescribe(cmd string) string {
	switch {
	case strings.HasPrefix(cmd, "go test"):
		return "Run the Go tests"
	case strings.HasPrefix(cmd, "npm test"):
		return "Run the web tests"
	case strings.HasPrefix(cmd, "git diff"):
		return "Show the subtask's changes"
	case strings.HasPrefix(cmd, "go vet"):
		return "Vet every package"
	case strings.HasPrefix(cmd, "npm run dev"):
		return "Start the web dev server"
	}
	return "Run " + strings.Fields(cmd)[0]
}

// devServer runs `npm run dev` for agent until stop closes, logging requests.
func (d *Demo) devServer(ctx context.Context, agent, task string, stop <-chan struct{}) {
	cmd := "npm run dev -- --port 5173"
	id := fmt.Sprintf("toolu_%08x", rand.Uint32())
	d.raw(agent, task, map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": "`" + cmd + "`", "kind": "execute", "status": "in_progress",
		"rawInput": map[string]any{"command": cmd, "description": "Start the web dev server", "run_in_background": true}})
	pid := d.startShell(agent, cmd, 5173)
	d.write(pid, "\n> web@0.1.0 dev\n> vite --port 5173\n\n\n  VITE v6.3.5  ready in 412 ms\n\n  ➜  Local:   http://localhost:5173/\n  ➜  Network: use --host to expose\n")
	d.raw(agent, task, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "completed",
		"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "Command running in background with ID: bash_1"}}}})
	paths := []string{"/", "/tasks", "/api/tasks", "/src/TaskForm.tsx", "/@vite/client"}
	for {
		select {
		case <-stop:
			d.endShell(pid)
			return
		case <-ctx.Done():
			d.endShell(pid)
			return
		case <-time.After(time.Duration(700+rand.IntN(1500)) * time.Millisecond):
			p := paths[rand.IntN(len(paths))]
			line := fmt.Sprintf("%s [vite] %s %s %d\n", time.Now().Format("15:04:05"), pick(strings.HasPrefix(p, "/api"), "proxy", "page reload"), p, 200)
			if rand.IntN(4) == 0 {
				line = time.Now().Format("15:04:05") + " [vite] hmr update /src/TaskForm.tsx\n"
			}
			d.write(pid, line)
		}
	}
}
