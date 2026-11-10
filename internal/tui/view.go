package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/three-agent-problem/halo/internal/plan"
)

const (
	leftW   = 52
	rightW  = 46
	commsH  = 12
	minWide = 150
)

// Screen is everything the view needs besides the state.
type Screen struct {
	W, H      int
	Frame     int
	Now       time.Time
	Server    string
	Connected bool
	Online    int
	Waiting   int
	Footer    string   // the prompt line(s), already rendered
	Popup     []string // the completion list, drawn above the input
	Bottom    string   // replaces the bottom of the screen when set (answering)
	BottomTag string
	Flash     string
	UI        *UI
	Team      string    // "team here" or "watch-only"
	Procs     bool      // this Halo can see the agents' shells
	Overlay   []string  // a box drawn in the middle (the /resume list)
	Runs      []RunInfo // your runs with the lead, for the full-screen comms
	RunSel    int       // the run selected in that list
}

// runsW is the width of the runs list beside the full-screen comms, and
// sideMin the narrowest the comms may be at side size.
const (
	runsW   = 46
	sideMin = 70
)

// Render draws the whole screen.
func (s *State) Render(sc Screen) string {
	if sc.W < 80 || sc.H < 24 {
		return fit("halo needs at least 80×24; this terminal is "+fmt.Sprintf("%d×%d", sc.W, sc.H), sc.W)
	}
	if sc.UI == nil {
		sc.UI = NewUI()
	}
	u := sc.UI
	foot := strings.Split(sc.Footer, "\n")
	bodyH := sc.H - 1 - len(foot)
	var body []string
	if u.grown() == sizeFull {
		pane := s.paneBox(u.Focus, sc.W, bodyH-1, sc)
		if u.Focus == pComms && sc.W >= runsW+sideMin {
			// Past conversations beside the comms, to open without /resume.
			pane = lipgloss.JoinHorizontal(lipgloss.Top, s.paneBox(pComms, sc.W-runsW, bodyH-1, sc), s.runsBox(runsW, bodyH-1, sc))
		}
		body = append([]string{s.tabs(sc.W, sc)}, strings.Split(pane, "\n")...)
	} else {
		body = s.normal(sc.W, bodyH, sc)
	}
	if sc.Bottom != "" {
		ab := strings.Split(box(sc.W, commsH, c("ask!", "ANSWER")+c("dim", " · "+sc.BottomTag), "", strings.Split(sc.Bottom, "\n"), true), "\n")
		body = append(body[:len(body)-commsH], ab...)
	}
	if len(sc.Popup) > 0 {
		body = overlay(body, sc.Popup, 1, bodyH-len(sc.Popup), sc.W)
	}
	if len(sc.Overlay) > 0 {
		w := ansi.StringWidth(sc.Overlay[0])
		body = overlay(body, sc.Overlay, (sc.W-w)/2, max(0, (bodyH-len(sc.Overlay))/2), sc.W)
	}
	if u.Help {
		w, h := min(sc.W-4, 96), min(bodyH-2, 40)
		body = overlay(body, helpBox(u.Query, w, h, sc.Frame), (sc.W-w)/2, (bodyH-h)/2, sc.W)
	}
	for i := range foot {
		foot[i] = fit(foot[i], sc.W)
	}
	return strings.Join(append(append([]string{s.header(sc)}, body...), foot...), "\n")
}

