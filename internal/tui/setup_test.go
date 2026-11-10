package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/three-agent-problem/halo/internal/config"
)

func spress(s *setup, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		case "ctrl+s", "ctrl+r":
			msg = tea.KeyPressMsg{Code: rune(k[5]), Mod: tea.ModCtrl}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		srun(s, s.keyPress(msg))
	}
}

// run runs cmd and feeds its message back, until it settles (ticks and polls stop it).
func srun(s *setup, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		switch msg.(type) {
		case nil, setupTick, tgPollMsg:
			return
		}
		if _, ok := msg.(tea.QuitMsg); ok {
			return
		}
		_, cmd = s.Update(msg)
	}
}

func stype(s *setup, text string) {
	s.Update(tea.PasteMsg{Content: text})
	spress(s, "enter")
}

func TestSetup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HALO_STATE", filepath.Join(dir, "state.json"))
	rosterPath := filepath.Join(dir, "roster.yaml")
	roster := config.Default()
	st := &config.State{}
	provisioned := 0
	s := newSetup(context.Background(), SetupOptions{
		State: st, Roster: roster, RosterPath: rosterPath, First: true,
		Check: func(_ context.Context, server, key string) (string, error) {
			if key != "lgk_good" {
				return "", errors.New("unknown or revoked API key")
			}
			return "ada", nil
		},
		Provision: func(context.Context, *config.Config, *config.State) error { provisioned++; return nil },
		Connector: func(string, string) Connector { return &Demo{Human: "ada"} },
		LookPath: func(bin string) (string, error) {
			if bin == "claude-agent-acp" || bin == "agy" {
				return "/usr/local/bin/" + bin, nil
			}
			return "", errors.New("not found")
		},
	})
	s.w, s.h = 160, 50

	// Server: a bad key says so, a good one is saved.
	if !s.inside || s.sec != secServer {
		t.Fatal("a first run opens on the server")
	}
	spress(s, "down", "enter")
	stype(s, "lgk_bad")
	if s.serverOK || !strings.Contains(s.serverErr, "revoked") {
		t.Fatalf("bad key: ok=%v err=%q", s.serverOK, s.serverErr)
	}
	spress(s, "enter")
	stype(s, "lgk_good")
	if !s.serverOK || st.Human != "ada" || st.Key != "lgk_good" || provisioned != 1 {
		t.Fatalf("good key: %+v provisioned=%d", st, provisioned)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "state.json")); !strings.Contains(string(raw), "lgk_good") {
		t.Fatal("the state wasn't saved")
	}
	if out := ansi.Strip(s.render()); strings.Contains(out, "lgk_good") {
		t.Fatal("the key shows on screen")
	}

	// Team: use Antigravity (installed), then stop using it; Codex isn't installed.
	spress(s, "esc", "2")
	for s.agentSel != 1 {
		spress(s, "down")
	}
	spress(s, "space")
	added := roster.Agents[len(roster.Agents)-1]
	if added.Name != "antigravity-1" || added.ACP != "agy acp --process-per-session" || added.Model != "gemini-3.8-flash-low" {
		t.Fatalf("added = %+v", added)
	}
	if raw, _ := os.ReadFile(rosterPath); !strings.Contains(string(raw), "agy acp") {
		t.Fatal("the roster wasn't saved")
	}
	spress(s, "space")
	if len(roster.Agents) != 6 {
		t.Fatalf("antigravity still in the team: %+v", roster.Agents)
	}
	spress(s, "down", "space")
	if len(roster.Agents) != 6 || !strings.Contains(s.flash, "isn't installed") {
		t.Fatalf("codex: %d agents, flash %q", len(roster.Agents), s.flash)
	}
	// Any ACP command, typed.
	for s.agentSel != len(codingAgents) {
		spress(s, "down")
	}
	spress(s, "enter")
	stype(s, "my-agent acp --fast")
	if a := roster.Agents[len(roster.Agents)-1]; a.ACP != "my-agent acp --fast" || a.Name != "my-agent-1" {
		t.Fatalf("typed command: %+v", a)
	}

	// The team table: next model, remove a worker (twice), the lead stays.
	spress(s, "tab")
	s.rowSel = 1 // haiku-1
	spress(s, "m")
	if roster.Agents[1].Model != "opus" {
		t.Fatalf("haiku's next model = %q", roster.Agents[1].Model)
	}
	spress(s, "d")
	if len(roster.Agents) != 7 {
		t.Fatal("one d removed it")
	}
	spress(s, "d")
	if len(roster.Agents) != 6 || roster.Agents[1].Name != "sonnet-1" {
		t.Fatalf("after d d: %+v", roster.Agents)
	}
	s.rowSel = 0
	spress(s, "d", "d")
	if roster.Agents[0].Role != config.RoleLead {
		t.Fatal("the lead was removed")
	}

	// Revert the Team: ctrl+r asks, n keeps, y puts it back as it was (and saves).
	s.rowSel = 1
	spress(s, "m")
	if !s.changed(secTeam) {
		t.Fatal("the team didn't change")
	}
	spress(s, "ctrl+r")
	if s.revertAsk != secTeam || !strings.Contains(ansi.Strip(s.footer()), "Revert Team") {
		t.Fatal("ctrl+r didn't ask")
	}
	spress(s, "n")
	if !s.changed(secTeam) {
		t.Fatal("n reverted")
	}
	spress(s, "ctrl+r", "y")
	if s.changed(secTeam) || len(roster.Agents) != 6 || roster.Agents[1].Model != "haiku" {
		t.Fatalf("after y: %+v", roster.Agents)
	}
	if got, _ := config.Load(rosterPath); len(got.Agents) != 6 || got.Agents[1].Name != "haiku-1" {
		t.Fatal("the revert wasn't saved")
	}
	spress(s, "ctrl+r")
	if s.revertAsk >= 0 || !strings.Contains(s.flash, "nothing changed") {
		t.Fatal("reverting an unchanged section asked")
	}
	// The server keeps its own changes: the team's revert didn't touch it.
	if st.Key != "lgk_good" {
		t.Fatal("the team's revert reverted the server")
	}
	spress(s, "s") // a no-op key in the team table
	s.rowSel = 1
	spress(s, "d", "d")

	// Preferences.
	spress(s, "esc", "4", "space")
	if roster.Notifies() {
		t.Fatal("notifications still on")
	}
	got, err := config.Load(rosterPath)
	if err != nil || got.Notifies() || len(got.Agents) != 5 {
		t.Fatalf("saved roster: %+v %v", got, err)
	}

	// Telegram, through the same wizard as /connections.
	spress(s, "esc", "3")
	if s.tg == nil || s.tg.st == nil || s.tg.st.Connected {
		t.Fatalf("telegram: %+v", s.tg)
	}
	s.Update(tea.PasteMsg{Content: "123456789:AAH-secret-secret-secret-0123456789ab"})
	spress(s, "enter")
	if !s.tg.st.Connected || s.tg.link == "" {
		t.Fatalf("telegram connect: %+v", s.tg.st)
	}
	spress(s, "d", "d") // disconnect again, for the demo's shared state

	// o opens Lagrange in the browser, unless there is none (SSH).
	var opened string
	oldOpen, oldCan := openURL, canOpenBrowser
	defer func() { openURL, canOpenBrowser = oldOpen, oldCan }()
	openURL = func(u string) error { opened = u; return nil }
	canOpenBrowser = func() bool { return false }
	spress(s, "esc", "o")
	if opened != "" || !strings.Contains(s.flash, "no browser") {
		t.Fatalf("headless: opened %q, flash %q", opened, s.flash)
	}
	canOpenBrowser = func() bool { return true }
	spress(s, "o")
	if opened != st.Server {
		t.Fatalf("opened %q, want %q", opened, st.Server)
	}

	out := ansi.Strip(s.render())
	for _, want := range []string{"HALO SETUP", "Lagrange", "Team", "Telegram", "Preferences", "@ada", "o open Lagrange"} {
		if !strings.Contains(out, want) {
			t.Errorf("screen lacks %q", want)
		}
	}
}

