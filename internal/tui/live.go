package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/three-agent-problem/halo/internal/shells"
)

// ── raw agent output ─────────────────────────────────────────────────────────

// Update is one raw ACP session update from an agent this Halo hosts: what
// Claude Code streams while it works, before Lagrange sees any of it.
type Update struct {
	Agent, Task, Type string
	Raw               json.RawMessage
	At                time.Time
}

// Block is one entry of an agent's live transcript.
type Block struct {
	Kind    string // "user", "think", "say", "tool", "plan"
	Task    string
	Text    string
	ID      string // the tool call
	Title   string
	Tool    string // the ACP tool kind: execute, read, edit, search, fetch, …
	Status  string // pending, in_progress, completed, failed
	Input   json.RawMessage
	Output  string
	Started time.Time
	Ended   time.Time
	Entries []PlanEntry
}

// PlanEntry is an item of the agent's own todo list.
type PlanEntry struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

const (
	maxBlocks = 600
	maxText   = 48 << 10
)

type acpUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content"`
	ToolCallID    string          `json:"toolCallId"`
	Title         string          `json:"title"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	RawInput      json.RawMessage `json:"rawInput"`
	RawOutput     json.RawMessage `json:"rawOutput"`
	Entries       []PlanEntry     `json:"entries"`
}

var chunkKind = map[string]string{"agent_message_chunk": "say", "agent_thought_chunk": "think", "user_message_chunk": "user"}

// Feed folds a raw update into the agent's transcript: chunks join the block
// before them, tool call updates change the call they belong to.
func (s *State) Feed(u Update) {
	a := s.Agents[u.Agent]
	if a == nil {
		return
	}
	var x acpUpdate
	if json.Unmarshal(u.Raw, &x) != nil {
		return
	}
	at := u.At
	if at.IsZero() {
		at = time.Now()
	}
	if a.tools == nil {
		a.tools = map[string]*Block{}
	}
	last := func() *Block {
		if len(a.Feed) == 0 {
			return nil
		}
		return a.Feed[len(a.Feed)-1]
	}
	switch x.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk", "user_message_chunk":
		kind, text := chunkKind[x.SessionUpdate], contentText(x.Content)
		if text == "" {
			return
		}
		if b := last(); b != nil && b.Kind == kind && b.Task == u.Task {
			b.Text, b.Ended = capTail(b.Text+text), at
			return
		}
		a.push(&Block{Kind: kind, Task: u.Task, Text: capTail(text), Started: at, Ended: at})
	case "tool_call", "tool_call_update":
		if x.ToolCallID == "" {
			return
		}
		b := a.tools[x.ToolCallID]
		if b == nil {
			b = &Block{Kind: "tool", Task: u.Task, ID: x.ToolCallID, Status: "pending", Started: at}
			a.tools[x.ToolCallID] = b
			a.push(b)
		}
		if x.Title != "" {
			b.Title = x.Title
		}
		if x.Kind != "" {
			b.Tool = x.Kind
		}
		if in := bytes.TrimSpace(x.RawInput); len(in) > 0 && string(in) != "null" && string(in) != "{}" {
			b.Input = append(json.RawMessage(nil), in...)
		}
		if x.Status != "" {
			b.Status = x.Status
			if x.Status == "completed" || x.Status == "failed" {
				b.Ended = at
			}
		}
		if out := toolText(x.Content); out != "" {
			b.Output = capTail(out)
		} else if b.Output == "" {
			b.Output = capTail(rawText(x.RawOutput))
		}
	case "plan":
		if b := last(); b != nil && b.Kind == "plan" && b.Task == u.Task {
			b.Entries, b.Ended = x.Entries, at
			return
		}
		a.push(&Block{Kind: "plan", Task: u.Task, Entries: x.Entries, Started: at, Ended: at})
	default:
		return
	}
	a.LastAct = at
}

func (a *Agent) push(b *Block) {
	a.Feed = append(a.Feed, b)
	if n := len(a.Feed) - maxBlocks; n > 0 {
		for _, old := range a.Feed[:n] {
			if old.ID != "" {
				delete(a.tools, old.ID)
			}
		}
		a.Feed = append([]*Block(nil), a.Feed[n:]...)
	}
}

func capTail(s string) string {
	if len(s) > maxText {
		s = s[len(s)-maxText:]
	}
	return s
}

// clean makes program output printable: no escape sequences, no carriage
// returns, tabs as spaces.
func clean(s string) string {
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if strings.Contains(s, "\r") {
		ls := strings.Split(s, "\n")
		for i, l := range ls {
			if j := strings.LastIndexByte(l, '\r'); j >= 0 {
				ls[i] = l[j+1:]
			}
		}
		s = strings.Join(ls, "\n")
	}
	return strings.ReplaceAll(s, "\t", "    ")
}

// contentText is the text of an ACP content block.
func contentText(raw json.RawMessage) string {
	var cb struct {
		Type, Text, Name, URI string
	}
	if json.Unmarshal(raw, &cb) != nil {
		return ""
	}
	switch cb.Type {
	case "text":
		return cb.Text
	case "image", "audio":
		return "[" + cb.Type + "]"
	case "resource_link", "resource":
		return "[" + nonEmpty(cb.Name, cb.URI, cb.Type) + "]"
	}
	return ""
}

// toolText is what a tool call's content shows: text, diffs, terminals.
func toolText(raw json.RawMessage) string {
	var items []struct {
		Type       string          `json:"type"`
		Content    json.RawMessage `json:"content"`
		Path       string          `json:"path"`
		OldText    *string         `json:"oldText"`
		NewText    string          `json:"newText"`
		TerminalID string          `json:"terminalId"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var parts []string
	for _, it := range items {
		switch it.Type {
		case "content":
			if t := unfence(contentText(it.Content)); t != "" {
				parts = append(parts, t)
			}
		case "diff":
			var b strings.Builder
			b.WriteString("@@ " + it.Path + "\n")
			if it.OldText != nil && *it.OldText != "" {
				for _, l := range strings.Split(strings.TrimRight(*it.OldText, "\n"), "\n") {
					b.WriteString("-" + l + "\n")
				}
			}
			for _, l := range strings.Split(strings.TrimRight(it.NewText, "\n"), "\n") {
				b.WriteString("+" + l + "\n")
			}
			parts = append(parts, strings.TrimRight(b.String(), "\n"))
		case "terminal":
			parts = append(parts, "[terminal "+it.TerminalID+"]")
		}
	}
	return strings.Join(parts, "\n")
}

