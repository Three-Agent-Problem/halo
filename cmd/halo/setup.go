package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/three-agent-problem/lagrange/client"

	"github.com/three-agent-problem/halo/internal/config"
	"github.com/three-agent-problem/halo/internal/host"
	"github.com/three-agent-problem/halo/internal/tui"
)

// setupCmd opens halo setup: the server, the team, Telegram and preferences, in one
// screen, now or any time later.
func setupCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	cfgPath := fs.String("config", "halo.yaml", "A roster for this repository only (default: the global roster).")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, st, rosterPath, err := load(*cfgPath)
	if err != nil {
		return err
	}
	return runSetup(ctx, cfg, st, rosterPath, false)
}

func runSetup(ctx context.Context, cfg *config.Config, st *config.State, rosterPath string, first bool) error {
	err := tui.RunSetup(ctx, tui.SetupOptions{
		State: st, Roster: cfg, RosterPath: rosterPath, First: first,
		Check: func(ctx context.Context, server, key string) (string, error) {
			me, err := client.New(server, key).Me(ctx)
			if err != nil {
				return "", fmt.Errorf("checking the key on %s: %w", server, err)
			}
			return me.User.Handle, nil
		},
		Login:     config.LagrangeLogin,
		Provision: host.Provision,
		Connector: func(server, key string) tui.Connector { return &tui.Live{Server: server, Key: key} },
	})
	if err != nil {
		return err
	}
	if first && (st.Server == "" || st.Key == "" || st.Human == "") {
		return errors.New("setup isn't finished: Halo needs a Lagrange server and an API key (run halo setup)")
	}
	return nil
}

// firstRun sets Halo up the first time: the setup screen in a terminal, plain
// prompts otherwise.
func firstRun(ctx context.Context, cfg *config.Config, st *config.State, rosterPath string) error {
	if server, key := config.LagrangeLogin(); server != "" {
		if me, err := client.New(server, key).Me(ctx); err == nil {
			st.Server, st.Key, st.Human = server, key, me.User.Handle
			fmt.Printf("halo: using your lagrange login: @%s on %s (halo setup changes it)\n", st.Human, st.Server)
			return st.Save()
		}
	}
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return setup(ctx, st)
	}
	return runSetup(ctx, cfg, st, rosterPath, true)
}
