package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/three-agent-problem/halo/internal/plan"
)

// The plan panel grown: a kanban of the subtasks at column and wide size, and a
// timeline of who worked when at full size. Both show what each worker is doing
// right now, read from its live transcript.

// boardCols are the kanban's columns, left to right, and the statuses in each.
var boardCols = []struct {
	name, key string
	statuses  []string
}{
	{"PENDING", "dim", []string{plan.Pending}},
	{"RUNNING", "task", []string{plan.Running, plan.Changes}},
	{"WAITING", "ask", []string{plan.Asking, plan.Ready, plan.Review}},
	{"DONE", "reply", []string{plan.Done, plan.Failed, plan.Conflict}},
}

// buckets sorts the plan's subtasks (by index) into the board's columns.
func (s *State) buckets() [][]int {
	out := make([][]int, len(boardCols))
	if s.Plan == nil {
		return out
	}
	for i, st := range s.Plan.Subtasks {
		for col, bc := range boardCols {
			if slices.Contains(bc.statuses, st.Status) {
				out[col] = append(out[col], i)
			}
		}
	}
	return out
}

// boardWide is whether a board w cells wide has room for side-by-side columns;
// narrower, the columns stack.
func boardWide(w int) bool { return w >= 80 }

// NowLine is what an agent is doing right now, in one line: its newest tool
// call, thought or message from the live transcript, or else the newest tool
// call Lagrange saw.
func (s *State) NowLine(handle string) string {
	a := s.Agents[handle]
	if a == nil {
		return ""
	}
	for i := len(a.Feed) - 1; i >= 0; i-- {
		b := a.Feed[i]
		switch b.Kind {
		case "tool":
			if t := firstLine(b.Title); t != "" {
				return "▸ " + t
			}
		case "think":
			if t := lastLine(b.Text); t != "" {
				return "✻ " + t
			}
		case "say":
			if t := lastLine(b.Text); t != "" {
				return "⏺ " + t
			}
		}
	}
	for i := len(a.Log) - 1; i >= 0; i-- {
		if t := firstLine(a.Log[i]); t != "" {
			if !strings.HasPrefix(t, "▸ ") {
				t = "⏺ " + t
			}
			return t
		}
	}
	return ""
}

// subNow is the live line of a subtask's card: what its worker (or reviewer)
// is doing, what it waits for, or how it ended.
func (s *State) subNow(st *plan.Subtask) (string, string) {
	switch st.Status {
	case plan.Pending:
		var wait []string
		for _, d := range st.Deps {
			if dt := s.Plan.Get(d); dt != nil && dt.Status != plan.Done {
				wait = append(wait, d)
			}
		}
		if len(wait) > 0 {
			return "⧗ after " + strings.Join(wait, ", "), "dim"
		}
		return "ready to start", "dim"
	case plan.Running, plan.Changes:
		if l := s.NowLine(st.Worker); l != "" {
			return l, "fg"
		}
		return "starting…", "dim"
	case plan.Asking:
		for _, t := range s.Tasks {
			if t.Sender == st.Worker && t.Kind == "ask" && t.Ended.IsZero() {
				return "? " + firstLine(t.Summary), "ask"
			}
		}
		return "? asking the lead", "ask"
	case plan.Review:
		if l := s.NowLine(s.Reviewer); l != "" {
			return "◆ review " + l, "review"
		}
		return "◆ in review", "review"
	case plan.Ready:
		return "◇ ready for review", "reply"
	case plan.Done:
		add, del, n := 0, 0, 0
		for _, cm := range s.Commits {
			if cm.Sub == st.ID && len(cm.Parents) < 2 {
				add, del, n = add+cm.Add, del+cm.Del, n+1
			}
		}
		if n == 0 {
			return "✓ merged", "reply"
		}
		return fmt.Sprintf("✓ +%d −%d · %d commit%s", add, del, n, pick(n == 1, "", "s")), "reply"
	}
	return "✗ " + nonEmpty(firstLine(st.Note), st.Status), "fail"
}

// elapsed is how long a subtask has run (or ran).
func elapsed(st *plan.Subtask, now time.Time) string {
	if st.StartedAt == 0 {
		return ""
	}
	end := now
	if st.EndedAt > 0 {
		end = time.UnixMilli(st.EndedAt)
	}
	return mmss(end.Sub(time.UnixMilli(st.StartedAt)))
}

