package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/three-agent-problem/halo/internal/plan"
)

func TestFindToken(t *testing.T) {
	for _, c := range []struct {
		v    string
		cur  int
		kind byte
		text string
	}{
		{"ask @son", 8, '@', "@son"},
		{"ask @son now", 8, '@', "@son"},
		{"fix #S3 please", 7, '#', "#S3"},
		{"see internal/tui/app.go", 23, 'p', "internal/tui/app.go"},
		{"see ./x", 7, 'p', "./x"},
		{"hello", 5, 0, ""},
		{"hello ", 6, 0, ""},
		{"", 0, 0, ""},
	} {
		tok, ok := FindToken(c.v, c.cur)
		if (c.kind != 0) != ok || tok.Kind != c.kind || tok.Text != c.text {
			t.Errorf("%q@%d: %+v %v", c.v, c.cur, tok, ok)
		}
	}
}

var testSrc = Sources{
	Agents:   []Candidate{{"@ada", "human"}, {"@lead", "lead"}, {"@sonnet-1", "worker"}, {"@sonnet-2", "worker"}, {"@reviewer", "reviewer"}},
	Subtasks: []Candidate{{"#S1", "Plan parser"}, {"#S3", "Prompt autocomplete"}},
	Files:    []string{"README.md", "internal/tui/app.go", "internal/tui/complete.go", "cmd/halo/main.go", "go.mod"},
}

func texts(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Text)
	}
	return out
}

func TestComplete(t *testing.T) {
	for _, c := range []struct {
		v    string
		want []string
	}{
		{"@", []string{"@ada", "@lead", "@sonnet-1", "@sonnet-2", "@reviewer"}},
		{"@son", []string{"@sonnet-1", "@sonnet-2"}},
		{"@view", []string{"@reviewer"}}, // substring after the prefix
		{"@sonnet-1", nil},               // already complete
		{"@zzz", nil},
		{"#", []string{"#S1", "#S3"}},
		{"#S3", nil},
		{"#auto", []string{"#S3"}}, // by title
		{"internal/tui/c", []string{"internal/tui/complete.go"}},
		{"app.go", []string{"internal/tui/app.go"}},
		{"main", nil}, // no / or . : not a path
		{"./cmd", []string{"cmd/halo/main.go"}},
		{"itac", nil},
		{"ic.go", []string{"internal/tui/complete.go"}}, // fuzzy
	} {
		_, got := Complete(c.v, len([]rune(c.v)), testSrc)
		if !reflect.DeepEqual(texts(got), c.want) {
			t.Errorf("%q: %v want %v", c.v, texts(got), c.want)
		}
	}
	// ranking: prefix before file-name prefix before substring; shorter first
	src := Sources{Files: []string{"a/long/path/x.go", "x.go", "b/x.go", "xx/y.go"}}
	_, got := Complete("x.", 2, src)
	if !reflect.DeepEqual(texts(got), []string{"x.go", "b/x.go", "a/long/path/x.go", "xx/y.go"}) {
		t.Errorf("ranking: %v", texts(got))
	}
	var many []string
	for i := 0; i < 30; i++ {
		many = append(many, "dir/f"+strings.Repeat("a", i)+".go")
	}
	if _, got := Complete("dir/", 4, Sources{Files: many}); len(got) != maxCands {
		t.Errorf("cap: %d", len(got))
	}
}

func TestAccept(t *testing.T) {
	tok, _ := FindToken("hi @son there", 7)
	v, cur := Accept("hi @son there", tok, Candidate{Text: "@sonnet-1"})
	if v != "hi @sonnet-1 there" || cur != 13 {
		t.Fatalf("%q %d", v, cur)
	}
	tok, _ = FindToken("@son", 4)
	if v, cur := Accept("@son", tok, Candidate{Text: "@sonnet-1"}); v != "@sonnet-1 " || cur != 10 {
		t.Fatalf("%q %d", v, cur)
	}
}

