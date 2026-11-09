package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/three-agent-problem/halo/internal/plan"
)

// paneBox draws panel id in a w×h box.
func (s *State) paneBox(id, w, h int, sc Screen) string {
	u := sc.UI
	focused := u.Focus == id
	size := sizeNormal
	if focused {
		size = u.Size[id]
	}
	title, right, lines := s.pane(id, w-4, h-2, size, focused, sc)
	num := c("dim", fmt.Sprintf("%d ", id))
	if focused {
		num = c("haiku!", fmt.Sprintf("%d ", id))
	}
	return box(w, h, num+title, right, lines, focused)
}

// pane returns a panel's title, right-hand note and lines for w×h cells inside
// the border.
func (s *State) pane(id, w, h int, size Size, focused bool, sc Screen) (string, string, []string) {
	u := sc.UI
	cur := u.Cur[id]
	if cur.detail {
		title, lines := s.detail(id, w, sc)
		if lines != nil {
			return title, c("dim", "j/k scroll · esc back"), scrollLines(lines, cur, h)
		}
		cur.detail = false
	}
	switch id {
	case pPlan:
		return s.planPane(w, h, size, focused, sc)
	case pOrbit:
		right := ""
		if size > sizeNormal {
			right = legend()
		}
		return c("hi!", "ORBIT") + c("dim", " · every message goes through lagrange"), right,
			strings.Split(s.orbit(w, h, sc.Now, sc.Frame), "\n")
	case pAgent:
		return s.agentPane(w, h, size, focused, sc)
	case pComms:
		return s.commsPane(w, h, size, focused, sc)
	case pCommits:
		return s.commitsPane(w, h, size, focused, sc)
	case pEvents:
		items := make([][]string, len(s.Raw))
		for i, r := range s.Raw {
			items[i] = []string{rawLine(r)}
		}
		return c("hi!", "EVENTS") + c("dim", " · GET /api/events"), live(sc.Connected), listLines(items, cur, h, focused, true)
	case pRoster:
		return s.rosterPane(w, h, size, focused, sc)
	}
	return "", "", nil
}

func legend() string {
	return c("task", "■ task ") + c("ask", "■ ask ") + c("reply", "■ reply ") + c("review", "■ review ") + c("fail", "■ fail")
}

// wrapLines word-wraps s to w cells and caps it at n lines (0: no cap), marking a
// cut with "…".
func wrapLines(s string, w, n int) []string {
	if w < 4 {
		w = 4
	}
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(s, "\t", "    "), "\n") {
		para = strings.TrimRight(para, " ")
		if para == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(ansi.Hardwrap(ansi.Wordwrap(para, w, " "), w, true), "\n")...)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	if n > 0 && len(out) > n {
		out = out[:n]
		out[n-1] = fit(out[n-1], w-1) + "…"
	}
	return out
}

// ── 1 plan ───────────────────────────────────────────────────────────────────