// card is one subtask on the board, w cells wide.
func (s *State) card(st *plan.Subtask, w int, sel bool, sc Screen) []string {
	bk := statusKey[st.Status]
	if sel {
		bk = "hub"
	}
	icon := statusIcon[st.Status]
	if st.Status == plan.Running {
		icon = string(spin[sc.Frame%len(spin)])
	}
	top := c(bk, "╭─") + c(statusKey[st.Status]+"!", icon) + " " + c("mute", st.ID) + " " + c(pick(sel, "hi!", "fg"), st.Title) + " "
	if ansi.StringWidth(top) > w-2 {
		top = ansi.Truncate(top, w-3, "…") + " "
	}
	top += c(bk, strings.Repeat("─", max(0, w-1-ansi.StringWidth(top)))+"╮")
	row := func(s string) string { return c(bk, "│") + " " + fit(s, w-4) + " " + c(bk, "│") }

	meta := c(modelKey(st.Model)+"!", st.Model)
	if st.Worker != "" {
		meta += c("dim", " "+s.Short(st.Worker))
	}
	if st.Round > 0 {
		meta += c("review", fmt.Sprintf(" r%d", st.Round+1))
	}
	if st.Escalated {
		meta += c("ask", " ↑")
	}
	if el := elapsed(st, sc.Now); el != "" {
		gap := w - 4 - ansi.StringWidth(meta) - len(el)
		if gap > 0 {
			meta += strings.Repeat(" ", gap) + c("dim", el)
		}
	}
	now, nk := s.subNow(st)
	lines := []string{top, row(meta), row(c(nk, now))}
	if st.Status == plan.Running || st.Status == plan.Changes || st.Status == plan.Review {
		lines = append(lines, row(c(modelKey(st.Model), bar(s.SubProgress(st, sc.Now), w-4))))
	}
	return append(lines, c(bk, "╰"+strings.Repeat("─", w-2)+"╯"))
}

// planBoard draws the kanban: columns side by side when there's room, stacked
// sections when not.
func (s *State) planBoard(w, h int, focused bool, sc Screen) []string {
	bs := s.buckets()
	sel := sc.UI.Cur[pPlan].sel
	if !boardWide(w) {
		return s.stackedBoard(bs, w, h, focused, sc)
	}
	const gap = 1
	cw := (w - gap*(len(boardCols)-1)) / len(boardCols)
	cols := make([][]string, len(boardCols))
	for ci, bc := range boardCols {
		head := c(bc.key+"!", bc.name) + c("dim", fmt.Sprintf(" %d", len(bs[ci])))
		col := []string{fit(head, cw), c("line2", strings.Repeat("─", cw))}
		var cards [][]string
		at := -1
		for k, i := range bs[ci] {
			if i == sel {
				at = k
			}
			cards = append(cards, s.card(s.Plan.Subtasks[i], cw, focused && i == sel, sc))
		}
		// Scroll the column so the selected card shows.
		room, from := h-len(col), 0
		for at >= 0 && from < at && cardsHeight(cards[from:at+1]) > room {
			from++
		}
		for k := from; k < len(cards); k++ {
			if len(col)+len(cards[k]) > h-1 && k < len(cards)-1 || len(col)+len(cards[k]) > h {
				col = append(col, c("dim", fit(fmt.Sprintf("  ↓ %d more", len(cards)-k), cw)))
				break
			}
			col = append(col, cards[k]...)
		}
		if from > 0 && len(col) > 2 {
			col[1] = c("dim", fit(fmt.Sprintf("↑ %d more ", from), cw))
		}
		if len(bs[ci]) == 0 {
			col = append(col, c("dim", fit("  —", cw)))
		}
		cols[ci] = col
	}
	out := make([]string, h)
	for r := range out {
		var b strings.Builder
		for ci := range cols {
			if ci > 0 {
				b.WriteString(strings.Repeat(" ", gap))
			}
			cell := ""
			if r < len(cols[ci]) {
				cell = cols[ci][r]
			}
			b.WriteString(fit(cell, cw))
		}
		out[r] = b.String()
	}
	return out
}

func cardsHeight(cards [][]string) int {
	n := 0
	for _, cd := range cards {
		n += len(cd)
	}
	return n
}

