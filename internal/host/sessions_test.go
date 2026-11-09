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
	lead := openSessions(path).scope("lead")
	if err := lead.Put(ctx, "ctx-1", a2aacp.Session{ID: "sess-1", AgentKey: "k"}); err != nil {
		t.Fatal(err)
	}
	again := openSessions(path)
	got, ok, err := again.scope("lead").Get(ctx, "ctx-1")
	if err != nil || !ok || got.ID != "sess-1" {
		t.Fatalf("after reopening: %+v %v %v", got, ok, err)
	}
	if _, ok, _ := again.scope("halo-sonnet-1").Get(ctx, "ctx-1"); ok {
		t.Fatal("scopes must not share keys")
	}
}
