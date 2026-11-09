// Command halo turns one prompt into a plan that a team of Claude Code agents builds,
// with every message going through a Lagrange server, and shows it all live.
//
//	halo init --server URL --key KEY   save the server and an API key, create the addresses
//	halo agents                        run the roster (lead, workers, reviewer) on this machine
//	halo merge [run] [--keep]          list finished runs, or merge one into the current branch
//	halo                               the TUI
//	halo --demo                        the TUI playing a scripted run, no server needed
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/three-agent-problem/lagrange/client"

	"github.com/three-agent-problem/halo/internal/config"
	"github.com/three-agent-problem/halo/internal/host"
	"github.com/three-agent-problem/halo/internal/tui"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "halo:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "init":
			return initCmd(ctx, args[1:])
		case "agents":
			return agentsCmd(ctx, args[1:])
		case "merge":
			return mergeCmd(ctx, args[1:])
		case "help", "-h", "--help":
			fmt.Println(`usage:
  halo init --server URL --key lgk_...   save the Lagrange server and an API key, create the addresses
  halo agents [-config halo.yaml]         run the team headless (halo hosts it itself; this is for another machine)
  halo merge                              list finished runs (halo/<run> branches)
  halo merge <run> [--keep]               merge a run into the checked-out branch, then clean it up
  halo [-watch] [-since N]                the TUI; it hosts the team on this repository unless another halo does
  halo --demo                             the TUI playing a scripted run, no server needed`)
			return nil
		}
	}
	return tuiCmd(ctx, args)
}

// load reads the roster (a local halo.yaml if there is one, else the global
// roster) and the saved state. It returns where the roster lives.
func load(path string) (*config.Config, *config.State, string, error) {
	cfg, from, err := config.LoadRoster(path)
	if err != nil {
		return nil, nil, "", err
	}
	if from != path {
		// The global roster works on the repository Halo runs in.
		cfg.Repo = gitTop()
	}
	st, err := config.LoadState()
	if err != nil {
		return nil, nil, "", err
	}
	if cfg.Server != "" && os.Getenv("LAGRANGE_SERVER") == "" {
		st.Server = cfg.Server
	}
	return cfg, st, from, nil
}

func members(cfg *config.Config, human string) []tui.Member {
	out := []tui.Member{{Handle: human, Name: human, Model: "human", Role: "human"}}
	for _, a := range cfg.Agents {
		out = append(out, tui.Member{Handle: cfg.Handle(a.Name), Name: a.Name, Model: a.Model, Role: a.Role})
	}
	return out
}

func tuiCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("halo", flag.ContinueOnError)
	cfgPath := fs.String("config", "halo.yaml", "A roster for this repository only (default: the global roster).")
	demo := fs.Bool("demo", false, "Play a scripted run without a server.")
	since := fs.Int64("since", -1, "Activity sequence to start from: -1 starts now (/resume reopens a past run), 0 replays everything.")
	watch := fs.Bool("watch", false, "Only watch: don't host the agents here.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *demo {
		cfg := config.Default()
		var handles []string
		for _, a := range cfg.Agents {
			handles = append(handles, cfg.Handle(a.Name))
		}
		d := &tui.Demo{Human: "kaan", Prefix: cfg.Prefix}
		return tui.Run(ctx, tui.Options{
			Backend: d, Feed: d.Updates(), Procs: d,
			State:  tui.NewState(cfg.Prefix, members(cfg, "kaan")),
			Server: "demo (no server)", Handles: handles, Watching: "the demo has no agents",
		})
	}
	cfg, st, rosterPath, err := load(*cfgPath)
	if err != nil {
		return err
	}
	if st.Server == "" || st.Key == "" {
		if err := setup(ctx, st); err != nil {
			return err
		}
	}
	if st.Human == "" {
		me, err := client.New(st.Server, st.Key).Me(ctx)
		if err != nil {
			return fmt.Errorf("who am I on %s: %w", st.Server, err)
		}
		st.Human = me.User.Handle
	}
	var handles []string
	for _, a := range cfg.Agents {
		handles = append(handles, cfg.Handle(a.Name))
	}
	live := &tui.Live{Server: st.Server, Key: st.Key, Human: st.Human, Lead: cfg.Handle(cfg.Lead().Name), Since: *since, Repo: cfg.Repo}
	o := tui.Options{Backend: live, State: tui.NewState(cfg.Prefix, members(cfg, st.Human)), Server: st.Server, Handles: handles}

	// Host the team here unless another Halo already does (one team, one host).
	on, err := live.Online(ctx, handles)
	switch online := len(on); {
	case *watch:
		o.Watching = "started with -watch"
	case cfg.Repo == "":
		o.Watching = "not in a git repository, so the agents have nowhere to work"
	case err != nil:
		o.Watching = "couldn't check the team: " + err.Error()
	case online > 0:
		o.Watching = fmt.Sprintf("%d of the team's agents are online in another Halo", online)
	default:
		t := newTeam(ctx, cfg, st, rosterPath)
		t.mu.Lock()
		t.start()
		t.mu.Unlock()
		defer func() {
			t.mu.Lock()
			t.stop()
			t.mu.Unlock()
		}()
		o.Team, o.Notes, o.Feed, o.Procs = t, t.notes, t.feed, t
	}
	return tui.Run(ctx, o)
}

func initCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	cfgPath := fs.String("config", "halo.yaml", "Roster file.")
	server := fs.String("server", "", "Lagrange server URL.")
	key := fs.String("key", "", "API key (lgk_...), from Settings → API keys or lagrange login.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, st, _, err := load(*cfgPath)
	if err != nil {
		return err
	}
	if *server != "" {
		st.Server = *server
	}
	if *key != "" {
		st.Key = *key
	}
	if st.Server == "" || st.Key == "" {
		return errors.New("init needs --server and --key")
	}
	me, err := client.New(st.Server, st.Key).Me(ctx)
	if err != nil {
		return fmt.Errorf("checking the key on %s: %w", st.Server, err)
	}
	st.Human = me.User.Handle
	if err := host.Provision(ctx, cfg, st); err != nil {
		return err
	}
	if err := st.Save(); err != nil {
		return err
	}
	fmt.Printf("ready: you are @%s on %s; %d addresses (@%s, ...). next: halo agents, then halo\n",
		st.Human, st.Server, len(cfg.Agents), cfg.Handle(cfg.Lead().Name))
	return nil
}

func agentsCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agents", flag.ContinueOnError)
	cfgPath := fs.String("config", "halo.yaml", "Roster file.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, st, _, err := load(*cfgPath)
	if err != nil {
		return err
	}
	if st.Server == "" || st.Key == "" {
		return errors.New("run halo init first")
	}
	if cfg.Repo == "" {
		return errors.New("run halo agents inside a git repository: the agents work on it")
	}
	return host.Run(ctx, cfg, st, nil)
}
