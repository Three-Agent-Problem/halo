package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The palette of the design mock.
var palette = map[string]string{
	"bg": "#07090a", "panel": "#0b0f0f", "line": "#1c2624", "line2": "#2a3835",
	"dim": "#4f625c", "mute": "#7d918a", "fg": "#cfdcd5", "hi": "#f2fff8",
	"haiku": "#7ee787", "sonnet": "#56d4ef", "opus": "#c9a2ff", "human": "#ff8fb3",
	"hub": "#ffcf6b", "task": "#56d4ef", "ask": "#ffb547", "reply": "#7ee787",
	"review": "#c9a2ff", "fail": "#ff6b6b",
}

var styles = map[string]lipgloss.Style{}

func init() {
	for k, v := range palette {
		styles[k] = lipgloss.NewStyle().Foreground(lipgloss.Color(v))
		styles[k+"!"] = lipgloss.NewStyle().Foreground(lipgloss.Color(v)).Bold(true)
	}
	styles["alert"] = lipgloss.NewStyle().Foreground(lipgloss.Color(palette["bg"])).Background(lipgloss.Color(palette["ask"])).Bold(true)
	styles["badge"] = lipgloss.NewStyle().Foreground(lipgloss.Color(palette["bg"])).Bold(true)
	styles["cursor"] = lipgloss.NewStyle().Foreground(lipgloss.Color(palette["bg"])).Background(lipgloss.Color(palette["haiku"]))
}

// c colors s with a palette key; a trailing "!" makes it bold.
func c(key, s string) string {
	st, ok := styles[key]
	if !ok {
		return s
	}
	return st.Render(s)
}

// badge renders s on a background of the palette color key.
func badge(key, s string) string {
	return styles["badge"].Background(lipgloss.Color(palette[key])).Render(s)
}

// modelKey is the palette key of a model.
func modelKey(model string) string {
	switch model {
	case "haiku", "sonnet", "opus", "human":
		return model
	}
	return "fg"
}

// fit truncates or pads a styled string to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	if n := w - ansi.StringWidth(s); n > 0 {
		s += strings.Repeat(" ", n)
	}
	return s
}

// pad pads or cuts plain s to n cells.
func pad(s string, n int) string { return fit(s, n) }

// box draws a pane with a title cut into its top border, like the mock: w x h cells
// including the border, with lines inside (one space of padding on each side).
func box(w, h int, title, right string, lines []string, hot bool) string {
	if w < 6 || h < 3 {
		return ""
	}
	bc := "line2"
	if hot {
		bc = "mute"
	}
	top := c(bc, "┌─") + " " + title + " "
	tail := ""
	if right != "" {
		tail = " " + right + " " + c(bc, "─┐")
	} else {
		tail = c(bc, "─┐")
	}
	fill := w - ansi.StringWidth(top) - ansi.StringWidth(tail)
	if fill < 1 {
		top = ansi.Truncate(top, w-ansi.StringWidth(tail)-1, "…")
		fill = w - ansi.StringWidth(top) - ansi.StringWidth(tail)
	}
	var b strings.Builder
	b.WriteString(top + c(bc, strings.Repeat("─", max(fill, 0))) + tail)
	inner := w - 4
	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		b.WriteString("\n" + c(bc, "│") + " " + fit(line, inner) + " " + c(bc, "│"))
	}
	b.WriteString("\n" + c(bc, "└"+strings.Repeat("─", w-2)+"┘"))
	return b.String()
}

// spinner frames.
var spin = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// bar is a progress bar of n cells.
func bar(p float64, n int) string {
	f := int(p*float64(n) + .5)
	f = max(0, min(n, f))
	return strings.Repeat("━", f) + strings.Repeat("╌", n-f)
}

// sparkline renders values as block characters.
func sparkline(v []int) string {
	const blocks = "▁▂▃▄▅▆▇█"
	mx := 3
	for _, x := range v {
		mx = max(mx, x)
	}
	r := []rune(blocks)
	var b strings.Builder
	for _, x := range v {
		b.WriteRune(r[min(7, x*7/mx)])
	}
	return b.String()
}