// normal is the layout below full size: plan and commits on the left, the orbit
// in the middle, the focused agent, shells and roster on the right, comms at the
// bottom. A grown panel takes its column (its mates collapse to bars), then the
// orbit's width; a minimized panel is a one-line bar and its column mates share
// the room it leaves.
func (s *State) normal(w, h int, sc Screen) []string {
	u := sc.UI
	g := u.grown()
	ch := commsH
	if u.Focus == pComms && g >= sizeColumn {
		ch = max(commsH, h/2)
		if g == sizeWide && w >= minWide {
			// The left side, full height; every other panel compact beside it, so
			// the run stays in view while you talk.
			cw := max(sideMin, w*11/20)
			rest := s.column([]int{pPlan, pAgent, pCommits, pRoster, pShells}, pAgent, w-cw, h, sc)
			return strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, s.paneBox(pComms, cw, h, sc), rest), "\n")
		}
	}
	midH := h - ch
	var mid []string
	switch {
	case w < minWide:
		// Narrow: the focused panel (or the orbit) above the comms; there is no
		// column to take, so column and wide look like normal.
		id := u.Focus
		if id == pComms {
			id = pOrbit
		}
		if u.Size[id] == sizeMin {
			mid = append([]string{s.miniBar(id, w, sc)}, strings.Split(s.paneBox(pOrbit, w, midH-1, sc), "\n")...)
		} else {
			mid = strings.Split(s.paneBox(id, w, midH, sc), "\n")
		}
	case u.Focus == pOrbit && g == sizeWide:
		mid = strings.Split(s.paneBox(pOrbit, w, midH, sc), "\n")
	default:
		lower := pCommits
		if u.Focus == pEvents {
			lower = pEvents
		}
		leftIDs, rightIDs := []int{pPlan, lower}, []int{pAgent, pShells, pRoster}
		lw, rw, orbit := leftW, rightW, true
		if g == sizeWide && u.Focus != pOrbit && u.Focus != pComms {
			orbit = false
			if slices.Contains(leftIDs, u.Focus) {
				lw = w - rightW
			} else {
				rw = w - leftW
			}
		}
		parts := []string{s.column(leftIDs, lower, lw, midH, sc)}
		if orbit {
			parts = append(parts, s.paneBox(pOrbit, w-lw-rw, midH, sc))
		}
		parts = append(parts, s.column(rightIDs, pAgent, rw, midH, sc))
		mid = strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, parts...), "\n")
	}
	return append(mid, strings.Split(s.paneBox(pComms, w, ch, sc), "\n")...)
}

// column stacks panels ids in a w×h column. Minimized panels, and the mates of a
// grown one, are one-line bars; flex takes the height the others leave (the last
// open panel does when flex is closed).
func (s *State) column(ids []int, flex, w, h int, sc Screen) string {
	u := sc.UI
	grown := 0
	if u.grown() >= sizeColumn && slices.Contains(ids, u.Focus) {
		grown = u.Focus
	}
	var open []int
	for _, id := range ids {
		if u.Size[id] != sizeMin && (grown == 0 || id == grown) {
			open = append(open, id)
		}
	}
	avail := h - (len(ids) - len(open))
	heights := map[int]int{}
	if len(open) > 0 {
		if !slices.Contains(open, flex) {
			flex = open[len(open)-1]
		}
		rest := avail
		for _, id := range open {
			if id != flex {
				heights[id] = s.natural(id, avail)
				rest -= heights[id]
			}
		}
		heights[flex] = rest
	}
	var out []string
	for _, id := range ids {
		if hh, ok := heights[id]; ok {
			out = append(out, strings.Split(s.paneBox(id, w, hh, sc), "\n")...)
		} else {
			out = append(out, s.miniBar(id, w, sc))
		}
	}
	if len(open) == 0 {
		out = append(out, c("dim", fit("  every panel here is minimized · focus one and press + to open it", w)))
		for len(out) < h {
			out = append(out, strings.Repeat(" ", w))
		}
	}
	return strings.Join(out, "\n")
}

// natural is the height panel id wants next to a flexible mate, with avail
// rows in the column.
func (s *State) natural(id, avail int) int {
	switch id {
	case pPlan:
		return max(3, min(max(s.planHeight(), 6), avail-6))
	case pShells:
		return max(3, min(max(len(s.Shells)+2, 3), 8, avail/4))
	case pRoster:
		return max(3, min(len(s.Order)+2, avail/3))
	}
	return max(3, avail/3)
}

// miniBar is a minimized panel: its number, name and a one-line summary in a
// border-colored rule.
func (s *State) miniBar(id, w int, sc Screen) string {
	bc, num := "line2", c("dim", fmt.Sprintf("%d ", id))
	if sc.UI.Focus == id {
		bc, num = "haiku", c("haiku!", fmt.Sprintf("%d ", id))
	}
	left := c(bc, "╶─ ") + c(bc+"!", "▸ ") + num + c("hi!", strings.ToUpper(paneName[id]))
	if sum := s.summary(id, sc); sum != "" {
		left += c("dim", " · ") + c("mute", sum)
	}
	left += " "
	right := c("dim", " + ") + c(bc, "─╴")
	if ansi.StringWidth(left)+ansi.StringWidth(right)+1 > w {
		left = ansi.Truncate(left, max(0, w-ansi.StringWidth(right)-2), "…") + " "
	}
	fill := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if fill < 1 {
		return fit(left, w)
	}
	return left + c(bc, strings.Repeat("─", fill)) + right
}

