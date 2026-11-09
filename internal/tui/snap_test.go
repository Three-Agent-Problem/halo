package tui

import (
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

// TestDemoRenders plays part of the demo run and renders frames at a few sizes:
// nothing may panic, and every line must be exactly as wide as the screen.
func TestDemoRenders(t *testing.T) {
	st := demoState()
	var mu sync.Mutex
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	d := &Demo{Human: "kaan", Prefix: "halo"}
	go d.Stream(ctx, func(e lg.Entry) { mu.Lock(); st.Apply(e, time.Now()); mu.Unlock() }, func(bool, error) {})
	frames := 0
	for ctx.Err() == nil {
		time.Sleep(300 * time.Millisecond)
		mu.Lock()
		now := time.Now()
		st.Prune(now)
		for _, size := range [][2]int{{200, 56}, {160, 44}, {100, 30}} {
			out := st.Render(Screen{W: size[0], H: size[1], Frame: frames, Now: now, Server: "demo", Connected: true, Online: 6, Footer: "› prompt"})
			lines := strings.Split(out, "\n")
			if len(lines) != size[1] {
				t.Fatalf("%dx%d: %d lines", size[0], size[1], len(lines))
			}
			for i, l := range lines {
				if w := ansiWidth(l); w != size[0] {
					t.Fatalf("%dx%d: line %d is %d wide: %q", size[0], size[1], i, w, stripANSI(l))
				}
			}
		}
		frames++
		mu.Unlock()
	}
	if st.Plan == nil || len(st.Comms) < 3 {
		t.Fatalf("the demo didn't get far: plan=%v comms=%d", st.Plan != nil, len(st.Comms))
	}
}

// TestSnapshot writes an HTML screenshot of the demo after HALO_SNAP_SECONDS (default
// 20) to $HALO_SNAP. Skipped unless HALO_SNAP is set.
func TestSnapshot(t *testing.T) {
	path := os.Getenv("HALO_SNAP")
	if path == "" {
		t.Skip("set HALO_SNAP=out.html")
	}
	secs, _ := strconv.Atoi(os.Getenv("HALO_SNAP_SECONDS"))
	if secs == 0 {
		secs = 20
	}
	st := demoState()
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &Demo{Human: "kaan", Prefix: "halo"}
	go d.Stream(ctx, func(e lg.Entry) { mu.Lock(); st.Apply(e, time.Now()); mu.Unlock() }, func(bool, error) {})
	end := time.Now().Add(time.Duration(secs) * time.Second)
	frame := 0
	for time.Now().Before(end) {
		time.Sleep(80 * time.Millisecond)
		mu.Lock()
		st.Prune(time.Now())
		mu.Unlock()
		frame++
	}
	mu.Lock()
	defer mu.Unlock()
	w, h := 210, 58
	if v, _ := strconv.Atoi(os.Getenv("HALO_SNAP_W")); v > 0 {
		w = v
	}
	if v, _ := strconv.Atoi(os.Getenv("HALO_SNAP_H")); v > 0 {
		h = v
	}
	out := st.Render(Screen{W: w, H: h, Frame: frame, Now: time.Now(), Server: "localhost:8190", Connected: true, Online: 6,
		Waiting: boolInt(d.waiting != nil), Footer: " " + c("hub!", "› ") + c("mute", st.Prompt) + "  " + c("mute!", "⏎") + c("dim", " prompt  ") + c("mute!", "a") + c("dim", " answer  ") + c("mute!", "tab") + c("dim", " focus  ") + c("mute!", "q") + c("dim", " quit")})
	if err := os.WriteFile(path, []byte(toHTML(out)), 0o644); err != nil {
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
