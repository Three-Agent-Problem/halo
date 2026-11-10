package main

import (
	"strings"
	"testing"
	"time"

	"github.com/three-agent-problem/lagrange/client"
)

func TestFormatTelegramStatus(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	tests := []struct {
		name    string
		tg      client.Telegram
		want    []string
		notWant []string
	}{
		{"not connected", client.Telegram{}, []string{"connected: no", "halo connections"}, []string{"bot:"}},
		{"paired not muted", client.Telegram{Connected: true, BotUsername: "hb", Paired: true, ChatUsername: "me", Level: "milestones"},
			[]string{"connected: yes", "@hb", "paired: yes (@me)", "level: milestones", "not muted"}, []string{"pair link"}},
		{"muted", client.Telegram{Connected: true, BotUsername: "hb", Paired: true, Level: "questions", MutedUntil: &future},
			[]string{"level: questions", "muted until"}, []string{"not muted"}},
		{"expired mute", client.Telegram{Connected: true, BotUsername: "hb", Paired: true, MutedUntil: &past},
			[]string{"not muted"}, []string{"muted until"}},
		{"pending link", client.Telegram{Connected: true, BotUsername: "hb", PairLink: "https://t.me/hb?start=x", PairExpiresAt: &future},
			[]string{"paired: no", "https://t.me/hb?start=x"}, nil},
		{"expired link", client.Telegram{Connected: true, BotUsername: "hb", PairLink: "https://t.me/hb?start=x", PairExpiresAt: &past},
			[]string{"paired: no"}, []string{"https://t.me", "pair link"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatTelegramStatus(tc.tg, now)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in:\n%s", w, got)
				}
			}
		})
	}
}
