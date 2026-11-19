package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/three-agent-problem/lagrange/report"

	"github.com/three-agent-problem/halo/internal/lg"
	"github.com/three-agent-problem/halo/internal/plan"
)

func TestRenderMarkdown(t *testing.T) {
	md := "# Done\n\nBuilt **two** things with `go test`:\n\n- one\n- a very long bullet that wraps around the edge of the box\n1. first\n> a quote\n```\ncode line\n```"
	out := ansi.Strip(strings.Join(renderMarkdown(md, 30), "\n"))
	for _, want := range []string{"DONE", "Built two things with go", "  • one", "  • a very long bullet that", "    wraps", "  1. first", "│ a quote", "│ code line"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "**") || strings.Contains(out, "`") || strings.Contains(out, "```") {
		t.Errorf("markup left in:\n%s", out)
	}
}

// The summary opens full screen with o, scrolls, and takes a question for the
// lead while open; the answer shows at its end. No run starts.
func TestReaderAsksAboutTheRun(t *testing.T) {
	m, b := btwModel(t, "")
	m.h = 24
	now := time.Now()
	m.st.Apply(lg.Entry{Seq: 5, At: now, Kind: "artifact", TaskID: "root-1", Sender: "halo-lead", Recipient: "ada", Summary: "report",
		Data: map[string]any{"artifactId": report.ArtifactName, "report": report.Report{Headline: "Dark mode", Status: "landed on main",
			Outcome: strings.Repeat("A line of what happened. ", 60)}.Map()}}, now)
	m.st.Apply(lg.Entry{Seq: 6, At: now, Kind: "task", TaskID: "root-1", Sender: "ada", Recipient: "halo-lead", State: stCompleted}, now)
	if !strings.Contains(m.st.Comms[len(m.st.Comms)-1].Text, "/summary reads it all") {
		t.Fatalf("ending %q", m.st.Comms[len(m.st.Comms)-1].Text)
	}

	m.command("/summary")
	m.key(tea.KeyPressMsg{Code: 'o', Text: "o"})
	if m.reader == nil || m.summary {
		t.Fatal("o should open the summary full screen")
	}
	out := ansi.Strip(m.render(now))
	if !strings.Contains(out, "DARK MODE") && !strings.Contains(out, "Dark mode") {
		t.Fatalf("reader:\n%s", out)
	}
	m.key(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m.render(now)
	if m.reader.off != 1 {
		t.Fatalf("j scrolled to %d", m.reader.off)
	}

	m.key(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.mode != modePrompt {
		t.Fatal("a should open the prompt")
	}
	typeText(m, "why opus for S2?")
	if _, cmd := m.key(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		cmd()
	}
	if b.asked != "why opus for S2?" || b.sent != "" || m.confirm != nil || m.reader == nil {
		t.Fatalf("asked %q, sent %q, confirm %v, reader %v", b.asked, b.sent, m.confirm, m.reader)
	}
	tags := map[string]any{"tags": map[string]any{"run": "root-1", BtwTag: "1"}}
	m.st.Apply(lg.Entry{Seq: 7, At: now, Kind: "task", TaskID: "b1", ParentTaskID: "root-1", Sender: "halo-lead", Recipient: "halo-lead", State: stSubmitted, Summary: "why opus for S2?", Data: tags}, now)
	m.st.Apply(lg.Entry{Seq: 8, At: now, Kind: "task", TaskID: "b1", ParentTaskID: "root-1", Sender: "halo-lead", Recipient: "halo-lead", State: stCompleted, Summary: "Sonnet failed the tests twice.", Data: tags}, now)
	out = ansi.Strip(m.render(now))
	if !strings.Contains(out, "Sonnet failed the tests twice.") {
		t.Fatalf("the answer isn't at the end:\n%s", out)
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.reader != nil {
		t.Fatal("esc should close it")
	}
}

// Each subtask ending says what it means and what happens next.
func TestTransitionLines(t *testing.T) {
	m := boardModel()
	now := time.Now()
	set := func(id, status, note string) string {
		p := *m.st.Plan
		p.Subtasks = append([]*plan.Subtask(nil), p.Subtasks...)
		for i, s := range p.Subtasks {
			if s.ID == id {
				c := *s
				c.Status, c.Note = status, note
				p.Subtasks[i] = &c
			}
		}
		n := len(m.st.Comms)
		m.st.setPlan(&p, now)
		if len(m.st.Comms) == n {
			return ""
		}
		return m.st.Comms[len(m.st.Comms)-1].Text
	}
	for _, tc := range []struct{ id, status, note, want string }{
		{"S2", plan.Ready, "", "S2 finished its work · @lead sends it to review next"},
		{"S2", plan.Review, "", "S2 is with @reviewer for review"},
		{"S2", plan.Changes, "missing tests", "↺ S2 needs changes: missing tests · @lead sends them back"},
		{"S3", plan.Failed, "npm test failed", "✗ S3 failed: npm test failed · @lead decides what next"},
		{"S2", plan.Done, "", "✓ S2 merged into the run branch · 3 to go"},
	} {
		if got := set(tc.id, tc.status, tc.note); !strings.Contains(got, tc.want) {
			t.Errorf("%s %s: %q, want %q", tc.id, tc.status, got, tc.want)
		}
	}
	if got := m.st.ending(stFailed, "boom\nmore"); !strings.Contains(got, "✗ run ended: boom") || !strings.Contains(got, "type to ask") {
		t.Errorf("failed ending %q", got)
	}
}
