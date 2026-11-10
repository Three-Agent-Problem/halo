package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/three-agent-problem/lagrange/report"
)

// The run summary: the run's situation report (what it built, what it cost), as a
// card that opens when the run lands and with /summary. c copies it as Markdown.

// summaryReport is the report with what only the TUI knows added: the team's cost.
func (s *State) summaryReport() *report.Report {
	if s.Report == nil {
		return nil
	}
	r := *s.Report
	r.Stats = append([]report.Stat(nil), r.Stats...)
	total, currency := 0.0, ""
	for _, a := range s.Agents {
		total += a.Usage.Cost()
		if currency == "" {
			currency = a.Usage.Currency
		}
	}
	if total > 0 {
		v := fmt.Sprintf("$%.2f", total)
		if currency != "" && currency != "USD" {
			v = fmt.Sprintf("%.2f %s", total, currency)
		}
		r.Stats = append(r.Stats, report.Stat{Label: "Team cost", Value: v})
	}
	return &r
}

// summaryBox draws the summary card, w cells wide.
func (s *State) summaryBox(w, maxH int) []string {
	r := s.summaryReport()
	if r == nil {
		return nil
	}
	inner := w - 6
	wrap := func(text string, style string, indent string) []string {
		var out []string
		for _, l := range strings.Split(ansi.Wordwrap(text, inner-len(indent), ""), "\n") {
			out = append(out, "  "+indent+c(style, l))
		}
		return out
	}
	lines := []string{""}
	lines = append(lines, wrap(r.Headline, "hi!", "")...)
	status := r.Status
	if p := r.Progress; p != nil && p.Total > 0 {
		status += fmt.Sprintf(" · %d/%d done", p.Done, p.Total)
	}
	if status != "" {
		key := "mute"
		if r.Final() {
			key = "reply!"
		}
		lines = append(lines, "  "+c(key, status))
	}
	if r.Outcome != "" {
		lines = append(lines, "")
		lines = append(lines, wrap(r.Outcome, "fg", "")...)
	}
	if len(r.Built) > 0 {
		lines = append(lines, "", "  "+c("hub!", "BUILT"))
		for _, it := range r.Built {
			text := c("reply", "✓ ") + c("fg", it.Title)
			if it.Detail != "" {
				text += c("mute", " · "+it.Detail)
			}
			lines = append(lines, "  "+fit(text, inner))
		}
	}
	if len(r.Stats) > 0 {
		lines = append(lines, "")
		var parts []string
		for _, st := range r.Stats {
			parts = append(parts, c("mute", st.Label+" ")+c("hi", st.Value))
		}
		row := ""
		for _, p := range parts {
			if row != "" && ansi.StringWidth(row)+3+ansi.StringWidth(p) > inner {
				lines = append(lines, "  "+row)
				row = ""
			}
			if row != "" {
				row += c("dim", " · ")
			}
			row += p
		}
		if row != "" {
			lines = append(lines, "  "+row)
		}
	}
	lines = append(lines, "", "  "+c("reply!", "c")+c("mute", " copy as Markdown   ")+c("reply!", "esc")+c("mute", " close"), "")
	if len(lines)+2 > maxH {
		lines = append(lines[:maxH-4], "  "+c("dim", "…"), "")
	}
	return strings.Split(box(w, len(lines)+2, c("reply!", "RUN SUMMARY"), "", lines, true), "\n")
}
