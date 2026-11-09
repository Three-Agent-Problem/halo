package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/three-agent-problem/halo/internal/plan"
)

const (
	leftW   = 50
	rightW  = 46
	commsH  = 11
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
	Footer    string // the prompt line, already rendered
	Bottom    string // replaces the comms pane when set (answering)
	BottomTag string
	Flash     string
}

// Render draws the whole screen.
func (s *State) Render(sc Screen) string {
	if sc.W < 80 || sc.H < 24 {
		return "halo needs at least 80×24; this terminal is " + fmt.Sprintf("%d×%d", sc.W, sc.H)
	}
	header := s.header(sc)
	midH := sc.H - 1 - commsH - 1
	var mid string
	if sc.W >= minWide {
		centerW := sc.W - leftW - rightW
		mid = lipgloss.JoinHorizontal(lipgloss.Top,
			s.left(leftW, midH, sc),
			box(centerW, midH, c("hi!", "ORBIT")+c("dim", " · every message routes through lagrange"),
				c("task", "■ task ")+c("ask", "■ ask ")+c("reply", "■ reply ")+c("review", "■ review ")+c("fail", "■ fail"),
				strings.Split(s.orbit(centerW-4, midH-2, sc.Now, sc.Frame), "\n"), false),
			s.right(rightW, midH, sc))
	} else {
		mid = box(sc.W, midH, c("hi!", "ORBIT"), "", strings.Split(s.orbit(sc.W-4, midH-2, sc.Now, sc.Frame), "\n"), false)
	}
	bottom := s.comms(sc.W, commsH, sc)
	if sc.Bottom != "" {
		bottom = box(sc.W, commsH, c("ask!", "ANSWER")+c("dim", " · "+sc.BottomTag), "", strings.Split(sc.Bottom, "\n"), true)
	}
	return strings.Join([]string{header, mid, bottom, fit(sc.Footer, sc.W)}, "\n")
}

func (s *State) header(sc Screen) string {
	sep := c("line2", " │ ")
	run := "—"
	if s.Root != "" {
		run = s.Root[len(s.Root)-4:]
	}
	conn := c("fail!", "●")
	if sc.Connected {
		conn = c("haiku!", "●")
	}
	rate, buckets := s.Rate(sc.Now, 24)
	_ = rate
	wait := c("dim", "▲ ") + c("dim!", "0") + c("dim", " waiting on you")
	if sc.Waiting > 0 {
		wait = c("ask!", fmt.Sprintf("▲ %d waiting on you", sc.Waiting))
	}
	el := "0:00"
	if !s.RootAt.IsZero() {
		end := sc.Now
		if !s.RootEnd.IsZero() {
			end = s.RootEnd
		}
		el = mmss(end.Sub(s.RootAt))
	}
	left := c("hub!", "◉ HALO") + "  " + c("mute", "run ") + c("hi!", run) + sep +
		c("mute", "lagrange ") + c("hi!", strings.TrimPrefix(strings.TrimPrefix(sc.Server, "https://"), "http://")) + " " + conn + sep +
		c("hi!", fmt.Sprint(sc.Online)) + c("mute", " agents online") + sep +
		c("mute", "events ") + c("hi!", fmt.Sprint(s.Events)) + " " + c("sonnet", sparkline(buckets)) + sep + wait
	if sc.Flash != "" {
		left += sep + c("ask", sc.Flash)
	}
	right := c("dim", "elapsed ") + c("hi!", el) + sep + c("hi!", sc.Now.Format("15:04:05"))
	gap := sc.W - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return fit(left, sc.W)
	}
	return left + strings.Repeat(" ", gap) + right
}

// ── left column ──────────────────────────────────────────────────────────────

func (s *State) left(w, h int, sc Screen) string {
	planLines := s.planLines(w-4, sc)
	planH := min(len(planLines)+2, h/2)
	runLines := s.runLines()
	runH := len(runLines) + 2
	evH := h - planH - runH
	done, total := 0, 0
	if s.Plan != nil {
		total = len(s.Plan.Subtasks)
		for _, st := range s.Plan.Subtasks {
			if st.Status == plan.Done {
				done++
			}
		}
	}
	parts := []string{
		box(w, planH, c("hi!", "PLAN")+c("dim", " · DAG"), c("dim", fmt.Sprintf("%d/%d", done, total)), planLines, false),
		box(w, runH, c("hi!", "RUN"), "", runLines, false),
	}
	if evH >= 4 {
		parts = append(parts, box(w, evH, c("hi!", "EVENTS")+c("dim", " · GET /api/events"), live(sc.Connected), s.rawLines(w-4, evH-2), false))
	}
	return strings.Join(parts, "\n")
}

