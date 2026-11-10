package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/three-agent-problem/lagrange/client"

	"github.com/three-agent-problem/halo/internal/config"
	"github.com/three-agent-problem/halo/internal/tui"
)

// connectionsCmd connects a Telegram bot to the person's Lagrange account, the same
// thing /connections does in the TUI: what waits on them reaches their phone, with
// buttons to answer, and their runs report there.
func connectionsCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("connections", flag.ContinueOnError)
	token := fs.String("token", "", "The bot token from @BotFather (default: asked for, hidden). LAGRANGE_TELEGRAM_TOKEN works too.")
	level := fs.String("level", "", "What the bot sends besides questions: questions or milestones.")
	pair := fs.Bool("pair", false, "Make a new pairing link (pair another chat).")
	test := fs.Bool("test", false, "Send a test message.")
	disconnect := fs.Bool("disconnect", false, "Disconnect the bot (forget its token and chat), to start over.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := config.LoadState()
	if err != nil {
		return err
	}
	if st.Server == "" || st.Key == "" {
		return errors.New("no Lagrange server yet: run halo (or halo init --server URL --key lgk_…) first")
	}
	c := client.New(st.Server, st.Key)
	if *disconnect {
		if err := c.DisconnectTelegram(ctx); err != nil {
			return err
		}
		fmt.Println("✓ disconnected: run halo connections to connect a bot again")
		return nil
	}
	tg, err := c.Telegram(ctx)
	if err != nil {
		return fmt.Errorf("reading the connection from %s: %w", st.Server, err)
	}

	if !tg.Connected {
		t := strings.TrimSpace(nonEmptyStr(*token, os.Getenv("LAGRANGE_TELEGRAM_TOKEN")))
		if t == "" {
			fmt.Println(`Connect Telegram: Halo's questions reach your phone, with buttons to answer.
The bot only answers with buttons (approve, reject, pick an option). It can't start,
stop or message anything, and typed text goes nowhere.

  1. In Telegram, open @BotFather and send /newbot
  2. Pick a name, then a username that ends in "bot"
  3. Paste the token it sends you below`)
			if t, err = readToken(); err != nil {
				return err
			}
		}
		if t == "" {
			return errors.New("no token: paste the one @BotFather sent, or pass --token")
		}
		if tg, err = c.ConnectTelegram(ctx, t); err != nil {
			return err
		}
		fmt.Printf("✓ connected to @%s\n", tg.BotUsername)
	} else if *pair || !tg.Paired {
		if tg, err = c.PairTelegram(ctx); err != nil {
			return err
		}
	}

	if tg.PairLink != "" {
		fmt.Printf("\nOpen @%s and tap Start. Scan this with your phone, or open the link:\n\n", tg.BotUsername)
		for _, l := range tui.QR(tg.PairLink) {
			fmt.Println("  " + l)
		}
		fmt.Printf("\n  %s\n\n(the link works once, for 10 minutes) waiting…", tg.PairLink)
		was := tg.ChatUsername
		deadline := time.Now().Add(10 * time.Minute)
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			if tg, err = c.Telegram(ctx); err != nil {
				return err
			}
			if tg.Paired && tg.PairExpiresAt == nil && (tg.ChatUsername != was || !*pair) {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("\nthe link expired: run halo connections again")
			}
		}
		fmt.Printf("\r✓ paired with @%s                              \n", nonEmptyStr(tg.ChatUsername, "your chat"))
		*test = true
	}

	if *level != "" {
		if tg, err = c.SetTelegramLevel(ctx, *level); err != nil {
			return err
		}
	}
	if *test {
		if err := c.TestTelegram(ctx); err != nil {
			return err
		}
		fmt.Println("✓ test message sent")
	}
	what := "questions, plus runs' news and when they finish"
	if tg.Level == "questions" {
		what = "only questions for you"
	}
	fmt.Printf("@%s talks to @%s and sends %s.\n", tg.BotUsername, nonEmptyStr(tg.ChatUsername, "you"), what)
	fmt.Println("Change it with --level questions|milestones, pair another chat with --pair, start over with --disconnect, or in Lagrange → Settings → Telegram.")
	return nil
}

func readToken() (string, error) {
	fmt.Print("\ntoken: ")
	if term.IsTerminal(os.Stdin.Fd()) {
		b, err := term.ReadPassword(os.Stdin.Fd())
		fmt.Println()
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func nonEmptyStr(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