func (s *State) planPane(w, h int, size Size, focused bool, sc Screen) (string, string, []string) {
	title := c("hi!", "PLAN")
	if s.Plan == nil {
		if s.Root == "" {
			lines := []string{c("dim", "no run yet. press i and describe what to build.")}
			if s.Hint != "" {
				lines = append(lines, "")
				for _, l := range wrapLines(s.Hint, w, 3) {
					lines = append(lines, c("mute", l))
				}
			}
			return title, "", lines
		}
		return title, "", []string{c("mute", "planning… "+string(spin[sc.Frame%len(spin)])), "", c("dim", "› "+s.Prompt)}
	}
	done := 0
	for _, st := range s.Plan.Subtasks {
		if st.Status == plan.Done {
			done++
		}
	}
	right := c("dim", fmt.Sprintf("%d/%d done", done, len(s.Plan.Subtasks)))
	if s.Plan.Branch != "" {
		br := s.Plan.Branch
		if s.Plan.MergedInto != "" {
			br += " → " + s.Plan.MergedInto
		}
		right = c("mute", br) + c("dim", " · ") + right
	}
	goalN := 2
	if size > sizeNormal {
		goalN = 0
	}
	var head []string
	for _, l := range wrapLines(s.Plan.Goal, w-2, goalN) {
		head = append(head, c("line2", "│ ")+c("mute", l))
	}
	head = append(head, "")
	var foot []string
	if !s.Plan.Approved {
		foot = []string{"", c("ask", "waiting for your approval · press a")}
	}
	items := make([][]string, len(s.Plan.Subtasks))
	for i, st := range s.Plan.Subtasks {
		items[i] = []string{s.subLine(st, w-2, sc)}
		if size > sizeNormal {
			var bits []string
			if len(st.Deps) > 0 {
				bits = append(bits, "after "+strings.Join(st.Deps, ","))
			}
			if st.Worker != "" {
				bits = append(bits, s.Short(st.Worker))
			}
			if st.Attempts > 1 {
				bits = append(bits, fmt.Sprintf("attempt %d", st.Attempts))
			}
			if st.Note != "" {
				bits = append(bits, st.Note)
			}
			if len(bits) > 0 {
				for _, l := range wrapLines(strings.Join(bits, " · "), w-8, 2) {
					items[i] = append(items[i], "      "+c("dim", l))
				}
			}
		}
	}
	list := listLines(items, sc.UI.Cur[pPlan], h-len(head)-len(foot), focused, false)
	for len(list) < h-len(head)-len(foot) {
		list = append(list, "")
	}
	return title, right, append(append(head, list...), foot...)
}

func (s *State) subLine(st *plan.Subtask, w int, sc Screen) string {
	icon := statusIcon[st.Status]
	if st.Status == plan.Running {
		icon = string(spin[sc.Frame%len(spin)])
	}
	el := ""
	if st.StartedAt > 0 {
		end := sc.Now
		if st.EndedAt > 0 {
			end = time.UnixMilli(st.EndedAt)
		}
		el = mmss(end.Sub(time.UnixMilli(st.StartedAt)))
	}
	barKey := modelKey(st.Model)
	switch st.Status {
	case plan.Done:
		barKey = "reply"
	case plan.Failed, plan.Conflict:
		barKey = "fail"
	}
	extra := ""
	if st.Round > 0 {
		extra = c("review", fmt.Sprintf(" r%d", st.Round+1))
	}
	if st.Escalated {
		extra += c("ask", " ↑")
	}
	model := strings.ToUpper(st.Model[:min(1, len(st.Model))])
	return c(statusKey[st.Status]+"!", icon) + " " + c("mute", pad(st.ID, 3)) + " " + pad(st.Title, max(6, w-26)) + " " +
		c(modelKey(st.Model)+"!", model) + " " + c(barKey, bar(s.SubProgress(st, sc.Now), 8)) + " " + c("dim", pad(el, 5)) + extra
}

