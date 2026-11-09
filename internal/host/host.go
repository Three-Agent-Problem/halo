// Package host runs the roster on this machine: the lead, the workers and the
// reviewer, each a Claude Code agent behind its own Lagrange tunnel. Every message
// between them goes through the Lagrange server as an A2A task.
package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/three-agent-problem/lagrange/client"
	"github.com/three-agent-problem/lagrange/delegate"
	"github.com/three-agent-problem/lagrange/tunnel"

	"github.com/three-agent-problem/halo/internal/config"
	"github.com/three-agent-problem/halo/internal/lg"
)

// Provision creates the roster's addresses that don't exist yet and saves a token
// for each.
func Provision(ctx context.Context, cfg *config.Config, st *config.State) error {
	api := client.New(st.Server, st.Key)
	addrs, err := api.Addresses(ctx)
	if err != nil {
		return fmt.Errorf("listing addresses: %w", err)
	}
	exists := map[string]bool{}
	for _, a := range addrs {
		exists[a.Handle] = true
	}
	for _, a := range cfg.Agents {
		h := cfg.Handle(a.Name)
		if exists[h] && st.Token(h) != "" {
			continue
		}
		var tok string
		if exists[h] {
			tok, err = api.MintToken(ctx, h, "halo")
		} else {
			tok, err = api.CreateAgent(ctx, h)
		}
		if err != nil {
			return fmt.Errorf("@%s: %w", h, err)
		}
		st.SetToken(h, tok)
	}
	return nil
}

// Host is the running roster.
type Host struct {
	cfg    *config.Config
	st     *config.State
	api    *client.Client
	log    *slog.Logger
	wt     *worktrees
	mcpURL string
	lead   *lead
	ctx    context.Context

	mu      sync.Mutex
	workers map[string]*worker // by handle, the reviewer included
	busy    map[string]int     // tasks the lead has in flight per worker
}

// Run runs the roster until ctx ends.
func Run(ctx context.Context, cfg *config.Config, st *config.State) error {
	wt, err := newWorktrees(cfg.Repo)
	if err != nil {
		return err
	}
	api := client.New(st.Server, st.Key)
	if st.Human == "" {
		me, err := api.Me(ctx)
		if err != nil {
			return err
		}
		st.Human = me.User.Handle
	}
	if err := Provision(ctx, cfg, st); err != nil {
		return err
	}
	if err := st.Save(); err != nil {
		return err
	}
	logDir := filepath.Join(wt.root, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(logDir, "halo.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	h := &Host{cfg: cfg, st: st, api: api, wt: wt, workers: map[string]*worker{}, busy: map[string]int{},
		log: slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}))}

	// The MCP server the agents' Claude sessions call.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	h.mcpURL = "http://" + ln.Addr().String()
	mux := http.NewServeMux()
	mux.Handle("/lead", h.leadMCP())
	mux.Handle("/worker", h.workerMCP())
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	h.ctx = ctx
	var wg sync.WaitGroup
	errs := make(chan error, len(cfg.Agents))
	start := func(a config.Agent, handler http.Handler) {
		handle := cfg.Handle(a.Name)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := tunnel.Run(ctx, tunnel.ClientConfig{Server: st.Server, Token: st.Token(handle), Capacity: a.Capacity,
				Handler: handler, UserAgent: "halo", Logger: h.log,
				OnConnect: func() { fmt.Printf("  ●  @%-16s %-7s %s\n", handle, a.Model, a.Role) }})
			if err != nil && !errors.Is(err, context.Canceled) {
				errs <- fmt.Errorf("@%s: %w", handle, err)
				cancel()
			}
		}()
	}
	fmt.Printf("halo agents · %s · repo %s\n  worktrees and logs: %s\n", st.Server, cfg.Repo, wt.root)
	for _, a := range cfg.Agents {
		if a.Role == config.RoleLead {
			continue
		}
		w, err := h.newWorker(ctx, a, logFile)
		if err != nil {
			return err
		}
		defer w.exec.Close()
		h.workers[w.handle] = w
		start(a, serve(card(w.handle, a), delegate.Executor(w)))
	}
	l, err := h.newLead(ctx, cfg.Lead(), logFile)
	if err != nil {
		return err
	}
	defer l.exec.Close()
	h.lead = l
	start(cfg.Lead(), serve(card(l.handle, cfg.Lead()), delegate.Executor(l)))

	<-ctx.Done()
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
		return nil
	}
}

func (h *Host) handle(a config.Agent) string { return h.cfg.Handle(a.Name) }

// caller calls other addresses as handle.
func (h *Host) caller(handle string) lg.Caller {
	return lg.Caller{Server: h.st.Server, Token: h.st.Token(handle)}
}

func card(handle string, a config.Agent) *a2a.AgentCard {
	desc := map[string]string{
		config.RoleLead:     "Plans a change, splits it into subtasks for a team of Claude Code workers, and coordinates them through review and merge.",
		config.RoleWorker:   "A Claude Code worker on " + a.Model + ". Builds one subtask in its own git worktree.",
		config.RoleReviewer: "Reviews a subtask's diff and answers APPROVE or CHANGES.",
	}[a.Role]
	return &a2a.AgentCard{
		Name:                "halo " + a.Name,
		Description:         desc,
		Version:             "0.1.0",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://agent/rpc", a2a.TransportProtocolJSONRPC)},
		Capabilities:        a2a.AgentCapabilities{Streaming: true},
		DefaultInputModes:   []string{"text/plain"},
		DefaultOutputModes:  []string{"text/plain"},
		Skills: []a2a.AgentSkill{{ID: a.Role, Name: strings.ToUpper(a.Role[:1]) + a.Role[1:], Description: desc,
			Tags: []string{"halo", a.Role, a.Model}}},
	}
}

func serve(c *a2a.AgentCard, exec a2asrv.AgentExecutor) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(c))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(exec)))
	return mux
}

// meta is what Halo puts in message metadata under "halo".
type meta struct {
	Kind     string // "question" for ask_lead
	Run      string // the run's root platform task
	Subtask  string
	Title    string
	Worktree string
	Branch   string
	Session  string // the Claude session to continue
	From     string
}

func (m meta) asMap() map[string]any {
	out := map[string]any{}
	for k, v := range map[string]string{"kind": m.Kind, "run": m.Run, "subtask": m.Subtask, "title": m.Title,
		"worktree": m.Worktree, "branch": m.Branch, "session": m.Session, "from": m.From} {
		if v != "" {
			out[k] = v
		}
	}
	return map[string]any{"halo": out}
}

func metaOf(msg *a2a.Message) meta {
	var m meta
	if msg == nil {
		return m
	}
	raw, _ := msg.Metadata["halo"].(map[string]any)
	get := func(k string) string { s, _ := raw[k].(string); return s }
	m.Kind, m.Run, m.Subtask, m.Title = get("kind"), get("run"), get("subtask"), get("title")
	m.Worktree, m.Branch, m.Session, m.From = get("worktree"), get("branch"), get("session"), get("from")
	return m
}

func textOf(msg *a2a.Message) string { return lg.MessageText(msg) }