func live(ok bool) string {
	if ok {
		return c("haiku!", "●") + c("dim", " live")
	}
	return c("fail!", "●") + c("dim", " offline")
}

var statusIcon = map[string]string{
	plan.Pending: "○", plan.Asking: "?", plan.Review: "◆", plan.Changes: "↺",
	plan.Done: "✓", plan.Failed: "✗", plan.Conflict: "✗",
}
var statusKey = map[string]string{
	plan.Pending: "dim", plan.Running: "task", plan.Asking: "ask", plan.Review: "review",
	plan.Changes: "ask", plan.Done: "reply", plan.Failed: "fail", plan.Conflict: "fail",
}

func (s *State) planLines(w int, sc Screen) []string {
	if s.Plan == nil {
		if s.Root == "" {
			return []string{c("dim", "no run yet. press enter and type a prompt.")}
		}
		return []string{c("mute", "planning… "+string(spin[sc.Frame%len(spin)])), "", c("dim", "› "+s.Prompt)}
	}
	var out []string
	goal := ansi.Wordwrap(s.Plan.Goal, w-2, " ")
	for i, l := range strings.Split(goal, "\n") {
		if i == 3 {
			break
		}
		out = append(out, c("line2", "│ ")+c("mute", l))
	}
	out = append(out, "")
	titleW := w - 26
	for _, st := range s.Plan.Subtasks {
		icon := statusIcon[st.Status]
		if st.Status == plan.Running {
			icon = string(spin[sc.Frame%len(spin)])
		}
		key := statusKey[st.Status]
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
		out = append(out, c(key+"!", icon)+" "+c("mute", pad(st.ID, 3))+" "+pad(st.Title, titleW)+" "+
			c(modelKey(st.Model)+"!", strings.ToUpper(st.Model[:1]))+" "+c(barKey, bar(s.SubProgress(st, sc.Now), 8))+" "+c("dim", pad(el, 5))+extra)
	}
	var deps []string
	for _, st := range s.Plan.Subtasks {
		if len(st.Deps) > 0 {
			deps = append(deps, st.ID+" ← "+strings.Join(st.Deps, ","))
		}
	}
	if len(deps) > 0 {
		out = append(out, "", c("dim", strings.Join(deps, "   ")))
	}
	if !s.Plan.Approved {
		out = append(out, c("ask", "waiting for your approval · press a"))
	}
	return out
}

func (s *State) runLines() []string {
	state := "idle"
	key := "dim"
	switch {
	case s.Root == "":
	case s.RootState == stCompleted:
		state, key = "completed", "reply"
	case s.RootState == stFailed || s.RootState == stRejected || s.RootState == stCanceled:
		state, key = strings.ToLower(short(s.RootState)), "fail"
	case s.Agents[s.Human] != nil && s.Agents[s.Human].State == "working":
		state, key = "waiting on you", "ask"
	case s.Plan == nil:
		state, key = "planning", "task"
	case !s.Plan.Approved:
		state, key = "awaiting approval", "ask"
	default:
		state, key = "running", "task"
	}
	esc := 0
	branch := "—"
	if s.Plan != nil {
		for _, st := range s.Plan.Subtasks {
			if st.Escalated {
				esc++
			}
		}
		branch = nonEmpty(s.Plan.Branch, "—")
	}
	row := func(k, v string) string { return c("dim", pad(k, 13)) + v }
	return []string{
		row("state", c(key+"!", state)),
		row("tasks", fmt.Sprint(len(s.Tasks))),
		row("questions", fmt.Sprint(s.Questions)),
		row("reviews", fmt.Sprintf("%d rounds", s.Reviews)),
		row("escalations", fmt.Sprint(esc)),
		row("branch", c("mute", branch)),
	}
}