func (s *State) subDetail(st *plan.Subtask, w int, now time.Time) []string {
	row := func(k, v string) string { return c("dim", pad(k, 10)) + v }
	model := c(modelKey(st.Model)+"!", st.Model)
	if st.Escalated {
		model += c("ask", " (moved up)")
	}
	out := []string{
		c("hi!", st.ID+" · "+st.Title), "",
		row("status", c(statusKey[st.Status]+"!", st.Status)),
		row("model", model),
		row("worker", c("mute", nonEmpty(s.Short(st.Worker), "—"))),
	}
	if len(st.Deps) > 0 {
		out = append(out, row("after", strings.Join(st.Deps, ", ")))
	}
	if st.Attempts > 0 || st.Round > 0 {
		out = append(out, row("attempts", fmt.Sprintf("%d · review round %d", max(1, st.Attempts), st.Round+1)))
	}
	if st.StartedAt > 0 {
		end := now
		if st.EndedAt > 0 {
			end = time.UnixMilli(st.EndedAt)
		}
		out = append(out, row("time", mmss(end.Sub(time.UnixMilli(st.StartedAt)))))
	}
	for i, l := range wrapLines(st.Note, w-10, 0) {
		k := ""
		if i == 0 {
			k = "note"
		}
		out = append(out, row(k, c("mute", l)))
	}
	var commits []string
	for _, cm := range s.Commits {
		if cm.Sub == st.ID {
			commits = append(commits, c("hub", cm.Short())+" "+c("reply", fmt.Sprintf("+%d", cm.Add))+c("fail", fmt.Sprintf(" −%d", cm.Del))+" "+cm.Subject)
		}
	}
	if len(commits) > 0 {
		out = append(out, "", c("dim", "── commits (panel 5 shows the diffs)"))
		out = append(out, commits...)
	}
	out = append(out, "", c("dim", "── messages"))
	n := 0
	for _, m := range s.Comms {
		if m.Sub != st.ID {
			continue
		}
		n++
		out = append(out, "", s.commHead(m))
		for _, l := range wrapLines(m.Text, w-4, 0) {
			out = append(out, "    "+c("fg", l))
		}
	}
	if n == 0 {
		out = append(out, c("dim", "none yet"))
	}
	return out
}

// ── 3 agent ──────────────────────────────────────────────────────────────────

func (s *State) agentPane(w, h int, size Size, focused bool, sc Screen) (string, string, []string) {
	a := s.Agents[s.Focus]
	title := c("hi!", "AGENT")
	if a == nil {
		return title, "", []string{c("dim", "nobody yet")}
	}
	title += c("dim", " · ") + c(modelKey(a.Model)+"!", s.Short(a.Handle)) + c("dim", " "+a.Model)
	if a.Sub != "" && s.Plan != nil {
		if st := s.Plan.Get(a.Sub); st != nil {
			title += c("dim", " · "+st.ID+" "+st.Title)
		}
	}
	right := ""
	if focused {
		right = c("dim", "h/l agent")
	}
	var items [][]string
	for _, l := range a.Log {
		if tool, ok := strings.CutPrefix(l, "▸ "); ok {
			if size > sizeNormal {
				ls := wrapLines(tool, w-4, 4)
				item := []string{c(modelKey(a.Model), "▸") + " " + c("fg", ls[0])}
				for _, x := range ls[1:] {
					item = append(item, "  "+c("fg", x))
				}
				items = append(items, item)
			} else {
				items = append(items, []string{c(modelKey(a.Model), "▸") + " " + c("fg", tool)})
			}
		} else {
			n := 1
			if size > sizeNormal {
				n = 6
			}
			var item []string
			for _, x := range wrapLines(l, w-4, n) {
				item = append(item, "  "+c("hi", x))
			}
			items = append(items, item)
		}
	}
	var status []string
	switch a.State {
	case "working", "asking", "waiting":
		what := "working…"
		if a.State == "asking" {
			what = "waiting for an answer…"
		}
		if a.State == "waiting" {
			what = "waiting for you…"
		}
		status = []string{c(modelKey(a.Model)+"!", string(spin[sc.Frame%len(spin)])) + " " + c("dim", what) + " " + cursor(sc.Frame)}
	}
	if len(items) == 0 && len(status) == 0 {
		return title, right, []string{c("dim", "nothing yet")}
	}
	list := listLines(items, sc.UI.Cur[pAgent], h-len(status), focused, false)
	return title, right, append(list, status...)
}

// lastTask is the newest task an agent worked on.
func (s *State) lastTask(handle string) *Task {
	var best *Task
	for _, t := range s.Tasks {
		if t.Recipient == handle && (best == nil || t.Started.After(best.Started)) {
			best = t
		}
	}
	return best
}

// ── 4 comms ──────────────────────────────────────────────────────────────────

var commTag = map[string]string{"task": "TASK", "ask": "ASK", "reply": "REPLY", "review": "REVIEW", "fail": "FAIL", "hub": "LAGRANGE"}

