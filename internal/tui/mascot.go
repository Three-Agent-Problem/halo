package tui

import (
	"strings"
	"time"
)

// The mascot: Pocket, a 5×3 body under a (═══) halo that bobs a row. Its eyes
// are holes ("▄" in the body color leaves the top half to the background);
// "█" closes them (a blink) and "▀" moves them down (a happy squint).
const mascotW = 5

var mascotBody = []string{" ▄▄▄ ", "█E█E█", "▀▀▀▀▀"}

// mascotSteps is the idle loop: halo up, down, a blink, a squint.
var mascotSteps = []struct {
	up   bool
	eyes string
	d    time.Duration
}{
	{true, "▄", 550 * time.Millisecond}, {false, "▄", 550 * time.Millisecond},
	{true, "▄", 550 * time.Millisecond}, {false, "▄", 550 * time.Millisecond},
	{true, "█", 120 * time.Millisecond}, {true, "▄", 550 * time.Millisecond},
	{false, "▄", 550 * time.Millisecond}, {false, "▀", 700 * time.Millisecond},
	{false, "▄", 400 * time.Millisecond},
}

// MascotFrame is the mascot at t into its loop, uncolored: five rows of five
// cells (halo, gap, body). Callers that print plain text use it.
func MascotFrame(t time.Duration) []string {
	var total time.Duration
	for _, s := range mascotSteps {
		total += s.d
	}
	t %= total
	step := mascotSteps[len(mascotSteps)-1]
	for _, s := range mascotSteps {
		if t < s.d {
			step = s
			break
		}
		t -= s.d
	}
	halo, gap := "(═══)", strings.Repeat(" ", mascotW)
	rows := []string{halo, gap}
	if !step.up {
		rows = []string{gap, halo}
	}
	for _, r := range mascotBody {
		rows = append(rows, strings.ReplaceAll(r, "E", step.eyes))
	}
	return rows
}

// mascot is the colored mascot for a UI frame that advances every tick.
func mascot(frame int, tick time.Duration) []string {
	rows := MascotFrame(time.Duration(frame) * tick)
	for i, r := range rows {
		if i < 2 {
			rows[i] = c("halo", r)
		} else {
			rows[i] = c("mascot", r)
		}
	}
	return rows
}

// MascotANSI is a still mascot in 24-bit color, for text printed outside the
// TUI (usage, goodbyes).
func MascotANSI() []string {
	rows := MascotFrame(0)
	for i, r := range rows {
		col := "\x1b[38;2;230;233;238m" // fog
		if i < 2 {
			col = "\x1b[38;2;240;69;92m" // crimson
		}
		rows[i] = col + r + "\x1b[0m"
	}
	return rows
}

// beside puts the mascot to the left of text lines, top-aligned.
func beside(m []string, text []string) []string {
	n := max(len(m), len(text))
	out := make([]string, n)
	for i := range n {
		l := strings.Repeat(" ", mascotW)
		if i < len(m) {
			l = m[i]
		}
		if i < len(text) {
			l += "  " + text[i]
		}
		out[i] = l
	}
	return out
}
