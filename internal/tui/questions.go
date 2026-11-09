package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/three-agent-problem/halo/internal/ask"
)

// qform is the lead's multiple-choice questions being answered: one tab per
// question and a last one to review and send, like Claude Code's plan questions.
type qform struct {
	w       Wait
	tab     int          // the question shown; len(questions) is the review tab
	row     []int        // the highlighted row per question; len(options) is "Other"
	answers []ask.Answer // what's picked so far
}

func newQForm(w Wait) *qform {
	return &qform{w: w, row: make([]int, len(w.Questions)), answers: make([]ask.Answer, len(w.Questions))}
}

func (f *qform) qs() []ask.Question { return f.w.Questions }

// review is whether the review tab is shown.
func (f *qform) review() bool { return f.tab >= len(f.qs()) }

// onOther is whether the highlighted row is the free-text one, where keys type.
func (f *qform) onOther() bool {
	return !f.review() && f.row[f.tab] == len(f.qs()[f.tab].Options)
}

// answered is whether question i has a pick or text.
func (f *qform) answered(i int) bool {
	return len(f.answers[i].Picked) > 0 || strings.TrimSpace(f.answers[i].Other) != ""
}

// pick picks option o of the current question: the one answer of a single
// choice (dropping any text), or a toggle of a multiple choice.
func (f *qform) pick(o int) {
	q, a := f.qs()[f.tab], &f.answers[f.tab]
	if !q.MultiSelect {
		a.Picked, a.Other = []int{o}, ""
		return
	}
	for i, p := range a.Picked {
		if p == o {
			a.Picked = append(a.Picked[:i], a.Picked[i+1:]...)
			return
		}
	}
	a.Picked = append(a.Picked, o)
}

func (f *qform) picked(i, o int) bool {
	for _, p := range f.answers[i].Picked {
		if p == o {
			return true
		}
	}
	return false
}

// key handles a key; it returns true when the form should be sent.
func (f *qform) key(k tea.KeyPressMsg) (send bool) {
	key := k.String()
	switch key {
	case "left", "shift+tab":
		f.tab = max(0, f.tab-1)
		return false
	case "right", "tab":
		f.tab = min(len(f.qs()), f.tab+1)
		return false
	}
	if f.review() {
		switch key {
		case "enter", "y":
			return true
		case "h":
			f.tab--
		}
		return false
	}
	q, a := f.qs()[f.tab], &f.answers[f.tab]
	n := len(q.Options) + 1
	if f.onOther() {
		switch key {
		case "up":
			f.row[f.tab]--
		case "enter":
			if strings.TrimSpace(a.Other) != "" {
				if !q.MultiSelect {
					a.Picked = nil
				}
				f.tab++
			}
		case "backspace":
			if r := []rune(a.Other); len(r) > 0 {
				a.Other = string(r[:len(r)-1])
			}
		case "ctrl+u":
			a.Other = ""
		default:
			if k.Text != "" {
				if !q.MultiSelect {
					a.Picked = nil
				}
				a.Other += k.Text
			}
		}
		return false
	}
	switch key {
	case "up", "k":
		f.row[f.tab] = max(0, f.row[f.tab]-1)
	case "down", "j":
		f.row[f.tab] = min(n-1, f.row[f.tab]+1)
	case "space", " ":
		f.pick(f.row[f.tab])
	case "enter":
		if q.MultiSelect {
			if len(a.Picked) == 0 {
				f.pick(f.row[f.tab])
			}
		} else {
			f.pick(f.row[f.tab])
		}
		f.tab++
	case "l":
		f.tab = min(len(f.qs()), f.tab+1)
	case "h":
		f.tab = max(0, f.tab-1)
	default:
		if len(key) == 1 && key[0] >= '1' && int(key[0]-'0') <= len(q.Options) {
			o := int(key[0] - '1')
			f.row[f.tab] = o
			f.pick(o)
			if !q.MultiSelect {
				f.tab++
			}
		}
	}
	return false
}

// paste types pasted text into the "Other" row.
func (f *qform) paste(s string) {
	if f.onOther() {
		a := &f.answers[f.tab]
		if !f.qs()[f.tab].MultiSelect {
			a.Picked = nil
		}
		a.Other += cleanPaste(s)
	}
}

// text is the answer sent to the lead.
func (f *qform) text() string { return ask.Format(f.qs(), f.answers) }