func (s *State) comms(filter string) []Comm {
	if filter == "" {
		return s.Comms
	}
	var out []Comm
	for _, m := range s.Comms {
		if m.From == filter || m.To == filter {
			out = append(out, m)
		}
	}
	return out
}

// commHead is a message's time, kind, sender and recipient: 43 cells.
func (s *State) commHead(m Comm) string {
	ts := c("dim", m.At.Format("15:04:05")) + "  "
	if m.Kind == "hub" {
		return ts + c("hub!", pad("LAGRANGE", 33))
	}
	return ts + c(kindKey(m.Kind)+"!", pad(commTag[m.Kind], 7)) + " " +
		c(modelKey(s.model(m.From)), pad(s.Short(m.From), 11)) + c("dim", "→ ") +
		c(modelKey(s.model(m.To)), pad(s.Short(m.To), 11)) + " "
}

const commHeadW = 43

func (s *State) commsPane(w, h int, size Size, focused bool, sc Screen) (string, string, []string) {
	u := sc.UI
	cs := s.comms(u.Filter)
	title := c("hi!", "COMMS") + c("dim", " · agent ⇄ agent")
	if u.Filter != "" {
		title += c("dim", " · only ") + c(modelKey(s.model(u.Filter))+"!", s.Short(u.Filter))
	}
	right := c("dim", fmt.Sprintf("%d msgs", len(cs)))
	if focused {
		right = c("dim", "f filter · enter read · ") + right
	}
	items := make([][]string, len(cs))
	for i, m := range cs {
		fresh := i == len(cs)-1 && sc.Now.Sub(m.At) < 1500*time.Millisecond
		tk := "fg"
		if m.Kind == "hub" {
			tk = "hub"
		}
		if fresh {
			tk = "hi!"
		}
		head := s.commHead(m)
		if size == sizeNormal && w-2-commHeadW >= 24 {
			// Header and text on one line; the text wraps to a second line under itself.
			ls := wrapLines(m.Text, w-2-commHeadW, 2)
			item := []string{head + c(tk, ls[0])}
			for _, l := range ls[1:] {
				item = append(item, strings.Repeat(" ", commHeadW)+c(tk, l))
			}
			items[i] = item
			continue
		}
		n := 10
		if size == sizeNormal {
			n = 2
		}
		item := []string{head + c("dim", m.Sub)}
		for _, l := range wrapLines(m.Text, w-6, n) {
			item = append(item, "    "+c(tk, l))
		}
		items[i] = item
	}
	return title, right, listLines(items, u.Cur[pComms], h, focused, true)
}

// ── 5 commits ────────────────────────────────────────────────────────────────

func (s *State) commitsPane(w, h int, size Size, focused bool, sc Screen) (string, string, []string) {
	title := c("hi!", "COMMITS")
	right := c("dim", fmt.Sprintf("%d", len(s.Commits)))
	if focused {
		right = c("dim", "enter diff · ") + right
	}
	if len(s.Commits) == 0 {
		msg := "workers commit when they finish a subtask"
		if s.Root == "" {
			msg = "no run yet"
		}
		return title, right, []string{c("dim", msg)}
	}
	items := make([][]string, len(s.Commits))
	for i, cm := range s.Commits {
		who := ""
		if s.Plan != nil && cm.Sub != "" {
			if st := s.Plan.Get(cm.Sub); st != nil && st.Worker != "" {
				who = s.Short(st.Worker)
			}
		}
		key := "fg"
		if who != "" {
			key = modelKey(s.model(s.Prefix + "-" + strings.TrimPrefix(who, "@")))
		}
		merge := strings.HasPrefix(cm.Subject, "halo: merge") || strings.HasPrefix(cm.Subject, "Merge ")
		subject := strings.TrimPrefix(cm.Subject, "halo: ")
		icon := c(key+"!", "●")
		if merge {
			icon = c("hub!", "◆")
		}
		stat := c("reply", pad(fmt.Sprintf("+%d", cm.Add), 5)) + c("fail", pad(fmt.Sprintf("−%d", cm.Del), 5))
		line := icon + " " + c("hub", cm.Short()) + " " + stat + c("fg", subject)
		item := []string{line}
		if size > sizeNormal {
			bits := []string{ago(sc.Now.Sub(cm.At)), fmt.Sprintf("%d files", cm.Files)}
			if who != "" {
				bits = append(bits, who)
			}
			if cm.Refs != "" {
				bits = append(bits, cm.Refs)
			}
			item = append(item, "    "+c("dim", strings.Join(bits, " · ")))
		}
		items[i] = item
	}
	return title, right, listLines(items, sc.UI.Cur[pCommits], h, focused, false)
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}

