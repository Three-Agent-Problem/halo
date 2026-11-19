package tui

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The run summary opened full screen (o on the summary card): the run's report,
// the lead's final answer, and your questions about the run with the lead's
// answers, as Markdown you scroll. a asks the lead more while you read; no run
// starts. c copies it all.

// reader is the open summary.
type reader struct {
	off    int  // the first line shown
	follow bool // stay at the end, so a new answer shows as it arrives
}

// summaryMarkdown is everything the reader shows, as Markdown.
func (s *State) summaryMarkdown() string {
	var b strings.Builder
	if r := s.summaryReport(); r != nil {
		b.WriteString(r.Markdown())
	} else {
		b.WriteString("**Run " + RunShort(s.Root) + "**\n" + short(s.RootState))
	}
	if p := strings.TrimSpace(s.Prompt); p != "" {
		b.WriteString("\n\n## What you asked for\n\n" + p)
	}
	if t := s.Tasks[s.Root]; t != nil && terminal(s.RootState) {
		if ans := strings.TrimSpace(t.Answer()); ans != "" {
			b.WriteString("\n\n## The lead's report\n\n" + ans)
		}
	}
	var qa []string
	for _, cm := range s.Comms {
		text, ok := strings.CutPrefix(cm.Text, "btw · ")
		if !ok {
			continue
		}
		switch cm.Kind {
		case "ask":
			qa = append(qa, "**You:** "+text)
		case "reply":
			qa = append(qa, "**"+s.Short(s.Lead)+":** "+text)
		case "fail":
			qa = append(qa, "**"+s.Short(s.Lead)+" couldn't answer:** "+text)
		}
	}
	if len(qa) > 0 {
		b.WriteString("\n\n## Your questions\n\n" + strings.Join(qa, "\n\n"))
	}
	return b.String()
}

var inlineRe = regexp.MustCompile("\\*\\*[^*]+\\*\\*|`[^`]+`")

// inline styles **bold** and `code` in one line of Markdown.
func inline(line, base string) string {
	var out strings.Builder
	last := 0
	for _, m := range inlineRe.FindAllStringIndex(line, -1) {
		out.WriteString(c(base, line[last:m[0]]))
		tok := line[m[0]:m[1]]
		if strings.HasPrefix(tok, "**") {
			out.WriteString(c("hi!", strings.Trim(tok, "*")))
		} else {
			out.WriteString(c("task", strings.Trim(tok, "`")))
		}
		last = m[1]
	}
	out.WriteString(c(base, line[last:]))
	return out.String()
}

// renderMarkdown draws Markdown as styled lines w cells wide: headings,
// bullets, numbered lists, quotes, code blocks, **bold** and `code`.
func renderMarkdown(md string, w int) []string {
	w = max(w, 20)
	var out []string
	wrap := func(text, first, rest string) {
		for i, l := range strings.Split(ansi.Wordwrap(text, w-ansi.StringWidth(first), ""), "\n") {
			out = append(out, pick(i == 0, first, rest)+l)
		}
	}
	code := false
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "```"):
			code = !code
		case code:
			out = append(out, c("dim", "│ ")+c("mute", ansi.Truncate(line, w-2, "…")))
		case trim == "":
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
		case strings.HasPrefix(trim, "#"):
			text := strings.TrimSpace(strings.TrimLeft(trim, "#"))
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			wrap(c("hub!", strings.ToUpper(text)), "", "")
		case strings.HasPrefix(trim, "- ") || strings.HasPrefix(trim, "* "):
			wrap(inline(trim[2:], "fg"), c("reply", "  • "), "    ")
		case numbered(trim):
			n, rest, _ := strings.Cut(trim, " ")
			wrap(inline(rest, "fg"), c("mute", "  "+n+" "), strings.Repeat(" ", len(n)+3))
		case strings.HasPrefix(trim, ">"):
			wrap(inline(strings.TrimSpace(trim[1:]), "mute"), c("dim", "│ "), c("dim", "│ "))
		default:
			wrap(inline(trim, "fg"), "", "")
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func numbered(s string) bool {
	n, _, ok := strings.Cut(s, " ")
	if !ok || len(n) < 2 || len(n) > 4 || !strings.HasSuffix(n, ".") {
		return false
	}
	for _, r := range n[:len(n)-1] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// readerBox draws the open summary, w × h.
func (m *model) readerBox(w, h int) []string {
	lines := renderMarkdown(m.st.summaryMarkdown(), w-6)
	view := h - 4
	end := max(0, len(lines)-view)
	if m.reader.follow || m.reader.off > end {
		m.reader.off = end
	}
	m.reader.off = max(0, m.reader.off)
	shown := lines[m.reader.off:min(len(lines), m.reader.off+view)]
	body := make([]string, 0, view+2)
	for _, l := range shown {
		body = append(body, "  "+l)
	}
	for len(body) < view {
		body = append(body, "")
	}
	pos := "all"
	if len(lines) > view {
		pos = pick(m.reader.off >= end, "end", strconv.Itoa(m.reader.off+1)+"–"+strconv.Itoa(m.reader.off+len(shown))+" of "+strconv.Itoa(len(lines)))
	}
	keys := "  " + c("reply!", "a") + c("mute", " ask the lead about it   ") + c("hub!", "↑↓ j/k space") + c("mute", " scroll   ") +
		c("hub!", "c") + c("mute", " copy   ") + c("dim", "esc") + c("mute", " close")
	body = append(body, "", keys)
	return strings.Split(box(w, h, c("reply!", "RUN SUMMARY"), c("dim", pos), body, true), "\n")
}

// readerKey handles a key while the summary is open.
func (m *model) readerKey(key string) tea.Cmd {
	r := m.reader
	page := max(1, m.h-12)
	switch key {
	case "j", "down":
		r.off++
	case "k", "up":
		r.off, r.follow = r.off-1, false
	case "space", "pgdown", "d", "ctrl+d":
		r.off += page
	case "pgup", "u", "ctrl+u":
		r.off, r.follow = r.off-page, false
	case "g", "home":
		r.off, r.follow = 0, false
	case "G", "end":
		r.follow = true
	case "c":
		return m.copyText(m.st.summaryMarkdown(), "summary")
	case "a", "i":
		m.mode = modePrompt
		m.setInput("")
		return m.startInput("ask " + m.st.Short(m.st.Lead) + " about this run · no new run starts")
	case "esc", "q", "o":
		m.reader = nil
	}
	return nil
}

// openReader opens the summary full screen, at the top.
func (m *model) openReader() {
	m.summary, m.reader = false, &reader{}
}

// copyText copies text: with the system's clipboard tool when there is one,
// since many terminals ignore the escape sequence (OSC 52) Bubble Tea sends;
// with that sequence otherwise.
func (m *model) copyText(text, what string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		if err := systemCopy(ctx, text); err == nil {
			return doneMsg{what + " copied as Markdown", nil}
		}
		return osc52Msg{text, what}
	}
}

// osc52Msg asks the terminal to copy text, when the system has no clipboard tool.
type osc52Msg struct{ text, what string }

var errNoClipboard = errors.New("no clipboard tool")

func systemCopy(ctx context.Context, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var tools [][]string
	switch runtime.GOOS {
	case "darwin":
		tools = [][]string{{"pbcopy"}}
	case "windows":
		tools = [][]string{{"clip"}}
	default:
		tools = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
	for _, t := range tools {
		if _, err := exec.LookPath(t[0]); err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, t[0], t[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	return errNoClipboard
}
