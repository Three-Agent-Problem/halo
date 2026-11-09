package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/three-agent-problem/halo/internal/plan"
)

func TestMergedCommOnceAndArrow(t *testing.T) {
	st := demoState()
	now := time.Now()
	mk := func(m string) *plan.Plan {
		return &plan.Plan{Branch: "halo/run", Approved: true, MergedInto: m,
			Subtasks: []*plan.Subtask{{ID: "S1", Title: "x", Model: "haiku", Status: plan.Done}}}
	}
	st.setPlan(mk(""), now)
	st.setPlan(mk("main"), now)
	st.setPlan(mk("main"), now)
	n := 0
	for _, c := range st.Comms {
		if c.Text == "✓ halo/run merged into main" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("merged comm appeared %d times", n)
	}
	out := stripANSI(st.Render(Screen{W: 200, H: 56, Now: now, Server: "demo", Connected: true, Footer: "›"}))
	if !strings.Contains(out, "halo/run → main") {
		t.Fatalf("no arrow in view")
	}
}