// TestSetupSnapshot writes an HTML screenshot of halo setup's team section to
// $HALO_SETUP_SNAP. Skipped unless it is set.
func TestSetupSnapshot(t *testing.T) {
	path := os.Getenv("HALO_SETUP_SNAP")
	if path == "" {
		t.Skip("set HALO_SETUP_SNAP=out.html")
	}
	dir := t.TempDir()
	t.Setenv("HALO_STATE", filepath.Join(dir, "state.json"))
	roster := config.Default()
	s := newSetup(context.Background(), SetupOptions{
		State: &config.State{Server: "http://localhost:8190", Key: "lgk_x", Human: "kaan"}, Roster: roster, RosterPath: filepath.Join(dir, "roster.yaml"),
		LookPath: func(bin string) (string, error) {
			switch bin {
			case "claude-agent-acp", "agy", "goose":
				return "/usr/local/bin/" + bin, nil
			}
			return "", errors.New("no")
		},
	})
	s.w, s.h = 210, 58
	spress(s, "2")
	s.agentSel = 1
	spress(s, "space")
	s.agentSel = 1
	for i, l := range strings.Split(s.render(), "\n") {
		if w := ansi.StringWidth(l); w > s.w {
			t.Errorf("line %d is %d wide: %q", i, w, ansi.Strip(l))
		}
	}
	if err := os.WriteFile(path, []byte(toHTML(s.render())), 0o644); err != nil {
		t.Fatal(err)
	}
}
