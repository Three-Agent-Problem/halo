package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestQRLinesHaveEqualWidth(t *testing.T) {
	for _, text := range []string{"hi", "https://t.me/example_bot?start=abc123"} {
		lines := QR(text)
		if len(lines) == 0 {
			t.Fatalf("QR(%q) returned no lines", text)
		}
		want := ansi.StringWidth(lines[0])
		if want == 0 {
			t.Fatalf("QR(%q) first line has zero width", text)
		}
		for i, l := range lines {
			if got := ansi.StringWidth(l); got != want {
				t.Errorf("QR(%q) line %d width = %d, want %d", text, i, got, want)
			}
		}
	}
}

func TestQRQuietZoneIsLight(t *testing.T) {
	light := qrCells[[2]bool{false, false}]
	dark := qrCells[[2]bool{true, true}]
	if light == dark {
		t.Fatalf("light and dark cells render the same (%q); the quiet-zone check would be vacuous", light)
	}
	lines := QR("https://t.me/example_bot?start=abc123")
	if len(lines) == 0 {
		t.Fatal("QR returned no lines")
	}
	width := ansi.StringWidth(lines[0])
	if got := lines[0]; got != strings.Repeat(light, width) {
		t.Errorf("first line is not a full light quiet zone")
	}
	if got := lines[len(lines)-1]; got != strings.Repeat(light, width) {
		t.Errorf("last line is not a full light quiet zone")
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, strings.Repeat(light, 2)) {
			t.Errorf("line %d: left quiet zone is not light", i)
		}
		if !strings.HasSuffix(l, strings.Repeat(light, 2)) {
			t.Errorf("line %d: right quiet zone is not light", i)
		}
	}
}

func TestQREdgeInputsDoNotPanic(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("QR(\"\") panicked: %v", r)
			}
		}()
		_ = QR("")
	})
	t.Run("too long", func(t *testing.T) {
		if got := QR(strings.Repeat("x", 5000)); got != nil {
			t.Errorf("QR of too-long input = %d lines, want nil", len(got))
		}
	})
}