func TestExpandMentions(t *testing.T) {
	short := map[string]string{"sonnet-1": "halo-sonnet-1", "lead": "halo-lead", "ada": "ada"}
	for in, want := range map[string]string{
		"ask @sonnet-1 to look":     "ask @halo-sonnet-1 to look",
		"@lead, see #S3 and a/b.go": "@halo-lead, see #S3 and a/b.go",
		"@halo-lead stays":          "@halo-lead stays",
		"@nobody stays":             "@nobody stays",
		"mail me@sonnet-1 stays":    "mail me@sonnet-1 stays",
		"@sonnet-1 and @sonnet-1.":  "@halo-sonnet-1 and @halo-sonnet-1.",
		"(@ada)":                    "(@ada)",
	} {
		if got := ExpandMentions(in, short); got != want {
			t.Errorf("%q: %q want %q", in, got, want)
		}
	}
}

type sendBackend struct {
	*Demo
	sent  string
	files []string
}

func (b *sendBackend) Send(_ context.Context, text string) (string, error) {
	b.sent = text
	return "", nil
}
func (b *sendBackend) Files(context.Context) ([]string, error) { return b.files, nil }

func typeText(m *model, s string) {
	for _, r := range s {
		m.key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func newTestModel() (*model, *sendBackend) {
	b := &sendBackend{Demo: &Demo{}}
	m := newModel(context.Background(), Options{Backend: b, State: demoState()})
	m.w, m.h = 160, 44
	m.sizeInput()
	m.st.Plan = &plan.Plan{Subtasks: []*plan.Subtask{{ID: "S3", Title: "Prompt autocomplete"}}}
	m.mode = modePrompt
	m.input.Focus()
	return m, b
}

func TestPopupModel(t *testing.T) {
	m, _ := newTestModel()
	typeText(m, "@son")
	out := stripANSI(m.render(timeNow()))
	if !strings.Contains(out, "@sonnet-1") || !strings.Contains(out, "AGENTS") {
		t.Fatalf("popup not drawn:\n%s", out)
	}
	checkFrame(t, m.render(timeNow()), 160, 44, "popup")
	m.key(keyPress("tab"))
	if got := m.input.Value(); got != "@sonnet-1 " {
		t.Fatalf("tab: %q", got)
	}
	if len(m.popupCands()) != 0 {
		t.Fatal("popup stays open after accept")
	}
	// down + enter picks the second; enter with the popup closed sends
	m.setInput("")
	typeText(m, "@son")
	m.key(tea.KeyPressMsg{Code: tea.KeyDown})
	m.key(keyPress("enter"))
	if got := m.input.Value(); got != "@sonnet-2 " || m.mode != modePrompt {
		t.Fatalf("enter accepts: %q mode %d", got, m.mode)
	}
	typeText(m, "see #S3")
	if cs := m.popupCands(); len(cs) != 0 {
		t.Fatalf("complete id: %v", cs)
	}
	m.key(keyPress("enter"))
	m.mode = modePrompt
	// esc closes the popup first, then the prompt
	m.setInput("@l")
	m.key(keyPress("esc"))
	if m.mode != modePrompt || len(m.popupCands()) != 0 {
		t.Fatalf("esc closes the popup first: mode %d", m.mode)
	}
	m.key(keyPress("esc"))
	if m.mode != modeNormal {
		t.Fatal("second esc leaves")
	}
}

func TestSendExpandsMentions(t *testing.T) {
	m, b := newTestModel()
	m.setInput("@sonnet-1 check #S3 in a/b.go")
	_, cmd := m.key(keyPress("enter"))
	if cmd == nil {
		t.Fatal("no send")
	}
	cmd()
	if b.sent != "@halo-sonnet-1 check #S3 in a/b.go" {
		t.Fatalf("sent %q", b.sent)
	}
}

func TestPathPopupAndFiles(t *testing.T) {
	m, b := newTestModel()
	b.files = []string{"internal/tui/app.go", "go.mod"}
	m.mode = modeNormal
	cmd := m.startInput(promptHint)
	m.mode = modePrompt
	m.Update(m.wantFilesMsg(b))
	_ = cmd
	typeText(m, "look at internal/tui/a")
	if cs := m.popupCands(); len(cs) != 1 || cs[0].Text != "internal/tui/app.go" {
		t.Fatalf("paths: %v", cs)
	}
}

func TestSlashStillCompletes(t *testing.T) {
	m, _ := newTestModel()
	typeText(m, "/re")
	if len(m.popupCands()) != 0 {
		t.Fatal("no file popup for /commands")
	}
	m.key(keyPress("tab"))
	if got := m.input.Value(); got != "/resume" {
		t.Fatalf("tab: %q", got)
	}
}
