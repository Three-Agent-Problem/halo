package tui

import (
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestMascotFrames(t *testing.T) {
	seen := map[string]bool{}
	for ms := 0; ms < 10_000; ms += 40 {
		rows := MascotFrame(time.Duration(ms) * time.Millisecond)
		if len(rows) != 5 {
			t.Fatalf("%dms: %d rows, want 5", ms, len(rows))
		}
		for _, r := range rows {
			if w := ansi.StringWidth(r); w != mascotW {
				t.Fatalf("%dms: row %q is %d wide, want %d", ms, r, w, mascotW)
			}
		}
		seen[rows[0]+rows[3]] = true
	}
	// halo up and down, eyes open, shut and happy all show up in the loop
	for _, want := range []string{"(═══)█▄█▄█", "     █▄█▄█", "(═══)█████", "     █▀█▀█"} {
		if !seen[want] {
			t.Errorf("loop never shows %q", want)
		}
	}
	for _, l := range mascot(3, 80*time.Millisecond) {
		if w := ansi.StringWidth(l); w != mascotW {
			t.Errorf("colored row %q is %d wide", l, w)
		}
	}
}