// box draws the form w cells wide.
func (f *qform) box(st *State, w, maxH, frame int) []string {
	iw := w - 4
	var lines []string
	// The tabs: one per question, ✓ once answered, then Submit.
	var tabs []string
	for i, q := range f.qs() {
		mark := "☐ "
		if f.answered(i) {
			mark = "✓ "
		}
		label := " " + mark + nonEmpty(q.Header, fmt.Sprintf("Q%d", i+1)) + " "
		if i == f.tab {
			tabs = append(tabs, badge("hub", label))
		} else {
			tabs = append(tabs, c(pick(f.answered(i), "reply", "mute"), label))
		}
	}
	sub := " Submit "
	if f.review() {
		tabs = append(tabs, badge("reply", sub))
	} else {
		tabs = append(tabs, c("dim", sub))
	}
	lines = append(lines, c("dim", "← ")+strings.Join(tabs, " ")+c("dim", " →"), "")
	if ctx := strings.TrimSpace(firstLine(f.w.Question)); ctx != "" && !strings.Contains(ctx, ": ") {
		lines = append(lines, c("mute", fit(ctx, iw)), "")
	}
	if f.review() {
		lines = append(lines, c("hi!", "Your answers"), "")
		for i, q := range f.qs() {
			head := c("mute!", pad(nonEmpty(q.Header, fmt.Sprintf("Q%d", i+1)), 10))
			ans := strings.TrimPrefix(ask.Format(f.qs()[i:i+1], f.answers[i:i+1]), nonEmpty(q.Header, q.Question)+": ")
			key := "fg"
			if !f.answered(i) {
				key = "ask"
			}
			for j, l := range wrapLines(ans, iw-11, 3) {
				if j > 0 {
					head = strings.Repeat(" ", 10)
				}
				lines = append(lines, head+" "+c(key, l))
			}
		}
		lines = append(lines, "", c("reply!", "enter")+c("mute", " send to ")+c("opus!", st.Short(f.w.Asker))+c("dim", "   ← back   esc later"))
	} else {
		q, a := f.qs()[f.tab], f.answers[f.tab]
		for _, l := range wrapLines(q.Question, iw, 3) {
			lines = append(lines, c("hi!", l))
		}
		if q.MultiSelect {
			lines = append(lines, c("dim", "pick any: space toggles, enter goes on"))
		}
		lines = append(lines, "")
		for o, opt := range q.Options {
			cur := f.row[f.tab] == o
			mark := pick(q.MultiSelect, "[ ]", "( )")
			if f.picked(f.tab, o) {
				mark = pick(q.MultiSelect, "[x]", "(•)")
			}
			pre := "  "
			lk := "fg"
			if cur {
				pre, lk = c("hub!", "❯ "), "hi!"
			}
			lines = append(lines, pre+c(pick(f.picked(f.tab, o), "reply!", "dim"), mark)+" "+c("dim", fmt.Sprintf("%d. ", o+1))+c(lk, opt.Label))
			for _, l := range wrapLines(opt.Description, iw-9, 2) {
				lines = append(lines, "         "+c("mute", l))
			}
		}
		pre, other := "  ", c("dim", "Other… type your own answer")
		if f.onOther() {
			pre = c("hub!", "❯ ")
			other = c("hi", a.Other) + cursor(frame)
			if a.Other == "" {
				other = c("dim", "type your own answer") + cursor(frame)
			}
		} else if a.Other != "" {
			other = c("fg", a.Other)
		}
		lines = append(lines, pre+c(pick(a.Other != "", "reply!", "dim"), pick(q.MultiSelect, "[ ]", "( )"))+" "+c("dim", fmt.Sprintf("%d. ", len(q.Options)+1))+other)
		lines = append(lines, "", c("dim", "↑↓ choose · enter pick · 1-9 quick pick · ←→ questions · esc later"))
	}
	if len(lines) > maxH-2 {
		lines = lines[:maxH-2]
	}
	title := c("ask!", "QUESTIONS") + c("dim", " · from ") + c("opus!", st.Short(f.w.Asker))
	right := c("dim", fmt.Sprintf("%d of %d", min(f.tab+1, len(f.qs())), len(f.qs())))
	for i := range lines {
		if ansi.StringWidth(lines[i]) > iw {
			lines[i] = ansi.Truncate(lines[i], iw, "…")
		}
	}
	return strings.Split(box(w, len(lines)+2, title, right, lines, true), "\n")
}