// unfence drops the ``` fence Claude Code puts around command output.
func unfence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return s
	}
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[i+1:]
	} else {
		return ""
	}
	return strings.TrimRight(strings.TrimSuffix(strings.TrimRight(t, "\n "), "```"), "\n")
}

func rawText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return unfence(s)
	}
	var v struct {
		Output, Stdout, Stderr, Content string
	}
	if json.Unmarshal(raw, &v) == nil {
		if t := strings.TrimSpace(strings.Join([]string{v.Output, v.Stdout, v.Stderr, v.Content}, "\n")); t != "" {
			return t
		}
	}
	return string(raw)
}

var claudeTools = map[string]bool{"Bash": true, "Read": true, "Write": true, "Edit": true, "MultiEdit": true, "Glob": true, "Grep": true,
	"LS": true, "WebFetch": true, "WebSearch": true, "Task": true, "TodoWrite": true, "NotebookEdit": true, "BashOutput": true, "KillShell": true}

var toolLabel = map[string]string{"execute": "Bash", "read": "Read", "edit": "Edit", "delete": "Delete", "move": "Move",
	"search": "Search", "fetch": "Fetch", "think": "Think", "switch_mode": "Mode"}

// input reads a string field of the tool call's input.
func (b *Block) input(key string) string {
	var m map[string]any
	if json.Unmarshal(b.Input, &m) != nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

// head is how a tool call is named: Bash go test ./..., Read app.go, …
func (b *Block) head() (string, string) {
	title := strings.Trim(strings.TrimSpace(b.Title), "`")
	label := toolLabel[b.Tool]
	if b.Tool == "execute" {
		if cmd := b.input("command"); cmd != "" {
			title = cmd
		}
	}
	// Claude Code names its tools in the title: "Glob **/*.go", "Grep pattern".
	if name, rest, ok := strings.Cut(title, " "); ok && claudeTools[name] {
		return name, rest
	}
	if label == "" {
		label, title = "Tool", nonEmpty(title, b.ID)
		if name, rest, ok := strings.Cut(title, " "); ok && strings.HasPrefix(name, "mcp__") {
			label, title = "MCP", strings.TrimPrefix(name, "mcp__")+" "+rest
		} else if strings.HasPrefix(title, "mcp__") {
			label, title = "MCP", strings.ReplaceAll(strings.TrimPrefix(title, "mcp__"), "__", " ")
		}
	} else if rest, ok := strings.CutPrefix(title, label+" "); ok {
		title = rest
	}
	return label, title
}

// background reports whether the call started a shell that keeps running.
func (b *Block) background() bool {
	var m struct {
		Bg bool `json:"run_in_background"`
	}
	return json.Unmarshal(b.Input, &m) == nil && m.Bg
}

func (b *Block) running() bool { return b.Status == "pending" || b.Status == "in_progress" }

// live reports whether the agent pane shows the raw transcript.
func (s *State) live(a *Agent, u *UI) bool { return a != nil && len(a.Feed) > 0 && !u.Calls }

// transcript lays out an agent's blocks as list items.
func (s *State) transcript(a *Agent, w int, size Size, sc Screen) [][]string {
	grown := size > sizeNormal
	n := func(normal, big int) int {
		if grown {
			return big
		}
		return normal
	}
	mk := modelKey(a.Model)
	var items [][]string
	for _, b := range a.Feed {
		var item []string
		switch b.Kind {
		case "user":
			for i, l := range wrapLines(clean(b.Text), w-2, n(1, 4)) {
				item = append(item, pick(i == 0, c("hub!", "› "), "  ")+c("mute", l))
			}
		case "think":
			for i, l := range wrapLines(clean(b.Text), w-2, n(2, 8)) {
				item = append(item, pick(i == 0, c("dim", "✻ "), "  ")+c("dim", l))
			}
		case "say":
			for i, l := range wrapLines(clean(b.Text), w-2, n(3, 14)) {
				item = append(item, pick(i == 0, c(mk+"!", "⏺ "), "  ")+c("fg", l))
			}
		case "plan":
			item = append(item, c(mk+"!", "≡ ")+c("hi!", "todo"))
			for i, e := range b.Entries {
				if i == n(4, 20) {
					item = append(item, c("dim", fmt.Sprintf("    … %d more", len(b.Entries)-i)))
					break
				}
				icon, k := "☐", "dim"
				switch e.Status {
				case "completed":
					icon, k = "☑", "reply"
				case "in_progress":
					icon, k = "◐", mk
				}
				item = append(item, "  "+c(k+"!", icon)+" "+c(pick(e.Status == "completed", "dim", "fg"), fit(e.Content, w-5)))
			}
		case "tool":
			item = s.toolItem(b, w, n(1, 6), grown, sc)
		}
		if len(item) > 0 {
			items = append(items, item)
		}
	}
	return items
}

func (s *State) toolItem(b *Block, w, outN int, grown bool, sc Screen) []string {
	icon, k := string(spin[sc.Frame%len(spin)]), "task"
	switch b.Status {
	case "completed":
		icon, k = "✓", "reply"
	case "failed":
		icon, k = "✗", "fail"
	case "pending":
		icon, k = "◌", "dim"
	}
	label, title := b.head()
	took := ""
	switch {
	case b.background():
		took = "bg → 8"
	case !b.Ended.IsZero():
		took = dur(b.Ended.Sub(b.Started))
	case b.running():
		took = dur(sc.Now.Sub(b.Started))
	}
	head := c(k+"!", icon) + " " + c("hi!", label) + " "
	room := w - 2 - len(label) - 1 - len(took) - 1
	item := []string{head + c("fg", pad(fit(firstLine(title), room), room)) + " " + c("dim", took)}
	if desc := b.input("description"); grown && desc != "" {
		item = append(item, "    "+c("dim", fit("# "+desc, w-4)))
	}
	out := strings.Split(strings.TrimRight(clean(b.Output), "\n"), "\n")
	if len(out) == 1 && out[0] == "" {
		out = nil
	}
	if len(out) == 0 && b.running() {
		return append(item, c("dim", "  ⎿ running…"))
	}
	for i, l := range out {
		if i == outN {
			item = append(item, c("dim", fmt.Sprintf("    … +%d lines · enter", len(out)-i)))
			break
		}
		item = append(item, c("dim", pick(i == 0, "  ⎿ ", "    "))+c(outKey(l), fit(l, w-4)))
	}
	return item
}

func outKey(l string) string {
	switch {
	case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
		return "reply"
	case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
		return "fail"
	case strings.HasPrefix(l, "@@"):
		return "hub"
	case strings.HasPrefix(l, "FAIL") || strings.HasPrefix(l, "--- FAIL") || strings.Contains(l, "error"):
		return "fail"
	}
	return "mute"
}

func dur(d time.Duration) string {
	switch {
	case d < 0:
		return ""
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return mmss(d)
}

func pick(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

// blockDetail is an open transcript entry: all of it.
func (s *State) blockDetail(a *Agent, b *Block, w int) []string {
	var out []string
	text := func(k, t string) {
		for _, l := range strings.Split(clean(t), "\n") {
			for _, x := range wrapLines(l, w-2, 0) {
				out = append(out, "  "+c(k, x))
			}
		}
	}
	when := b.Started.Format("15:04:05")
	if b.Task != "" {
		when += " · task " + b.Task
	}
	switch b.Kind {
	case "tool":
		label, title := b.head()
		out = append(out, c("hi!", label)+" "+c("fg", firstLine(title)), c("dim", when+" · "+nonEmpty(b.Status, "?")+pick(b.Ended.IsZero(), "", " · "+dur(b.Ended.Sub(b.Started)))), "")
		if desc := b.input("description"); desc != "" {
			out = append(out, c("dim", "  # "+desc), "")
		}
		if len(b.Input) > 0 {
			out = append(out, c("dim", "── input"))
			var pretty bytes.Buffer
			if json.Indent(&pretty, b.Input, "", "  ") != nil {
				pretty.Reset()
				pretty.Write(b.Input)
			}
			text("mute", pretty.String())
			out = append(out, "")
		}
		out = append(out, c("dim", "── output"))
		if strings.TrimSpace(b.Output) == "" {
			out = append(out, c("dim", pick(b.running(), "  running…", "  (none)")))
		}
		for _, l := range strings.Split(clean(b.Output), "\n") {
			for _, x := range wrapLines(l, w-2, 0) {
				out = append(out, "  "+c(outKey(l), x))
			}
		}
	case "plan":
		out = append(out, c("hi!", "todo")+c("dim", " · "+when), "")
		for _, e := range b.Entries {
			out = append(out, "  "+c("fg", pick(e.Status == "completed", "☑ ", pick(e.Status == "in_progress", "◐ ", "☐ "))+e.Content))
		}
	default:
		name := map[string]string{"user": "prompt", "think": "thinking", "say": "says"}[b.Kind]
		out = append(out, c(modelKey(a.Model)+"!", s.Short(a.Handle))+c("dim", " "+name+" · "+when), "")
		text(pick(b.Kind == "think", "dim", "fg"), b.Text)
	}
	return out
}

// ── shells ───────────────────────────────────────────────────────────────────

// Shell is a command an agent runs, or a server it left listening.
type Shell = shells.Shell

// Procs reads the hosted agents' processes. Only the Halo that hosts the team
// has them.
type Procs interface {
	Shells(ctx context.Context) ([]Shell, error)
	// Output is the end of a shell's output file.
	Output(ctx context.Context, path string) (string, error)
	// Stop ends a shell and everything it started.
	Stop(ctx context.Context, pid int) error
}

type (
	shellsMsg []Shell
	outMsg    struct {
		pid  int
		text string
	}
)

func (m *model) fetchShells() tea.Cmd {
	if m.o.Procs == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		defer cancel()
		ss, err := m.o.Procs.Shells(ctx)
		if err != nil {
			return nil
		}
		return shellsMsg(ss)
	}
}

// wantOutput reads the output of the open shell, or of the selected one when
// the panel is grown.
func (m *model) wantOutput() tea.Cmd {
	c := m.ui.Cur[pShells]
	if m.o.Procs == nil || c.sel >= len(m.st.Shells) || (!c.detail && m.ui.Size[pShells] == sizeNormal) {
		return nil
	}
	sh := m.st.Shells[c.sel]
	if sh.Out == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 3*time.Second)
		defer cancel()
		text, err := m.o.Procs.Output(ctx, sh.Out)
		if err != nil {
			text = "can't read " + sh.Out + ": " + err.Error()
		}
		return outMsg{sh.PID, text}
	}
}

