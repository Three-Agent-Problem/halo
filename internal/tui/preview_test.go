package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/three-agent-problem/halo/internal/plan"
)

func TestWithin(t *testing.T) {
	dir := "/cache/halo/abc/_run"
	for path, want := range map[string]bool{
		"/cache/halo/abc/_run":         true,
		"/cache/halo/abc/_run/web/app": true,
		"/cache/halo/abc/_run/":        true,
		"/cache/halo/abc/_run2":        false,
		"/cache/halo/abc/S1":           false,
		"/cache/halo/abc":              false,
		"/home/me/repo":                false,
		"":                             false,
	} {
		if got := within(dir, path); got != want {
			t.Errorf("within(%q) = %v, want %v", path, got, want)
		}
	}
	if within("", "/x") {
		t.Error("empty dir contains nothing")
	}
}

func TestPreviewShell(t *testing.T) {
	dir := "/cache/halo/abc/_run"
	ss := []Shell{
		{PID: 1, Cwd: dir, Command: "ls"}, // no port
		{PID: 2, Cwd: "/cache/halo/abc/S1", Ports: []int{3000}},
		{PID: 3, Cwd: dir + "/web", Ports: []int{5173}},
	}
	sh, ok := previewShell(ss, dir)
	if !ok || sh.PID != 3 {
		t.Fatalf("got %+v, %v; want pid 3", sh, ok)
	}
	if _, ok := previewShell(ss[:2], dir); ok {
		t.Error("a shell outside the run worktree matched")
	}
}

func previewModel(t *testing.T, hosted bool, ss []Shell) *model {
	t.Helper()
	o := Options{Backend: &Demo{Human: "ada", Prefix: "halo"}, State: demoState(),
		Preview: func(short string) (string, string, bool) {
			if short != "abc123" {
				t.Errorf("short = %q", short)
			}
			return "/cache/halo/abc123/_run", "npm run dev", true
		}}
	if hosted {
		o.Procs = &Demo{}
	}
	m := newModel(context.Background(), o)
	m.w, m.h = 120, 40
	m.st.Plan = &plan.Plan{Goal: "build a site", Branch: "halo/abc123", Approved: true,
		Subtasks: []*plan.Subtask{{ID: "S1", Title: "page"}}}
	m.st.Shells = ss
	m.ui.Focus, m.ui.Size[pPlan] = pPlan, sizeFull
	return m
}

func TestPlanPanePreviewRow(t *testing.T) {
	run := "/cache/halo/abc123/_run"
	m := previewModel(t, true, []Shell{{PID: 9, Kind: "server", Cwd: run, Ports: []int{5173}}})
	if out := stripANSI(m.render(time.Now())); !strings.Contains(out, "preview  http://localhost:5173") {
		t.Errorf("no URL row:\n%s", out)
	}
	m = previewModel(t, true, nil)
	if out := stripANSI(m.render(time.Now())); !strings.Contains(out, "preview  not running — p to start") {
		t.Errorf("no not-running row:\n%s", out)
	}
	m = previewModel(t, false, nil)
	out := stripANSI(m.render(time.Now()))
	if !strings.Contains(out, "preview  not running") || strings.Contains(out, "p to start") {
		t.Errorf("watch-only row wrong:\n%s", out)
	}
	m = previewModel(t, true, nil)
	m.o.Preview, m.previewBranch = func(string) (string, string, bool) { return "", "", false }, ""
	if out := stripANSI(m.render(time.Now())); strings.Contains(out, "preview  not") || strings.Contains(out, "preview  http") {
		t.Errorf("non-web project shows a row:\n%s", out)
	}
}

func TestPreviewKeys(t *testing.T) {
	run := "/cache/halo/abc123/_run"
	var opened, started []string
	oldOpen, oldStart := openURL, startPreview
	defer func() { openURL, startPreview = oldOpen, oldStart }()
	openURL = func(u string) error { opened = append(opened, u); return nil }
	startPreview = func(pv Preview) error { started = append(started, pv.Dir+" "+pv.Cmd); return nil }

	m := previewModel(t, true, nil)
	m.render(time.Now())
	m.ui.Focus = pPlan
	m.normalKey("o")
	m.normalKey("p")
	if len(opened) != 0 || len(started) != 1 || started[0] != run+" npm run dev" {
		t.Errorf("opened %v, started %v", opened, started)
	}
	m.st.Shells = []Shell{{PID: 9, Cwd: run, Ports: []int{5173}}}
	m.normalKey("o")
	m.normalKey("p")
	if len(opened) != 1 || opened[0] != "http://localhost:5173" || len(started) != 1 {
		t.Errorf("opened %v, started %v", opened, started)
	}

	w := previewModel(t, false, nil) // watch-only: p isn't ours
	w.render(time.Now())
	w.ui.Focus = pPlan
	w.normalKey("p")
	if len(started) != 1 {
		t.Error("a watch-only Halo started the preview")
	}
}

func TestPreviewAppearsLater(t *testing.T) {
	web := false
	m := previewModel(t, true, nil)
	m.o.Preview = func(string) (string, string, bool) {
		if !web {
			return "", "", false
		}
		return "/cache/halo/abc123/_run", "npm run dev", true
	}
	now := time.Now()
	if out := stripANSI(m.render(now)); strings.Contains(out, "preview  not") {
		t.Fatalf("row before the project is web:\n%s", out)
	}
	web = true
	if out := stripANSI(m.render(now.Add(time.Second))); strings.Contains(out, "preview  not") {
		t.Error("looked again before the throttle elapsed")
	}
	if out := stripANSI(m.render(now.Add(previewRecheck))); !strings.Contains(out, "preview  not running — p to start") {
		t.Errorf("row didn't appear once the project became web:\n%s", out)
	}
}
