package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/three-agent-problem/halo/internal/shells"
)

// previewRecheck is how often a run not yet known to be a web project is looked at again.
const previewRecheck = 3 * time.Second

// Preview is the live preview of a web run: where the run branch's code is and
// the command that serves it. The zero value means the project isn't a web one.
type Preview struct {
	Dir string // the run branch's worktree
	Cmd string // the command that serves it
}

// Web reports whether there is a preview to show.
func (p Preview) Web() bool { return p.Dir != "" && p.Cmd != "" }

// previewFor resolves the preview of the run whose branch is branch ("halo/<short>").
func (m *model) previewFor(branch string) Preview {
	if m.o.Preview == nil || branch == "" {
		return Preview{}
	}
	dir, cmd, ok := m.o.Preview(strings.TrimPrefix(branch, "halo/"))
	if !ok {
		return Preview{}
	}
	return Preview{Dir: dir, Cmd: cmd}
}

// within reports whether path is dir or inside it.
func within(dir, path string) bool {
	if dir == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// previewShell finds the server shell that runs in the run's worktree: the
// newest one with a port and a working directory inside dir.
func previewShell(ss []Shell, dir string) (Shell, bool) {
	var found Shell
	ok := false
	for _, sh := range ss {
		if len(sh.Ports) > 0 && within(dir, sh.Cwd) {
			found, ok = sh, true // shells are sorted oldest first
		}
	}
	return found, ok
}

func portURL(port int) string { return fmt.Sprintf("http://localhost:%d", port) }

// previewKey handles p (start) and o (open) in the plan panel.
func (m *model) previewKey(key string) (tea.Cmd, bool) {
	pv := m.st.Preview
	if !pv.Web() {
		return nil, false
	}
	sh, running := previewShell(m.st.Shells, pv.Dir)
	switch key {
	case "o":
		if !running {
			m.say("the preview isn't running")
			return nil, true
		}
		m.openPort(sh)
		return nil, true
	case "p":
		if m.o.Procs == nil {
			return nil, false
		}
		if running {
			m.say("the preview is already running at " + portURL(sh.Ports[0]))
			return nil, true
		}
		if err := startPreview(pv); err != nil {
			m.say("can't start the preview: " + err.Error())
		} else {
			m.say("starting " + pv.Cmd + " in the run worktree; it shows up in the shells panel")
		}
		return m.fetchShells(), true
	}
	return nil, false
}

// startPreview runs the preview command in the run's worktree under shells.Wrap
// so that the shells panel lists it. Its output goes to a file the panel can show.
var startPreview = func(pv Preview) error {
	out, err := os.CreateTemp("", "halo-preview-*.log")
	if err != nil {
		return err
	}
	argv := shells.Wrap("preview", []string{"/bin/sh", "-c", pv.Cmd})
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = pv.Dir, out, out
	if err := cmd.Start(); err != nil {
		_ = out.Close()
		return err
	}
	_ = out.Close()
	go func() { _ = cmd.Wait() }()
	return nil
}
