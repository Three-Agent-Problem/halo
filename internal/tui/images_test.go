package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestImagePath(t *testing.T) {
	dir := t.TempDir()
	shot := filepath.Join(dir, "Screen Shot 1.png")
	if err := os.WriteFile(shot, []byte("\x89PNG fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	txt := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(txt, []byte("x"), 0o644)
	for in, want := range map[string]string{
		shot:                                shot,
		strings.ReplaceAll(shot, " ", `\ `): shot, // dragged in, shell-escaped
		"'" + shot + "'":                    shot,
		"  " + shot + "\n":                  shot,
		"file://" + strings.ReplaceAll(shot, " ", "%20"): shot,
		txt:                               "",
		filepath.Join(dir, "missing.png"): "",
		"just some text about a.png":      "",
		"relative.png":                    "",
	} {
		if got := imagePath(in); got != want {
			t.Errorf("imagePath(%q) = %q, want %q", in, got, want)
		}
	}
}

type sendRec struct {
	*Demo
	sent string
}

func (s *sendRec) Send(_ context.Context, text string) (string, error) {
	s.sent = text
	return "root", nil
}

func TestImageChipInPrompt(t *testing.T) {
	dir := t.TempDir()
	shot := filepath.Join(dir, "bug.png")
	if err := os.WriteFile(shot, []byte("\x89PNG fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &sendRec{Demo: &Demo{}}
	m := newModel(context.Background(), Options{Backend: rec, State: demoState(), ImageDir: filepath.Join(dir, "images")})
	m.w, m.h = 160, 44
	m.sizeInput()
	m.normalKey("i")
	for _, r := range "fix this " {
		m.key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.Update(tea.PasteMsg{Content: shot})
	if v := m.input.Value(); v != "fix this [img1 bug.png] " {
		t.Fatalf("input %q", v)
	}
	_, cmd := m.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter sends")
	}
	cmd()
	copied, _ := filepath.Glob(filepath.Join(dir, "images", "*-bug.png"))
	if len(copied) != 1 {
		t.Fatalf("copied %v", copied)
	}
	if want := "fix this (image: " + copied[0] + "; open it with the Read tool)"; rec.sent != want {
		t.Fatalf("sent %q\nwant %q", rec.sent, want)
	}
}