// stackedBoard is the board in a narrow panel: a section per column, each
// subtask a line with its live line under it while it runs.
func (s *State) stackedBoard(bs [][]int, w, h int, focused bool, sc Screen) []string {
	cur := sc.UI.Cur[pPlan]
	var items [][]string
	at := 0
	for ci, bc := range boardCols {
		items = append(items, []string{c(bc.key+"!", bc.name) + c("dim", fmt.Sprintf(" %d ", len(bs[ci]))) + c("line2", strings.Repeat("─", max(0, w-4-len(bc.name)-4)))})
		for _, i := range bs[ci] {
			st := s.Plan.Subtasks[i]
			if i == cur.sel {
				at = len(items)
			}
			item := []string{s.subLine(st, w-2, sc)}
			if now, nk := s.subNow(st); st.Status != plan.Pending || len(st.Deps) > 0 {
				item = append(item, "    "+c(nk, fit(now, w-6)))
			}
			items = append(items, item)
		}
	}
	tmp := &pcursor{sel: at, off: sc.UI.BoardOff}
	out := listLines(items, tmp, h, focused, false)
	sc.UI.BoardOff = tmp.off
	return out
}

// boardMove moves the plan's selection on the board: j/k within a column (or
// down the stacked sections), h/l to the next column with subtasks.
func (s *State) boardMove(u *UI, key string, wide bool) bool {
	if s.Plan == nil || len(s.Plan.Subtasks) == 0 {
		return false
	}
	bs := s.buckets()
	cur := u.Cur[pPlan]
	col, pos := 0, 0
	for ci, b := range bs {
		if k := slices.Index(b, cur.sel); k >= 0 {
			col, pos = ci, k
		}
	}
	var flat []int
	for _, b := range bs {
		flat = append(flat, b...)
	}
	fi := slices.Index(flat, cur.sel)
	switch key {
	case "j", "down", "k", "up":
		d := 1
		if key == "k" || key == "up" {
			d = -1
		}
		if wide {
			if b := bs[col]; len(b) > 0 {
				cur.sel = b[max(0, min(len(b)-1, pos+d))]
			}
		} else {
			cur.sel = flat[max(0, min(len(flat)-1, fi+d))]
		}
	case "h", "left", "l", "right":
		d := 1
		if key == "h" || key == "left" {
			d = -1
		}
		for ci := col + d; ci >= 0 && ci < len(bs); ci += d {
			if len(bs[ci]) > 0 {
				cur.sel = bs[ci][min(pos, len(bs[ci])-1)]
				break
			}
		}
	case "g", "home":
		cur.sel = flat[0]
	case "G", "end":
		cur.sel = flat[len(flat)-1]
	default:
		return false
	}
	cur.follow = false
	return true
}

// ── timeline ─────────────────────────────────────────────────────────────────

// tlCell is one cell of a swimlane.
type tlCell struct {
	r   rune
	key string
}

