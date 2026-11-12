package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/three-agent-problem/lagrange/acp"
)

func usageUpdate(t *testing.T, agent, ctx string, used, size int64, cost float64) Update {
	t.Helper()
	v := map[string]any{"sessionUpdate": "usage_update", "used": used, "size": size}
	if cost >= 0 {
		v["cost"] = map[string]any{"amount": cost, "currency": "USD"}
	}
	raw, _ := json.Marshal(v)
	return Update{Agent: agent, Task: "t-" + ctx, Context: ctx, Type: "usage_update", Raw: raw, At: time.Now()}
}

func TestUsagePerSession(t *testing.T) {
	st := demoState()
	w := "halo-sonnet-1"
	// The cost is cumulative per session: the newest amount replaces the one
	// before it, and a second context is a second session that adds up.
	st.Feed(usageUpdate(t, w, "c1", 10_000, 200_000, .10))
	st.Feed(usageUpdate(t, w, "c1", 30_000, 200_000, .25))
	st.Feed(usageUpdate(t, w, "c2", 5_000, 200_000, .05))
	st.Feed(usageUpdate(t, w, "c2", 160_000, 200_000, -1)) // after compaction: no cost
	u := st.Agents[w].Usage
	if got := u.Cost(); got < .299 || got > .301 {
		t.Fatalf("cost %.3f, want .30", got)
	}
	if u.Used != 160_000 || u.Fill() != .8 {
		t.Fatalf("context %d (%.2f)", u.Used, u.Fill())
	}
	if got := st.TeamCost(); got < .299 || got > .301 {
		t.Fatalf("team %.3f", got)
	}
	if cell := stripANSI(usageCell(u)); cell != "$0.30 ctx 80%" {
		t.Fatalf("cell %q", cell)
	}
	if d := usageDetail(u); !strings.Contains(d, "160k / 200k") || !strings.Contains(d, "2 sessions: $0.25 + $0.05") {
		t.Fatalf("detail %q", d)
	}
	if len(st.Agents[w].Feed) != 0 {
		t.Fatal("usage isn't a transcript block")
	}

	// Turns' tokens (acp.ReportTokens) add up and leave the context fill alone.
	for range 2 {
		raw, _ := json.Marshal(map[string]any{"sessionUpdate": "usage_update", "_meta": map[string]any{acp.TokensMeta: map[string]any{"output": 300, "cacheRead": 20_000}}})
		st.Feed(Update{Agent: w, Task: "t-c2", Context: "c2", Type: "usage_update", Raw: raw, At: time.Now()})
	}
	u = st.Agents[w].Usage
	if u.Tokens != (acp.Tokens{Output: 600, CacheRead: 40_000}) || u.Used != 160_000 || u.Cost() < .299 {
		t.Fatalf("after tokens: %+v", u)
	}
}
