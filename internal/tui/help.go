package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type binding struct{ scope, keys, desc string }

// bindings is every key, for the ? overlay and the footer.
var bindings = []binding{
	{"panels", "1 … 7", "focus a panel: 1 plan · 2 orbit · 3 agent · 4 comms · 5 commits · 6 events · 7 roster"},
	{"panels", "tab / shift+tab", "next / previous panel"},
	{"panels", "+ / _", "grow / shrink the focused panel: normal → half → full"},
	{"panels", "j k  ↓ ↑", "move the selection; scroll an open item"},
	{"panels", "g / G", "first item / newest item (and follow it)"},
	{"panels", "ctrl+d / ctrl+u", "half a page down / up"},
	{"panels", "enter", "open the selected item"},
	{"panels", "esc", "back: open item → panel → normal size"},
	{"run", "i", "write a prompt to the lead"},
	{"run", "/", "a command: /clear starts fresh · /resume reopens a past run · /merge lands the run · /agents · /help"},
	{"run", "a", "answer what waits on you: y approve · n reject · r reply"},
	{"run", "x x", "cancel the run"},
	{"run", "q / ctrl+c", "quit"},
	{"run", "?", "this help; type to search, esc to close"},
	{"1 plan", "enter", "subtask detail: status, worker, commits, every message about it"},
	{"3 agent", "h / l", "previous / next agent"},
	{"3 agent", "enter", "the agent's latest task and its whole answer"},
	{"4 comms", "f", "only messages from or to one agent (press again for the next one)"},
	{"4 comms", "F", "clear the filter"},
	{"4 comms", "enter", "read the whole message"},
	{"5 commits", "enter", "the commit's diff (git show)"},
	{"5 commits", "[ / ]", "previous / next commit while a diff is open"},
	{"6 events", "enter", "the raw activity entry as JSON"},
	{"7 roster", "enter", "watch that agent in panel 3"},
	{"7 roster", "n", "add a worker; its model comes from its name (opus-2 → opus)"},
	{"7 roster", "m", "change the selected agent's model: haiku → sonnet → opus"},
	{"7 roster", "d d", "remove the selected worker"},
	{"7 roster", "R", "restart the team"},
	{"colors", "■ task", "work sent to an agent"},
	{"colors", "■ ask", "a question: agent → lead, or lead → you"},
	{"colors", "■ reply", "an answer, a finished subtask"},
	{"colors", "■ review", "a review request or verdict"},
	{"colors", "■ fail", "a failure or CHANGES from the reviewer"},
	{"colors", "H  S  O", "haiku · sonnet · opus"},
}

var scopeKey = map[string]string{"panels": "hub", "run": "hub", "colors": "dim"}

// helpLines lists the bindings that match q.
func helpLines(q string, w int) []string {
	var out []string
	last := ""
	for _, b := range bindings {
		if !matches(b.scope+" "+b.keys+" "+b.desc, q) {
			continue
		}
		if b.scope != last {
			if last != "" {
				out = append(out, "")
			}
			out = append(out, c(nonEmpty(scopeKey[b.scope], "task")+"!", strings.ToUpper(b.scope)))
			last = b.scope
		}
		keys := c("hi!", pad(b.keys, 17))
		if b.scope == "colors" {
			k := map[string]string{"■ task": "task", "■ ask": "ask", "■ reply": "reply", "■ review": "review", "■ fail": "fail"}[b.keys]
			keys = c(nonEmpty(k, "hi")+"!", pad(b.keys, 17))
		}
		desc := wrapLines(b.desc, w-21, 0)
		out = append(out, "  "+keys+" "+c("fg", desc[0]))
		for _, l := range desc[1:] {
			out = append(out, strings.Repeat(" ", 21)+c("fg", l))
		}
	}
	if len(out) == 0 {
		out = []string{c("dim", "no key matches “"+q+"”")}
	}
	return out
}

// helpBox is the ? overlay.
func helpBox(q string, w, h, frame int) []string {
	search := c("hub!", "/ ") + c("hi", q) + cursor(frame)
	if q == "" {
		search = c("hub!", "/ ") + c("dim", "type to search") + cursor(frame)
	}
	lines := append([]string{search, ""}, helpLines(q, w-4)...)
	if len(lines) > h-2 {
		lines = append(lines[:h-3], c("dim", "… type to narrow it down"))
	}
	return strings.Split(box(w, h, c("hi!", "KEYS"), c("dim", "esc close"), lines, true), "\n")
}

// overlay draws top over base at column x, row y.
func overlay(base, top []string, x, y, w int) []string {
	out := append([]string(nil), base...)
	for i, l := range top {
		r := y + i
		if r < 0 || r >= len(out) {
			continue
		}
		row := out[r]
		left := ansi.Truncate(row, x, "")
		if n := x - ansi.StringWidth(left); n > 0 {
			left += strings.Repeat(" ", n)
		}
		right := ansi.TruncateLeft(row, x+ansi.StringWidth(l), "")
		out[r] = left + l + right
		if ansi.StringWidth(out[r]) != w {
			out[r] = fit(out[r], w)
		}
	}
	return out
}