// planTimeline draws one swimlane per agent over the run's time: what each
// worked on and when, where it waited, and what everyone is doing now.
func (s *State) planTimeline(w, h int, sc Screen) []string {
	const labelW = 13
	cols := w - labelW - 1
	if cols < 10 || s.RootAt.IsZero() {
		return []string{c("dim", "the timeline starts with the run")}
	}
	start, end := s.RootAt, sc.Now
	if !s.RootEnd.IsZero() {
		end = s.RootEnd
	}
	span := max(end.Sub(start), 20*time.Second)
	x := func(t time.Time) int { return max(0, min(cols-1, int(int64(t.Sub(start))*int64(cols)/int64(span)))) }

	var sel string
	if cur := sc.UI.Cur[pPlan]; s.Plan != nil && cur.sel < len(s.Plan.Subtasks) {
		sel = s.Plan.Subtasks[cur.sel].ID
	}
	lanes := map[string][]tlCell{}
	var order []string
	lane := func(hd string) []tlCell {
		if _, ok := lanes[hd]; !ok {
			lanes[hd] = make([]tlCell, cols)
			for i := range lanes[hd] {
				lanes[hd][i] = tlCell{'·', "line2"}
			}
			order = append(order, hd)
		}
		return lanes[hd]
	}
	type seg struct {
		hd, label string
		x0, x1    int
		r         rune
		key       string
	}
	var segs []seg
	ts := make([]*Task, 0, len(s.Tasks))
	for _, t := range s.Tasks {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Started.Before(ts[j].Started) })
	for _, t := range ts {
		te := t.Ended
		if te.IsZero() {
			te = end
		}
		x0, x1 := x(t.Started), x(te)
		mk := modelKey(s.model(t.Recipient))
		switch {
		case t.ID == s.Root:
			segs = append(segs, seg{t.Recipient, "", x0, x1, '─', mk})
		case t.Recipient == s.Human:
			segs = append(segs, seg{t.Recipient, "", x0, x1, '?', "ask"})
		case t.Kind == "review":
			segs = append(segs, seg{t.Recipient, t.Sub, x0, x1, '▒', "review"})
		case t.Kind == "ask":
			// A worker asking: it waits, the lead answers.
			segs = append(segs, seg{t.Sender, "", x0, x1, '?', "ask"}, seg{t.Recipient, "", x0, x1, '▪', mk})
		default:
			segs = append(segs, seg{t.Recipient, t.Sub, x0, x1, '█', mk})
		}
		if t.State == stFailed || t.State == stCanceled || t.State == stRejected {
			segs[len(segs)-1].key = "fail"
		}
	}
	// Lanes in roster order: the lead, workers, reviewer, then you if you were asked.
	for _, hd := range s.Order {
		if hd == s.Human {
			continue
		}
		for _, sg := range segs {
			if sg.hd == hd {
				lane(hd)
				break
			}
		}
	}
	for _, sg := range segs {
		if sg.hd == s.Human {
			lane(s.Human)
			break
		}
	}
	for _, sg := range segs {
		l, ok := lanes[sg.hd]
		if !ok {
			continue
		}
		for i := sg.x0; i <= sg.x1; i++ {
			l[i] = tlCell{sg.r, sg.key}
		}
	}
	// Ticks: the time every ~12 cells, and where now is.
	axis := []rune(strings.Repeat(" ", cols))
	step := max(12, cols/8)
	for i := 0; i+6 < cols; i += step {
		copy(axis[i:], []rune("┊"+mmss(time.Duration(int64(span)*int64(i)/int64(cols)))))
	}
	out := []string{c("dim", pad("", labelW)) + " " + c("dim", string(axis))}
	for _, hd := range order {
		name := pick(hd == s.Human, "you", s.Short(hd))
		var b strings.Builder
		for _, cl := range lanes[hd] {
			b.WriteString(c(cl.key, string(cl.r)))
		}
		row := b.String()
		// Label each subtask's first segment on the lane, the selected one lit.
		for _, sg := range segs {
			if sg.hd != hd || sg.label == "" || sg.x1-sg.x0+1 < len(sg.label)+1 {
				continue
			}
			lbl := badge(pick(sg.label == sel, "hub", sg.key), sg.label)
			row = ansi.Truncate(row, sg.x0, "") + lbl + ansi.TruncateLeft(row, sg.x0+len(sg.label), "")
		}
		out = append(out, c(modelKey(s.model(hd))+"!", pad(name, labelW))+" "+row)
	}
	out = append(out, "", c("dim", pad("", labelW)+" ")+c("task", "█")+c("dim", " working  ")+c("review", "▒")+c("dim", " review  ")+
		c("ask", "?")+c("dim", " asking / waiting on you  ")+c("opus", "▪")+c("dim", " lead answering  ")+c("opus", "─")+c("dim", " the run  ")+badge("hub", "S2")+c("dim", " selected (j/k)"))
	// What everyone is doing now.
	out = append(out, "", c("hi!", "NOW"))
	n := 0
	for _, hd := range s.Order {
		a := s.Agents[hd]
		if a == nil || hd == s.Human || (a.State != "working" && a.State != "asking") {
			continue
		}
		line := c(modelKey(a.Model)+"!", pad(s.Short(hd), labelW)) + " "
		if a.Sub != "" {
			line += c("mute", a.Sub+" ")
		}
		line += c(pick(a.State == "asking", "ask", "fg"), s.NowLine(hd))
		out = append(out, line)
		n++
	}
	if n == 0 {
		out = append(out, c("dim", "nobody is working"))
	}
	if len(out) > h {
		out = out[:h]
	}
	return out
}
