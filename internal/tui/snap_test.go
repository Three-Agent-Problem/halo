package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"fmt"
	"html"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/three-agent-problem/halo/internal/lg"
)

func demoState() *State {
	return NewState("halo", []Member{
		{Handle: "kaan", Name: "kaan", Model: "human", Role: "human"},
		{Handle: "halo-lead", Name: "lead", Model: "opus", Role: "lead"},
		{Handle: "halo-haiku-1", Name: "haiku-1", Model: "haiku", Role: "worker"},
		{Handle: "halo-sonnet-1", Name: "sonnet-1", Model: "sonnet", Role: "worker"},
		{Handle: "halo-sonnet-2", Name: "sonnet-2", Model: "sonnet", Role: "worker"},
		{Handle: "halo-opus-1", Name: "opus-1", Model: "opus", Role: "worker"},
		{Handle: "halo-reviewer", Name: "reviewer", Model: "sonnet", Role: "reviewer"},
	})
}

// playDemo runs the demo into a model for d, then returns it with the lock held
// by the caller's mu.
func playDemo(t *testing.T, ctx context.Context, mu *sync.Mutex) (*model, *Demo) {
	t.Helper()
	d := &Demo{Human: "kaan", Prefix: "halo"}
	m := newModel(context.Background(), Options{Backend: d, State: demoState(), Server: "localhost:8190", Procs: d})
	m.connected, m.online = true, 6
	feed := d.Updates()
	go func() {
		for {
			select {
			case u := <-feed:
				mu.Lock()
				m.st.Feed(u)
				mu.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()
	go d.Stream(ctx, func(e lg.Entry) { mu.Lock(); m.st.Apply(e, time.Now()); mu.Unlock() }, func(bool, error) {})
	return m, d
}

// sync copies the demo's commits and diffs into the model, as polling would.
func syncDemo(m *model, d *Demo) {
	cs, _ := d.Commits(context.Background(), "", time.Time{})
	m.st.SetCommits(cs)
	for _, c := range cs {
		m.ui.Diffs[c.SHA], _ = d.Diff(context.Background(), c.SHA)
	}
	m.st.Shells, _ = d.Shells(context.Background())
	for _, sh := range m.st.Shells {
		m.ui.Out[sh.PID], _ = d.Output(context.Background(), sh.Out)
	}
}

func checkFrame(t *testing.T, out string, w, h int, what string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) != h {
		t.Fatalf("%s %dx%d: %d lines", what, w, h, len(lines))
	}
	for i, l := range lines {
		if got := ansiWidth(l); got != w {
			t.Fatalf("%s %dx%d: line %d is %d wide: %q", what, w, h, i, got, stripANSI(l))
		}
	}
}

// TestDemoRenders plays part of the demo run and renders every panel at every
// size, with and without its item open: nothing may panic, and every line must be
// exactly as wide as the screen.
func TestDemoRenders(t *testing.T) {
	var mu sync.Mutex
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	m, d := playDemo(t, ctx, &mu)
	frames := 0
	for ctx.Err() == nil {
		time.Sleep(250 * time.Millisecond)
		mu.Lock()
		now := time.Now()
		m.st.Prune(now)
		syncDemo(m, d)
		m.frame = frames
		for _, size := range [][2]int{{200, 56}, {160, 44}, {100, 30}} {
			m.w, m.h = size[0], size[1]
			for id := 1; id <= nPanes; id++ {
				m.ui.Focus = id
				for z := sizeNormal; z <= sizeFull; z++ {
					m.ui.Size[id] = z
					m.ui.Cur[id].detail = frames%2 == 1
					m.ui.Help = id == pComms && z == sizeHalf
					checkFrame(t, m.render(now), size[0], size[1], fmt.Sprintf("panel %d %s detail=%v", id, z, m.ui.Cur[id].detail))
				}
				m.ui.Size[id], m.ui.Cur[id].detail, m.ui.Help = sizeNormal, false, false
			}
		}
		frames++
		mu.Unlock()
	}
	mu.Lock()
	defer mu.Unlock()
	if m.st.Plan == nil || len(m.st.Comms) < 3 {
		t.Fatalf("the demo didn't get far: plan=%v comms=%d", m.st.Plan != nil, len(m.st.Comms))
	}
}

func press(m *model, keys ...string) {
	for _, k := range keys {
		m.normalKey(k)
	}
}

func TestPanelKeys(t *testing.T) {
	m := newModel(context.Background(), Options{Backend: &Demo{}, State: demoState()})
	m.w, m.h = 200, 56
	press(m, "2", "+")
	if m.ui.Focus != pOrbit || m.ui.Size[pOrbit] != sizeHalf {
		t.Fatalf("2 +: focus %d size %s", m.ui.Focus, m.ui.Size[pOrbit])
	}
	press(m, "+", "+")
	if m.ui.Size[pOrbit] != sizeFull {
		t.Fatalf("+ stops at full, got %s", m.ui.Size[pOrbit])
	}
	press(m, "_")
	if m.ui.Size[pOrbit] != sizeHalf {
		t.Fatalf("_ shrinks, got %s", m.ui.Size[pOrbit])
	}
	press(m, "esc")
	if m.ui.Size[pOrbit] != sizeNormal {
		t.Fatalf("esc goes back to normal, got %s", m.ui.Size[pOrbit])
	}
	press(m, "tab")
	if m.ui.Focus != pAgent {
		t.Fatalf("tab: focus %d", m.ui.Focus)
	}
	press(m, "shift+tab", "shift+tab", "shift+tab")
	if m.ui.Focus != nPanes {
		t.Fatalf("shift+tab wraps: focus %d", m.ui.Focus)
	}

	// Comms: select, open, scroll, back.
	for i := 0; i < 5; i++ {
		m.st.comm(time.Now(), "task", "halo-lead", "halo-sonnet-1", strings.Repeat("word ", 400), "S1")
	}
	press(m, "4", "k", "k")
	c := m.ui.Cur[pComms]
	if c.follow || c.sel != 2 {
		t.Fatalf("k k from the newest: sel %d follow %v", c.sel, c.follow)
	}
	press(m, "enter", "j", "j")
	if !c.detail || c.dOff != 2 || c.sel != 2 {
		t.Fatalf("enter j j: detail %v dOff %d sel %d", c.detail, c.dOff, c.sel)
	}
	press(m, "esc")
	if c.detail {
		t.Fatal("esc closes the message")
	}
	press(m, "G")
	if !c.follow || c.sel != 4 {
		t.Fatalf("G follows the newest: sel %d follow %v", c.sel, c.follow)
	}
	press(m, "f")
	if m.ui.Filter != "halo-lead" {
		t.Fatalf("f filters by the first agent, got %q", m.ui.Filter)
	}
	press(m, "F")
	if m.ui.Filter != "" {
		t.Fatal("F clears the filter")
	}

	press(m, "?")
	if !m.ui.Help {
		t.Fatal("? opens the help")
	}
	for _, r := range "diff" {
		m.key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if m.ui.Query != "diff" {
		t.Fatalf("typing searches, query %q", m.ui.Query)
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.ui.Help || m.ui.Query != "" {
		t.Fatal("esc closes the help")
	}
}

func TestHelpSearch(t *testing.T) {
	all := len(helpLines("", 90))
	got := helpLines("diff", 90)
	if len(got) >= all || len(got) < 2 {
		t.Fatalf("diff: %d lines of %d", len(got), all)
	}
	for _, l := range got {
		if p := stripANSI(l); p != "" && !strings.Contains(strings.ToLower(p), "commits") && !matches(p, "diff") {
			t.Errorf("%q doesn't match diff", p)
		}
	}
	if got := helpLines("zzzz", 90); len(got) != 1 {
		t.Fatalf("no match: %v", got)
	}
}

// TestSnapshot writes an HTML screenshot of the demo after HALO_SNAP_SECONDS (default
// 20) to $HALO_SNAP, after pressing HALO_SNAP_KEYS (space separated, e.g. "5 enter").
// Skipped unless HALO_SNAP is set.
func TestSnapshot(t *testing.T) {
	path := os.Getenv("HALO_SNAP")
	if path == "" {
		t.Skip("set HALO_SNAP=out.html")
	}
	secs, _ := strconv.Atoi(os.Getenv("HALO_SNAP_SECONDS"))
	if secs == 0 {
		secs = 20
	}
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, d := playDemo(t, ctx, &mu)
	end := time.Now().Add(time.Duration(secs) * time.Second)
	// HALO_SNAP_UNTIL=port waits (up to the deadline) for a shell with a port.
	until := os.Getenv("HALO_SNAP_UNTIL")
	for time.Now().Before(end) {
		if ss, _ := d.Shells(ctx); until == "port" && len(ss) > 0 && len(ss[len(ss)-1].Ports) > 0 && time.Since(ss[len(ss)-1].Started) > 6*time.Second {
			break
		}
		time.Sleep(80 * time.Millisecond)
		mu.Lock()
		m.st.Prune(time.Now())
		m.frame++
		mu.Unlock()
	}
	mu.Lock()
	defer mu.Unlock()
	m.w, m.h = 210, 58
	if v, _ := strconv.Atoi(os.Getenv("HALO_SNAP_W")); v > 0 {
		m.w = v
	}
	if v, _ := strconv.Atoi(os.Getenv("HALO_SNAP_H")); v > 0 {
		m.h = v
	}
	syncDemo(m, d)
	if ws, _ := d.Waiting(ctx); len(ws) > 0 {
		m.waits = ws
	}
	for _, k := range strings.Fields(os.Getenv("HALO_SNAP_KEYS")) {
		if k == "?" {
			m.ui.Help = true
			continue
		}
		if name, ok := strings.CutPrefix(k, "@"); ok {
			m.st.Focus, m.st.manualFocus = "halo-"+name, time.Now().Add(time.Hour)
			continue
		}
		if q, ok := strings.CutPrefix(k, "?"); ok {
			m.ui.Help, m.ui.Query = true, q
			continue
		}
		m.normalKey(k)
	}
	if err := os.WriteFile(path, []byte(toHTML(m.render(time.Now()))), 0o644); err != nil {
		t.Fatal(err)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

var sgr = regexp.MustCompile(`\x1b\[([0-9;:]*)m`)

func stripANSI(s string) string { return sgr.ReplaceAllString(s, "") }

func ansiWidth(s string) int { return len([]rune(stripANSI(s))) }

// toHTML turns SGR-colored text into a page that looks like the terminal.
func toHTML(s string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><meta charset="utf-8"><title>halo</title>
<link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;700&display=swap" rel="stylesheet">
<style>body{margin:0;background:#07090a;color:#cfdcd5}pre{margin:0;padding:14px 16px;font:13px/1.32 "JetBrains Mono",monospace;font-variant-ligatures:none}</style><pre>`)
	fg, bg, bold := "", "", false
	open := false
	idx := sgr.FindAllStringSubmatchIndex(s, -1)
	last := 0
	emit := func(text string) {
		if text == "" {
			return
		}
		style := ""
		if fg != "" {
			style += "color:" + fg + ";"
		}
		if bg != "" {
			style += "background:" + bg + ";"
		}
		if bold {
			style += "font-weight:700;"
		}
		if style != "" {
			b.WriteString(`<span style="` + style + `">` + html.EscapeString(text) + `</span>`)
		} else {
			b.WriteString(html.EscapeString(text))
		}
	}
	_ = open
	for _, m := range idx {
		emit(s[last:m[0]])
		last = m[1]
		params := strings.FieldsFunc(s[m[2]:m[3]], func(r rune) bool { return r == ';' || r == ':' })
		if len(params) == 0 {
			fg, bg, bold = "", "", false
		}
		for i := 0; i < len(params); i++ {
			switch p := params[i]; {
			case p == "0":
				fg, bg, bold = "", "", false
			case p == "1":
				bold = true
			case p == "22":
				bold = false
			case p == "39":
				fg = ""
			case p == "49":
				bg = ""
			case (p == "38" || p == "48") && i+4 < len(params) && params[i+1] == "2":
				col := fmt.Sprintf("rgb(%s,%s,%s)", params[i+2], params[i+3], params[i+4])
				if p == "38" {
					fg = col
				} else {
					bg = col
				}
				i += 4
			}
		}
	}
	emit(s[last:])
	b.WriteString("</pre>")
	return b.String()
}

func TestQuitAsks(t *testing.T) {
	m := newModel(context.Background(), Options{Backend: &Demo{}, State: demoState()})
	m.w, m.h = 200, 56
	for _, k := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: 'c', Mod: tea.ModCtrl}} {
		if _, cmd := m.key(k); cmd != nil || !m.quitAsk {
			t.Fatalf("%s should open the dialog, not quit", k)
		}
		if out := stripANSI(m.render(time.Now())); !strings.Contains(out, "Quit Halo?") {
			t.Fatalf("no dialog:\n%s", out)
		}
		m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
		if m.quitAsk {
			t.Fatal("esc keeps Halo open")
		}
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.quitAsk {
		t.Fatal("esc alone never opens the dialog")
	}
	m.key(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if _, cmd := m.key(tea.KeyPressMsg{Code: 'y', Text: "y"}); cmd == nil {
		t.Fatal("y quits")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("y should return tea.Quit")
	}
}
