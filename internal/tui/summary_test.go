package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/three-agent-problem/lagrange/report"

	"github.com/three-agent-problem/halo/internal/lg"
)

func TestSummaryOpensWhenTheRunLands(t *testing.T) {
	m := newModel(context.Background(), Options{Backend: &Demo{Human: "ada"}, State: demoState()})
	m.w, m.h = 200, 56
	now := time.Now()
	m.Update(entryMsg(lg.Entry{Seq: 1, At: now, Kind: "task", TaskID: "r1", Sender: "ada", Recipient: "halo-lead", State: stSubmitted, Summary: "build it"}))
	rep := func(seq int64, r report.Report) {
		m.Update(entryMsg(lg.Entry{Seq: seq, At: now, Kind: "artifact", TaskID: "r1", Sender: "halo-lead", Recipient: "ada",
			Summary: "report: " + r.Headline, Data: map[string]any{"artifactId": report.ArtifactName, "report": r.Map()}}))
	}
	rep(2, report.Report{Headline: "Rate limiter", Status: "working", Progress: &report.Progress{Done: 1, Total: 2}})
	if m.summary || m.st.Report == nil {
		t.Fatalf("progress report: summary=%v report=%v", m.summary, m.st.Report)
	}
	if strings.Contains(strings.Join(m.st.Agents["halo-lead"].Log, "\n"), "Rate limiter") {
		t.Fatal("the report leaked into the lead's message text")
	}
	rep(3, report.Report{Headline: "Rate limiter", Status: "landed on main", Progress: &report.Progress{Done: 2, Total: 2},
		Built: []report.Item{{Title: "S1 bucket", Detail: "sonnet"}}, Stats: []report.Stat{{Label: "Review rounds", Value: "1"}},
		Outcome: "Merged halo/abc into main."})
	if !m.summary {
		t.Fatal("the final report didn't open the summary")
	}
	out := ansi.Strip(m.render(now))
	for _, want := range []string{"RUN SUMMARY", "landed on main · 2/2 done", "✓ S1 bucket · sonnet", "Review rounds 1", "Merged halo/abc into main."} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q", want)
		}
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	if cmd == nil {
		t.Fatal("c should copy")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.summary {
		t.Fatal("esc should close it")
	}
	m.command("/summary")
	if !m.summary {
		t.Fatal("/summary should reopen it")
	}
}

func TestConnectionsWizard(t *testing.T) {
	m := newModel(context.Background(), Options{Backend: &Demo{Human: "ada"}, State: demoState()})
	m.w, m.h = 200, 56
	step := func(cmd tea.Cmd) {
		for cmd != nil {
			msg := cmd()
			if _, ok := msg.(tgPollMsg); ok {
				return
			}
			_, cmd = m.Update(msg)
		}
	}
	step(m.command("/connections"))
	if m.conn == nil || m.conn.st == nil || m.conn.st.Connected {
		t.Fatalf("wizard = %+v", m.conn)
	}
	m.Update(tea.PasteMsg{Content: "nope"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	step(cmd)
	if m.conn.err == "" || m.conn.token != "" {
		t.Fatalf("a bad token: err=%q token=%q", m.conn.err, m.conn.token)
	}
	m.Update(tea.PasteMsg{Content: " 123456789:AAH-secret-secret-secret-0123456789ab\n"})
	if out := ansi.Strip(m.render(time.Now())); strings.Contains(out, "secret") {
		t.Fatal("the token shows on screen")
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	step(cmd)
	if !m.conn.st.Connected || m.conn.link == "" {
		t.Fatalf("connected: %+v link=%q", m.conn.st, m.conn.link)
	}
	if out := ansi.Strip(m.render(time.Now())); !strings.Contains(out, m.conn.link) || !strings.Contains(out, "▀") {
		t.Fatal("no pair link or QR code")
	}
	demoTG.linkAt = time.Now().Add(-time.Minute)
	step(m.tgUpdate(tgPollMsg{}))
	if !m.conn.st.Paired || !m.tgPaired || !strings.Contains(m.conn.note, "paired") {
		t.Fatalf("paired: %+v note=%q", m.conn.st, m.conn.note)
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	step(cmd)
	if m.conn.st.Level != "questions" {
		t.Fatalf("level = %q", m.conn.st.Level)
	}
	m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if !m.conn.st.Connected || !m.conn.armed {
		t.Fatal("one d should only ask")
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	step(cmd)
	if m.conn.st.Connected || m.tgPaired || !strings.Contains(m.conn.note, "disconnected") {
		t.Fatalf("after d d: %+v", m.conn.st)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.conn != nil {
		t.Fatal("esc should close")
	}
	if strings.Contains(ansi.Strip(m.footer()), "✈") {
		t.Fatal("the paired mark stayed after disconnecting")
	}
}
