package host

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"
)

func TestSessionFileSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	ctx := context.Background()
	lead := &leadSessions{f: openSessions(path)}
	if err := lead.Put(ctx, "ctx-1", a2aacp.Session{ID: "sess-1", AgentKey: "k"}); err != nil {
		t.Fatal(err)
	}
	again := &leadSessions{f: openSessions(path)}
	got, ok, err := again.Get(ctx, "ctx-1")
	if err != nil || !ok || got.ID != "sess-1" {
		t.Fatalf("after reopening: %+v %v %v", got, ok, err)
	}
	if _, ok, _ := again.f.get("halo-sonnet-1 ctx-1"); ok {
		t.Fatal("agents must not share keys")
	}
	// A talk's context borrows its run's session, and only while it talks.
	again.alias("ctx-btw", "ctx-1")
	if got, ok, _ := again.Get(ctx, "ctx-btw"); !ok || got.ID != "sess-1" {
		t.Fatalf("alias: %+v %v", got, ok)
	}
	again.unalias("ctx-btw")
	if _, ok, _ := again.Get(ctx, "ctx-btw"); ok {
		t.Fatal("the alias outlived the talk")
	}
}
