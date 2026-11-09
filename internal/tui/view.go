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
	Team      string   // "team here" or "watch-only"
	Procs     bool     // this Halo can see the agents' shells
	Overlay   []string // a box drawn in the middle (the /resume list)
}

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
	switch size := u.Size[u.Focus]; {
	case size == sizeNormal:
		body = s.normal(sc.W, bodyH, sc)
	default:
		body = []string{s.tabs(sc.W, sc)}
		h := bodyH - 1
		if size == sizeFull {
			body = append(body, strings.Split(s.paneBox(u.Focus, sc.W, h, sc), "\n")...)
			break
		}
		// The companion is a panel that reads well narrow: the live agent, or the
		// comms when the agent is the one grown.
		mainW := sc.W * 64 / 100
		other := pAgent
		if u.Focus == pAgent {
			other = pComms
		}
		body = append(body, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top,
			s.paneBox(u.Focus, mainW, h, sc), s.paneBox(other, sc.W-mainW, h, sc)), "\n")...)
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

// normal is the default layout: plan and commits on the left, the orbit in the
// middle, the focused agent and the roster on the right, comms at the bottom.
func (s *State) normal(w, h int, sc Screen) []string {
	u := sc.UI
	midH := h - commsH
	var mid string
	if w >= minWide {
		planH := min(max(s.planHeight(), 6), midH-6)
		lower := pCommits
		if u.Focus == pEvents {
			lower = pEvents
		}
		left := s.paneBox(pPlan, leftW, planH, sc) + "\n" + s.paneBox(lower, leftW, midH-planH, sc)
		rosterH := min(len(s.Order)+2, midH/3)
		shellsH := min(max(len(s.Shells)+2, 3), 8, midH/4)
		right := s.paneBox(pAgent, rightW, midH-rosterH-shellsH, sc) + "\n" + s.paneBox(pShells, rightW, shellsH, sc) + "\n" + s.paneBox(pRoster, rightW, rosterH, sc)
		mid = lipgloss.JoinHorizontal(lipgloss.Top, left, s.paneBox(pOrbit, w-leftW-rightW, midH, sc), right)
	} else {
		// Narrow: the focused panel (or the orbit) above the comms.
		id := u.Focus
		if id == pComms {
			id = pOrbit
		}
		mid = s.paneBox(id, w, midH, sc)
	}
	return append(strings.Split(mid, "\n"), strings.Split(s.paneBox(pComms, w, commsH, sc), "\n")...)
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
	right := c("dim", u.Size[u.Focus].String()+" · ") + c("mute!", "+") + c("dim", " grow  ") + c("mute!", "_") + c("dim", " shrink  ") + c("mute!", "?") + c("dim", " keys ")
	left := b.String()
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return fit(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
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