// shellTitle is what the agent said the command does (the Bash tool's
// description), else the command.
func (s *State) shellTitle(sh Shell) string {
	if a := s.Agents[sh.Agent]; a != nil {
		for i := len(a.Feed) - 1; i >= 0; i-- {
			b := a.Feed[i]
			if b.Kind == "tool" && b.Tool == "execute" && strings.TrimSpace(b.input("command")) == sh.Command {
				if d := b.input("description"); d != "" {
					return d
				}
				break
			}
		}
	}
	return firstLine(sh.Command)
}

func ports(sh Shell) string {
	var ps []string
	for _, p := range sh.Ports {
		ps = append(ps, fmt.Sprintf(":%d", p))
	}
	return strings.Join(ps, " ")
}

func shortPath(p string) string {
	parts := strings.Split(filepath.ToSlash(p), "/")
	if len(parts) > 3 {
		return "…/" + strings.Join(parts[len(parts)-2:], "/")
	}
	return p
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimRight(clean(s), "\n "), "\n")
	return strings.TrimSpace(ls[len(ls)-1])
}

func (s *State) shellsPane(w, h int, size Size, focused bool, sc Screen) (string, string, []string) {
	title := c("hi!", "SHELLS")
	right := ""
	if n := len(s.Shells); n > 0 {
		right = c("dim", fmt.Sprintf("%d running", n))
	}
	if focused && len(s.Shells) > 0 {
		right = c("dim", "enter output · o open · d d stop")
	}
	if !sc.Procs {
		return title, "", []string{c("dim", "shells show in the Halo that hosts the team")}
	}
	if len(s.Shells) == 0 {
		lines := []string{c("dim", "no shells running")}
		if size > sizeNormal {
			lines = append(lines, "", c("dim", "commands the agents run and servers they start show here, with their ports and live output"))
		}
		return title, right, lines
	}
	w -= 2 // listLines' selection mark
	items := make([][]string, len(s.Shells))
	for i, sh := range s.Shells {
		a := s.Agents[sh.Agent]
		mk := "dim"
		if a != nil {
			mk = modelKey(a.Model)
		}
		icon := string(spin[(sc.Frame+i*3)%len(spin)])
		if len(sh.Ports) > 0 {
			icon = "◉"
		}
		p := ports(sh)
		if p != "" {
			p += " "
		}
		name := pad(s.Short(sh.Agent), 10)
		up := ago(sc.Now.Sub(sh.Started))
		room := w - 2 - 11 - len(p) - len(up) - 1
		item := []string{c(mk+"!", icon) + " " + c(mk, name) + " " + c("hub!", p) + c("fg", pad(fit(s.shellTitle(sh), room), room)) + " " + c("dim", up)}
		if size > sizeNormal {
			item = append(item, "    "+c("mute", fit("$ "+strings.ReplaceAll(sh.Command, "\n", " ⏎ "), w-4)))
			meta := fmt.Sprintf("pid %d · %d proc", sh.PID, sh.Procs)
			if sh.Kind == "server" {
				meta += " · exec'd server"
			}
			if sh.Cwd != "" {
				meta += " · " + shortPath(sh.Cwd)
			}
			item = append(item, "    "+c("dim", fit(meta, w-4)))
			if out := sc.UI.Out[sh.PID]; out != "" && i == sc.UI.Cur[pShells].sel {
				item = append(item, "    "+c("mute", "⎿ ")+c("fg", fit(lastLine(out), w-6)))
			}
		}
		items[i] = item
	}
	return title, right, listLines(items, sc.UI.Cur[pShells], h, focused, false)
}