// summary is the one line a minimized panel shows.
func (s *State) summary(id int, sc Screen) string {
	switch id {
	case pPlan:
		if s.Plan == nil {
			return pick(s.Root == "", "no run", "planning")
		}
		n := map[string]int{}
		for _, st := range s.Plan.Subtasks {
			n[st.Status]++
		}
		out := fmt.Sprintf("%d/%d done", n[plan.Done], len(s.Plan.Subtasks))
		if r := n[plan.Running] + n[plan.Changes]; r > 0 {
			out += fmt.Sprintf(" · %d running", r)
		}
		if a := n[plan.Asking]; a > 0 {
			out += fmt.Sprintf(" · %d asking", a)
		}
		if f := n[plan.Failed] + n[plan.Conflict]; f > 0 {
			out += fmt.Sprintf(" · %d failed", f)
		}
		if !s.Plan.Approved {
			out += " · awaiting approval"
		}
		return out
	case pCommits:
		if len(s.Commits) == 0 {
			return "none yet"
		}
		return fmt.Sprintf("%d · last %s", len(s.Commits), ago(sc.Now.Sub(s.Commits[len(s.Commits)-1].At)))
	case pEvents:
		return fmt.Sprintf("%d events", len(s.Raw))
	case pAgent:
		a := s.Agents[s.Focus]
		if a == nil {
			return "nobody yet"
		}
		out := s.Short(a.Handle) + " " + a.Model
		if a.Sub != "" {
			out += " · " + a.Sub
		}
		if a.State != "" {
			out += " · " + a.State
		}
		return out
	case pShells:
		ports := []string{}
		for _, sh := range s.Shells {
			for _, p := range sh.Ports {
				ports = append(ports, fmt.Sprintf(":%d", p))
			}
		}
		out := fmt.Sprintf("%d running", len(s.Shells))
		if len(ports) > 0 {
			out += " · " + strings.Join(ports, " ")
		}
		return out
	case pRoster:
		working := 0
		for _, hd := range s.Order {
			if a := s.Agents[hd]; a != nil && a.State == "working" {
				working++
			}
		}
		out := fmt.Sprintf("%d agents · %d working", len(s.Order), working)
		if t := s.TeamCost(); t > 0 {
			out += " · " + money(t)
		}
		return out
	}
	return ""
}

// planHeight is what the plan panel needs at normal size.
func (s *State) planHeight() int {
	if s.Plan == nil {
		if s.Hint != "" {
			return 4 + 1 + min(3, len(wrapLines(s.Hint, leftW-4, 0)))
		}
		return 6
	}
	n := len(s.Plan.Subtasks) + 2 + min(2, len(wrapLines(s.Plan.Goal, leftW-6, 0))) + 1
	for _, st := range s.Plan.Subtasks {
		if st.Status == plan.Running || st.Status == plan.Changes || st.Status == plan.Asking {
			n++ // the live line under it
		}
	}
	if !s.Plan.Approved {
		n += 2
	}
	return n
}

// tabs is the panel bar shown when a panel is grown.
func (s *State) tabs(w int, sc Screen) string {
	u := sc.UI
	var b strings.Builder
	b.WriteString(" ")
	for id := 1; id <= nPanes; id++ {
		label := fmt.Sprintf(" %d %s ", id, paneName[id])
		if id == u.Focus {
			b.WriteString(badge("haiku", label))
		} else {
			b.WriteString(c("mute", label))
		}
		b.WriteString(" ")
	}
	right := c("dim", u.Size[u.Focus].label(u.Focus)+" · ") + c("mute!", "+") + c("dim", " grow  ") + c("mute!", "_") + c("dim", " shrink  ") + c("mute!", "?") + c("dim", " keys ")
	left := b.String()
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return fit(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

// runsBox lists your runs with the lead beside the full-screen comms: [ and ]
// pick one, o opens it here (like /resume).
func (s *State) runsBox(w, h int, sc Screen) string {
	inner := w - 4
	lines := []string{}
	if len(sc.Runs) == 0 {
		lines = append(lines, c("dim", "no runs yet"))
	}
	sel := max(0, min(sc.RunSel, len(sc.Runs)-1))
	// Keep the selection in view: two lines per run.
	first := max(0, sel-(h-4)/2+1)
	for i := first; i < len(sc.Runs) && len(lines)+2 <= h-2; i++ {
		r := sc.Runs[i]
		state := strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(r.State, "TASK_STATE_")), "_", "-")
		mark, title := "  ", "fg"
		if i == sel {
			mark, title = c("hub!", "▌ "), "hi!"
		}
		if r.ID == s.Root {
			state = "on screen"
		}
		lines = append(lines,
			mark+c(title, fit(firstLine(r.Title), inner-2)),
			"  "+c("hub", RunShort(r.ID))+" "+c(nonEmpty(runStateKey[state], "dim"), state)+c("dim", fmt.Sprintf(" · %d tasks · %s", r.Tasks, ago(sc.Now.Sub(r.Updated)))))
	}
	return box(w, h, c("hi!", "RUNS"), c("dim", "[ ] pick · o open"), lines, false)
}

