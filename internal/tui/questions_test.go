package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/three-agent-problem/lagrange/ask"
)

type answerRec struct {
	*Demo
	task, text, decision string
}

func (a *answerRec) Answer(_ context.Context, task, text, decision string) error {
	a.task, a.text, a.decision = task, text, decision
	return nil
}

var testForm = []ask.Question{
	{Header: "Store", Question: "Where should tasks live?", Options: []ask.Option{{Label: "Postgres (Recommended)"}, {Label: "SQLite"}}},
	{Header: "Surfaces", Question: "Which surfaces?", MultiSelect: true, Options: []ask.Option{{Label: "API"}, {Label: "Web form"}, {Label: "CLI"}}},
}

func typeKeys(m *model, keys ...string) tea.Cmd {
	var last tea.Cmd
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "left":
			msg = tea.KeyPressMsg{Code: tea.KeyLeft}
		default:
			r := []rune(k)[0]
			msg = tea.KeyPressMsg{Code: r, Text: k}
		}
		_, last = m.key(msg)
	}
	return last
}

func TestQuestionForm(t *testing.T) {
	rec := &answerRec{Demo: &Demo{}}
	m := newModel(context.Background(), Options{Backend: rec, State: demoState()})
	m.w, m.h = 160, 44
	m.waits = []Wait{{TaskID: "q1", Asker: m.st.Lead, Question: "Two questions before I plan:", Since: time.Now(), Questions: testForm}}

	typeKeys(m, "a")
	if m.mode != modeQuestions || m.qf == nil {
		t.Fatal("a opens the form")
	}
	out := stripANSI(m.render(time.Now()))
	for _, want := range []string{"QUESTIONS", "Where should tasks live?", "( ) 1. Postgres (Recommended)", "3. type your own answer"} {
		if !strings.Contains(out, want) && !strings.Contains(out, strings.Replace(want, "type your own answer", "Other… type your own answer", 1)) {
			t.Errorf("form lacks %q:\n%s", want, out)
		}
	}
	typeKeys(m, "1") // single choice: picks and moves on
	if m.qf.tab != 1 || !m.qf.picked(0, 0) {
		t.Fatalf("1 picks Postgres and moves on: tab %d", m.qf.tab)
	}
	typeKeys(m, "space", "down", "space") // API and Web form
	typeKeys(m, "down", "down")           // the Other row: keys type
	typeKeys(m, "j", "k")
	if m.qf.answers[1].Other != "jk" {
		t.Fatalf("typing on Other: %q", m.qf.answers[1].Other)
	}
	typeKeys(m, "esc")
	if m.mode != modeNormal || m.qf == nil {
		t.Fatal("esc keeps the answers for later")
	}
	typeKeys(m, "a", "enter") // Other has text: on to the review
	if !m.qf.review() {
		t.Fatalf("enter on Other goes on, tab %d", m.qf.tab)
	}
	if out := stripANSI(m.render(time.Now())); !strings.Contains(out, "Your answers") || !strings.Contains(out, "API, Web form (note: jk)") {
		t.Fatalf("review:\n%s", out)
	}
	cmd := typeKeys(m, "enter")
	if cmd == nil {
		t.Fatal("enter on the review sends")
	}
	cmd()
	if rec.task != "q1" || rec.text != "Store: Postgres (Recommended)\nSurfaces: API, Web form (note: jk)" || rec.decision != "" {
		t.Fatalf("sent %q %q %q", rec.task, rec.text, rec.decision)
	}
	if m.mode != modeNormal || len(m.waits) != 0 || m.qf != nil {
		t.Fatal("sending closes the form and drops the wait")
	}
}

func TestPlainWaitKeepsYNR(t *testing.T) {
	m := newModel(context.Background(), Options{Backend: &Demo{}, State: demoState()})
	m.w, m.h = 160, 44
	m.waits = []Wait{{TaskID: "a1", Asker: m.st.Lead, Question: "Approve this plan?", Since: time.Now()}}
	typeKeys(m, "a")
	if m.mode != modeAnswer {
		t.Fatalf("a without questions is the y/n/r answer, mode %d", m.mode)
	}
}