// diffLines colors git show output.
func diffLines(out string, w int) []string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(out, "\t", "    "), "\n") {
		key := "fg"
		switch {
		case strings.HasPrefix(l, "commit "), strings.HasPrefix(l, "diff --git"):
			key = "hi!"
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "index "):
			key = "dim"
		case strings.HasPrefix(l, "+"):
			key = "reply"
		case strings.HasPrefix(l, "-"):
			key = "fail"
		case strings.HasPrefix(l, "@@"):
			key = "task"
		case strings.HasPrefix(l, "Author:"), strings.HasPrefix(l, "Date:"):
			key = "mute"
		}
		lines = append(lines, c(key, fit(l, w)))
	}
	return lines
}

// ── 6 events ─────────────────────────────────────────────────────────────────

func rawLine(r Raw) string {
	kk := map[string]string{"task": "task", "message": "ask", "artifact": "dim", "input": "review"}
	sk := "reply"
	switch {
	case r.State == "INPUT_REQ":
		sk = "ask"
	case r.State == "FAILED" || r.State == "REJECTED" || r.State == "CANCELED":
		sk = "fail"
	case r.State == "":
		sk = "dim"
	}
	return c("dim", fmt.Sprintf("%06d", r.Seq)) + " " + c(nonEmpty(kk[r.Kind], "dim"), pad(r.Kind, 8)) + " " +
		c("mute", pad(r.Who, 13)) + " " + c(sk, pad(r.State, 9)) + " " + c("dim", r.Text)
}

// ── 7 roster ─────────────────────────────────────────────────────────────────

func (s *State) rosterPane(w, h int, size Size, focused bool, sc Screen) (string, string, []string) {
	right := ""
	if focused {
		right = c("dim", "enter watch")
	}
	items := make([][]string, len(s.Order))
	for i, hd := range s.Order {
		x := s.Agents[hd]
		icon, k := "·", "dim"
		switch x.State {
		case "working":
			icon, k = string(spin[sc.Frame%len(spin)]), modelKey(x.Model)
		case "asking", "waiting":
			icon, k = "◌", "ask"
		case "failed":
			icon, k = "✗", "fail"
		}
		st := x.State
		if !x.Online && x.Role != "human" {
			icon, k, st = "○", "dim", "offline"
		}
		item := []string{c(k+"!", icon) + " " + c(modelKey(x.Model), pad(s.Short(hd), 12)) + c("dim", pad(x.Model, 8)) + c(k, pad(st, 9)) + " " + c("dim", x.Sub)}
		if size > sizeNormal {
			bits := []string{x.Role}
			if !x.LastAct.IsZero() {
				bits = append(bits, "active "+ago(sc.Now.Sub(x.LastAct)))
			}
			tools := 0
			for _, t := range s.Tasks {
				if t.Recipient == hd {
					tools += t.Tools
				}
			}
			if tools > 0 {
				bits = append(bits, fmt.Sprintf("%d tool calls", tools))
			}
			item = append(item, "    "+c("dim", strings.Join(bits, " · ")))
		}
		items[i] = item
	}
	return c("hi!", "ROSTER"), right, listLines(items, sc.UI.Cur[pRoster], h, focused, false)
}

// ── detail views ─────────────────────────────────────────────────────────────