func (s *State) header(sc Screen) string {
	sep := c("line2", " · ")
	conn := c("fail!", "●")
	if sc.Connected {
		conn = c("haiku!", "●")
	}
	left := c("hub!", "◉ HALO") + "  "
	if s.Root == "" {
		left += c("dim", "no run")
	} else {
		state, key := s.runState()
		left += c("mute", "run ") + c("hi!", RunShort(s.Root)) + " " + c(key+"!", state)
		if s.Plan != nil {
			done := 0
			for _, st := range s.Plan.Subtasks {
				if st.Status == plan.Done {
					done++
				}
			}
			left += sep + c("hi!", fmt.Sprintf("%d/%d", done, len(s.Plan.Subtasks))) + c("dim", " done")
		}
		left += sep + c("dim", fmt.Sprintf("%d tasks  %d questions  %d reviews  %d commits", len(s.Tasks), s.Questions, s.Reviews, len(s.Commits)))
	}
	if sc.Waiting > 0 {
		left += sep + c("ask!", fmt.Sprintf("▲ %d waiting on you", sc.Waiting))
	}
	if sc.Flash != "" {
		left += sep + c("ask", sc.Flash)
	}
	_, buckets := s.Rate(sc.Now, 12)
	el := ""
	if !s.RootAt.IsZero() {
		end := sc.Now
		if !s.RootEnd.IsZero() {
			end = s.RootEnd
		}
		el = c("hi!", mmss(end.Sub(s.RootAt))) + sep
	}
	team := ""
	if sc.Team != "" {
		key := "mute"
		if sc.Team == "watch-only" {
			key = "ask"
		}
		team = c(key, sc.Team) + sep
	}
	right := c("sonnet", sparkline(buckets)) + sep + team + c("mute", fmt.Sprintf("%d online", sc.Online)) + sep +
		c("mute", strings.TrimPrefix(strings.TrimPrefix(sc.Server, "https://"), "http://")) + " " + conn + sep + el + c("dim", sc.Now.Format("15:04"))
	gap := sc.W - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return fit(left, sc.W)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (s *State) runState() (string, string) {
	switch {
	case s.RootState == stCompleted:
		return "completed", "reply"
	case s.RootState == stFailed || s.RootState == stRejected || s.RootState == stCanceled:
		return strings.ToLower(short(s.RootState)), "fail"
	case s.Agents[s.Human] != nil && s.Agents[s.Human].State == "working":
		return "waiting on you", "ask"
	case s.Plan == nil:
		return "planning", "task"
	case !s.Plan.Approved:
		return "awaiting approval", "ask"
	}
	return "running", "task"
}

func live(ok bool) string {
	if ok {
		return c("haiku!", "●") + c("dim", " live")
	}
	return c("fail!", "●") + c("dim", " offline")
}

var statusIcon = map[string]string{
	plan.Pending: "○", plan.Asking: "?", plan.Ready: "◇", plan.Review: "◆", plan.Changes: "↺",
	plan.Done: "✓", plan.Failed: "✗", plan.Conflict: "✗",
}
var statusKey = map[string]string{
	plan.Pending: "dim", plan.Running: "task", plan.Asking: "ask", plan.Ready: "reply", plan.Review: "review",
	plan.Changes: "ask", plan.Done: "reply", plan.Failed: "fail", plan.Conflict: "fail",
}

func cursor(frame int) string {
	if (frame/6)%2 == 0 {
		return styles["cursor"].Render(" ")
	}
	return " "
}
