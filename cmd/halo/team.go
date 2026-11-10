package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/three-agent-problem/lagrange/client"

	"github.com/three-agent-problem/halo/internal/config"
	"github.com/three-agent-problem/halo/internal/host"
	"github.com/three-agent-problem/halo/internal/shells"
	"github.com/three-agent-problem/halo/internal/tui"
)

// team hosts the roster inside the TUI's process: the same agents halo agents
// runs, restarted when the roster changes.
type team struct {
	parent context.Context
	st     *config.State
	path   string // where the roster is saved
	notes  chan string
	feed   chan tui.Update // the agents' raw output

	mu     sync.Mutex
	cfg    *config.Config
	cancel context.CancelFunc
	done   chan struct{}
}

func newTeam(ctx context.Context, cfg *config.Config, st *config.State, path string) *team {
	return &team{parent: ctx, cfg: cfg, st: st, path: path, notes: make(chan string, 16), feed: make(chan tui.Update, 8192)}
}

func (t *team) note(s string) {
	select {
	case t.notes <- s:
	default:
	}
}

// start runs the roster until stop. Call it with t.mu held.
func (t *team) start() {
	ctx, cancel := context.WithCancel(t.parent)
	done := make(chan struct{})
	t.cancel, t.done = cancel, done
	cfg := t.cfg.Clone()
	go func() {
		defer close(done)
		err := host.Run(ctx, cfg, t.st, func(e host.Event) {
			switch e.Kind {
			case "error":
				t.note(e.Text)
			case "acp":
				select {
				case t.feed <- tui.Update{Agent: e.Agent, Task: e.Task, Context: e.Context, Type: e.Text, Raw: e.Data, At: time.Now()}:
				default: // the TUI is behind; drop rather than stall the agent
				}
			}
		})
		if err != nil && ctx.Err() == nil {
			t.note("the agents stopped: " + err.Error())
		}
	}()
}

// stop stops the roster and waits for it, at most 15 s. Call it with t.mu held.
func (t *team) stop() {
	if t.cancel == nil {
		return
	}
	t.cancel()
	select {
	case <-t.done:
	case <-time.After(15 * time.Second):
	}
	t.cancel = nil
}

// Members implements tui.Team.
func (t *team) Members() []tui.Member {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []tui.Member
	for _, a := range t.cfg.Agents {
		out = append(out, tui.Member{Handle: t.cfg.Handle(a.Name), Name: a.Name, Model: a.Model, Role: a.Role})
	}
	return out
}

// SetMembers implements tui.Team: it saves the roster and restarts the agents.
func (t *team) SetMembers(_ context.Context, ms []tui.Member) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := t.cfg.Clone()
	n.Agents = nil
	for _, m := range ms {
		capacity := 1
		if m.Role != config.RoleWorker {
			capacity = 4
		}
		a := config.Agent{Name: m.Name, Model: m.Model, Role: m.Role, Capacity: capacity}
		if old, ok := t.cfg.Agent(t.cfg.Handle(m.Name)); ok {
			a.ACP = old.ACP // the TUI doesn't edit it: keep halo.yaml's
		}
		n.Agents = append(n.Agents, a)
	}
	if err := n.SaveRoster(t.path); err != nil {
		return err
	}
	t.stop()
	t.cfg = n
	t.start()
	return nil
}

// gitTop is the repository the current directory is in, or "".
func gitTop() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// setup asks for the server and an API key on the terminal, the first time.
func setup(ctx context.Context, st *config.State) error {
	in := bufio.NewReader(os.Stdin)
	ask := func(q, def string) (string, error) {
		if def != "" {
			fmt.Printf("%s [%s]: ", q, def)
		} else {
			fmt.Printf("%s: ", q)
		}
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			line = def
		}
		if line == "" && err != nil {
			return "", err
		}
		return line, nil
	}
	if server, key := config.LagrangeLogin(); server != "" && (st.Server == "" || st.Server == server) {
		if me, err := client.New(server, key).Me(ctx); err == nil {
			st.Server, st.Key, st.Human = server, key, me.User.Handle
			fmt.Printf("halo: using your lagrange login: @%s on %s\n", st.Human, st.Server)
			return st.Save()
		}
	}
	fmt.Println("halo: first run. It needs a Lagrange server and an API key (lagrange login, or Settings → API keys).")
	var err error
	if st.Server, err = ask("server URL", nonEmpty(st.Server, "http://localhost:8080")); err != nil {
		return err
	}
	st.Server = strings.TrimRight(st.Server, "/")
	if st.Key, err = ask("API key", ""); err != nil || st.Key == "" {
		return errors.New("no API key")
	}
	me, err := client.New(st.Server, st.Key).Me(ctx)
	if err != nil {
		return fmt.Errorf("checking the key on %s: %w", st.Server, err)
	}
	st.Human = me.User.Handle
	return st.Save()
}

func nonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// Land implements tui.Team: it merges halo/<short> into the checked-out branch,
// then removes the run's branches and worktrees.
func (t *team) Land(ctx context.Context, short string) (string, error) {
	t.mu.Lock()
	repo := t.cfg.Repo
	t.mu.Unlock()
	base, err := exec.CommandContext(ctx, "git", "-C", repo, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return "", errors.New("the repository has a detached HEAD; check out a branch first")
	}
	summary, err := host.Land(ctx, repo, "halo/"+short, strings.TrimSpace(string(base)))
	if err != nil {
		return "", err
	}
	if err := host.Cleanup(ctx, repo, short); err != nil {
		return summary + " (cleanup: " + err.Error() + ")", nil
	}
	return summary, nil
}

// Shells implements tui.Procs: the shells of the agents this process hosts.
func (t *team) Shells(ctx context.Context) ([]tui.Shell, error) { return shells.Scan(ctx, os.Getpid()) }

// Output implements tui.Procs.
func (t *team) Output(_ context.Context, path string) (string, error) {
	return shells.Tail(path, 256<<10)
}

// Stop implements tui.Procs.
func (t *team) Stop(ctx context.Context, pid int) error { return shells.Stop(ctx, pid) }