func (s *State) shellDetail(sh Shell, w, h int, u *UI) []string {
	var out []string
	mk := "dim"
	if a := s.Agents[sh.Agent]; a != nil {
		mk = modelKey(a.Model)
	}
	out = append(out, c("hi!", fit(s.shellTitle(sh), w)))
	for _, l := range strings.Split(sh.Command, "\n") {
		for i, x := range wrapLines(l, w-4, 0) {
			out = append(out, c("hub!", pick(i == 0, "$ ", "  "))+c("fg", x))
		}
	}
	out = append(out, c(mk+"!", s.Short(sh.Agent))+c("dim", fmt.Sprintf(" · pid %d · %d proc · up %s", sh.PID, sh.Procs, ago(time.Since(sh.Started)))))
	if sh.Cwd != "" {
		out = append(out, c("dim", "cwd ")+c("mute", fit(sh.Cwd, w-4)))
	}
	for _, p := range sh.Ports {
		out = append(out, c("dim", "port ")+c("hub!", fmt.Sprintf("http://localhost:%d", p)))
	}
	out = append(out, "")
	switch text, ok := u.Out[sh.PID]; {
	case sh.Out == "":
		out = append(out, c("dim", "── output goes to a terminal or pipe, not a file Halo can read"))
	case !ok:
		out = append(out, c("dim", "── output · loading…"))
	default:
		out = append(out, c("dim", "── output · live · "+shortPath(sh.Out)))
		body := strings.Split(strings.TrimRight(clean(text), "\n"), "\n")
		for _, l := range body {
			for _, x := range wrapLines(l, w-2, 0) {
				out = append(out, "  "+c(outKey(l), x))
			}
		}
	}
	return out
}