func (s *State) rawLines(w, h int) []string {
	raw := s.Raw
	if len(raw) > h {
		raw = raw[len(raw)-h:]
	}
	kk := map[string]string{"task": "task", "message": "ask", "artifact": "dim", "input": "review"}
	out := make([]string, 0, h)
	for i := len(raw); i < h; i++ {
		out = append(out, "")
	}
	for _, r := range raw {
		sk := "reply"
		switch {
		case r.State == "INPUT_REQ":
			sk = "ask"
		case r.State == "FAILED" || r.State == "REJECTED" || r.State == "CANCELED":
			sk = "fail"
		case r.State == "":
			sk = "dim"
		}
		out = append(out, c("dim", fmt.Sprintf("%06d", r.Seq))+" "+c(nonEmpty(kk[r.Kind], "dim"), pad(r.Kind, 8))+" "+
			c("mute", pad(r.Who, 13))+" "+c(sk, pad(r.State, 9))+" "+c("dim", r.Text))
	}
	return out
}

// ── right column ─────────────────────────────────────────────────────────────

func (s *State) right(w, h int, sc Screen) string {
	rosterH := len(s.Order) + 2
	focusH := h - rosterH
	a := s.Agents[s.Focus]
	head := ""
	var lines []string
	if a != nil {
		head = c("dim", " · ") + c(modelKey(a.Model), s.Short(a.Handle))
		if a.Sub != "" && s.Plan != nil {
			if st := s.Plan.Get(a.Sub); st != nil {
				head += c("dim", " · "+st.ID+" "+st.Title)
			}
		}
		head += c("dim", " · "+a.Model)
		log := a.Log
		max := focusH - 3
		if len(log) > max {
			log = log[len(log)-max:]
		}
		for _, l := range log {
			if strings.HasPrefix(l, "▸ ") {
				lines = append(lines, c(modelKey(a.Model), "▸")+" "+c("fg", strings.TrimPrefix(l, "▸ ")))
			} else {
				lines = append(lines, "  "+c("hi", l))
			}
		}
		switch a.State {
		case "working", "asking", "waiting":
			what := "working…"
			if a.State == "asking" {
				what = "waiting for an answer…"
			}
			if a.State == "waiting" {
				what = "waiting for you…"
			}
			lines = append(lines, c(modelKey(a.Model)+"!", string(spin[sc.Frame%len(spin)]))+" "+c("dim", what)+" "+cursor(sc.Frame))
		}
		if len(lines) == 0 {
			lines = append(lines, c("dim", "nothing yet"))
		}
	}
	focus := box(w, focusH, c("hi!", "FOCUS")+head, c("dim", "tab ⇥"), lines, false)
	var roster []string
	for _, hd := range s.Order {
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
		roster = append(roster, c(k+"!", icon)+" "+c(modelKey(x.Model), pad(s.Short(hd), 12))+c("dim", pad(x.Model, 8))+c(k, pad(st, 9))+" "+c("dim", x.Sub))
	}
	return focus + "\n" + box(w, rosterH, c("hi!", "ROSTER"), "", roster, false)
}

func cursor(frame int) string {
	if (frame/6)%2 == 0 {
		return styles["cursor"].Render(" ")
	}
	return " "
}

// ── comms ───────────────────────────────────────────────────────────────────

var commTag = map[string]string{"task": "TASK", "ask": "ASK", "reply": "REPLY", "review": "REVIEW", "fail": "FAIL", "hub": "LAGRANGE"}

func (s *State) comms(w, h int, sc Screen) string {
	n := h - 2
	cs := s.Comms
	if len(cs) > n {
		cs = cs[len(cs)-n:]
	}
	var lines []string
	for i := len(cs); i < n; i++ {
		lines = append(lines, "")
	}
	for i, m := range cs {
		ts := c("dim", m.At.Format("15:04:05"))
		fresh := i == len(cs)-1 && sc.Now.Sub(m.At) < 1500*time.Millisecond
		text := m.Text
		if fresh {
			text = c("hi!", text)
		}
		if m.Kind == "hub" {
			lines = append(lines, ts+"  "+c("hub!", pad(commTag[m.Kind], 9))+c("hub", m.Text))
			continue
		}
		lines = append(lines, ts+"  "+c(kindKey(m.Kind)+"!", pad(commTag[m.Kind], 9))+
			c(modelKey(s.model(m.From)), pad(s.Short(m.From), 12))+c("dim", "→ ")+
			c(modelKey(s.model(m.To)), pad(s.Short(m.To), 12))+text)
	}
	return box(w, h, c("hi!", "COMMS")+c("dim", " · agent ⇄ agent, live"), c("dim", fmt.Sprintf("%d msgs", len(s.Comms))), lines, false)
}
