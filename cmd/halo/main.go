// Command halo turns one prompt into a plan that a team of Claude Code agents builds,
// with every message going through a Lagrange server, and shows it all live.
//
//	halo init --server URL --key KEY   save the server and an API key, create the addresses
//	halo agents                        run the roster (lead, workers, reviewer) on this machine
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
		case "help", "-h", "--help":
			fmt.Println(`usage:
  halo init --server URL --key lgk_...   save the Lagrange server and an API key, create the addresses
  halo agents [-config halo.yaml]         run the roster on this machine (keep it running)
  halo [-config halo.yaml] [-since N]     the TUI
  halo --demo                             the TUI playing a scripted run, no server needed`)
			return nil
		}
	}
	return tuiCmd(ctx, args)
}

func load(path string) (*config.Config, *config.State, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	st, err := config.LoadState()
	if err != nil {
		return nil, nil, err
	}
	if cfg.Server != "" && os.Getenv("LAGRANGE_SERVER") == "" {
		st.Server = cfg.Server
	}
	return cfg, st, nil
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
	cfgPath := fs.String("config", "halo.yaml", "Roster file.")
	demo := fs.Bool("demo", false, "Play a scripted run without a server.")
	since := fs.Int64("since", 0, "Activity sequence to start from (0 replays everything, -1 starts now).")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, st, err := load(*cfgPath)
	if err != nil {
		return err
	}
	var handles []string
	for _, a := range cfg.Agents {
		handles = append(handles, cfg.Handle(a.Name))
	}
	if *demo {
		cfg = config.Default()
		human := "kaan"
		return tui.Run(ctx, tui.Options{
			Backend: &tui.Demo{Human: human, Prefix: cfg.Prefix},
			State:   tui.NewState(cfg.Prefix, members(cfg, human)),
			Server:  "demo (no server)", Handles: handles,
		})
	}
	if st.Server == "" || st.Key == "" {
		return errors.New("no server or API key: run halo init --server URL --key KEY (or set LAGRANGE_SERVER and LAGRANGE_KEY), or try halo --demo")
	}
	human := st.Human
	if human == "" {
		me, err := client.New(st.Server, st.Key).Me(ctx)
		if err != nil {
			return fmt.Errorf("who am I on %s: %w", st.Server, err)
		}
		human = me.User.Handle
	}
	return tui.Run(ctx, tui.Options{
		Backend: &tui.Live{Server: st.Server, Key: st.Key, Human: human, Lead: cfg.Handle(cfg.Lead().Name), Since: *since},
		State:   tui.NewState(cfg.Prefix, members(cfg, human)),
		Server:  st.Server, Handles: handles,
	})
}

func initCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	cfgPath := fs.String("config", "halo.yaml", "Roster file.")
	server := fs.String("server", "", "Lagrange server URL.")
	key := fs.String("key", "", "API key (lgk_...), from Settings → API keys or lagrange login.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, st, err := load(*cfgPath)
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
	cfg, st, err := load(*cfgPath)
	if err != nil {
		return err
	}
	if st.Server == "" || st.Key == "" {
		return errors.New("run halo init first")
	}
	return host.Run(ctx, cfg, st)
}