// shellKey handles the shells panel's keys.
func (m *model) shellKey(key string) (tea.Cmd, bool) {
	c := m.ui.Cur[pShells]
	if c.sel >= len(m.st.Shells) {
		return nil, false
	}
	sh := m.st.Shells[c.sel]
	switch key {
	case "o":
		if len(sh.Ports) == 0 {
			m.say("that shell doesn't listen on a port")
			return nil, true
		}
		url := fmt.Sprintf("http://localhost:%d", sh.Ports[0])
		if err := openURL(url); err != nil {
			m.say("can't open " + url + ": " + err.Error())
		} else {
			m.say("opened " + url)
		}
		return nil, true
	case "d":
		arm := fmt.Sprint(sh.PID)
		if m.deleteArm != arm || time.Since(m.deleteAt) > 3*time.Second {
			m.deleteArm, m.deleteAt = arm, time.Now()
			m.say(fmt.Sprintf("press d again to stop %s (pid %d) and what it started", fit(m.st.shellTitle(sh), 40), sh.PID))
			return nil, true
		}
		m.deleteArm = ""
		procs := m.o.Procs
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
			defer cancel()
			return doneMsg{fmt.Sprintf("stopped pid %d", sh.PID), procs.Stop(ctx, sh.PID)}
		}, true
	}
	return nil, false
}

// openURL opens url in the browser.
var openURL = func(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, url).Start()
}