// detail is the open item of a panel, or nil when it has none.
func (s *State) detail(id, w int, sc Screen) (string, []string) {
	u := sc.UI
	sel := u.Cur[id].sel
	switch id {
	case pPlan:
		if s.Plan == nil || sel >= len(s.Plan.Subtasks) {
			return "", nil
		}
		st := s.Plan.Subtasks[sel]
		return c("hi!", "PLAN") + c("dim", " › "+st.ID), s.subDetail(st, w, sc.Now)
	case pComms:
		cs := s.comms(u.Filter)
		if sel >= len(cs) {
			return "", nil
		}
		m := cs[sel]
		out := []string{s.commHead(m) + c("dim", m.Sub), ""}
		for _, l := range wrapLines(m.Text, w-2, 0) {
			out = append(out, "  "+c("fg", l))
		}
		return c("hi!", "COMMS") + c("dim", fmt.Sprintf(" › message %d of %d", sel+1, len(cs))), out
	case pCommits:
		if sel >= len(s.Commits) {
			return "", nil
		}
		cm := s.Commits[sel]
		d, ok := u.Diffs[cm.SHA]
		if !ok {
			return c("hi!", "COMMITS") + c("dim", " › "+cm.Short()), []string{c("dim", "loading the diff…")}
		}
		return c("hi!", "COMMITS") + c("dim", fmt.Sprintf(" › %s · %d of %d · [ ] prev/next", cm.Short(), sel+1, len(s.Commits))), diffLines(d, w)
	case pEvents:
		if sel >= len(s.Raw) {
			return "", nil
		}
		raw, _ := json.MarshalIndent(s.Raw[sel].E, "", "  ")
		var out []string
		for _, l := range strings.Split(string(raw), "\n") {
			out = append(out, wrapLines(l, w, 0)...)
		}
		return c("hi!", "EVENTS") + c("dim", fmt.Sprintf(" › #%d", s.Raw[sel].Seq)), out
	case pAgent:
		a := s.Agents[s.Focus]
		if a == nil {
			return "", nil
		}
		t := s.lastTask(a.Handle)
		if t == nil {
			return "", nil
		}
		out := []string{c("dim", "from ") + c(modelKey(s.model(t.Sender)), s.Short(t.Sender)) + c("dim", " · "+short(t.State)), ""}
		for _, l := range wrapLines(t.Summary, w-2, 0) {
			out = append(out, "  "+c("mute", l))
		}
		out = append(out, "", c("dim", "── answer"))
		ans := t.Answer()
		if ans == "" {
			ans = "(nothing yet)"
		}
		for _, l := range wrapLines(ans, w-2, 0) {
			out = append(out, "  "+c("fg", l))
		}
		return c("hi!", "AGENT") + c("dim", " › "+s.Short(a.Handle)+"'s latest task"), out
	}
	return "", nil
}

// Count is how many items panel id lists, for moving its selection.
func (s *State) Count(id int, u *UI) int {
	switch id {
	case pPlan:
		if s.Plan != nil {
			return len(s.Plan.Subtasks)
		}
	case pAgent:
		if a := s.Agents[s.Focus]; a != nil {
			return len(a.Log)
		}
	case pComms:
		return len(s.comms(u.Filter))
	case pCommits:
		return len(s.Commits)
	case pEvents:
		return len(s.Raw)
	case pRoster:
		return len(s.Order)
	}
	return 0
}

// handles lists who appears in the comms, for the filter.
func (s *State) commHandles() []string {
	seen := map[string]bool{}
	for _, m := range s.Comms {
		for _, h := range []string{m.From, m.To} {
			if h != "" {
				seen[h] = true
			}
		}
	}
	var out []string
	for _, h := range s.Order {
		if seen[h] {
			out = append(out, h)
			delete(seen, h)
		}
	}
	var rest []string
	for h := range seen {
		rest = append(rest, h)
	}
	sort.Strings(rest)
	return append(out, rest...)
}
