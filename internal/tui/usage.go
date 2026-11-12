package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/three-agent-problem/lagrange/acp"
)

// Usage is what an agent's Claude Code sessions report through ACP usage
// updates: the newest context window fill, and the cost of every session.
type Usage struct {
	Used, Size int64              // tokens in the newest session's context, of its window
	Sessions   map[string]float64 // A2A context → that session's cost so far, USD
	Currency   string
	// Tokens is what the agent's turns read and wrote, summed (acp.ReportTokens).
	Tokens acp.Tokens
}

// Cost is the agent's total over its sessions.
func (u Usage) Cost() float64 {
	t := 0.0
	for _, c := range u.Sessions {
		t += c
	}
	return t
}

// Fill is how full the context window is, 0-1, or -1 when unknown.
func (u Usage) Fill() float64 {
	if u.Size <= 0 {
		return -1
	}
	return min(1, float64(u.Used)/float64(u.Size))
}

// usage records a usage update. The cost is cumulative per ACP session, which
// a2aacp keeps per A2A context, so the newest amount per context replaces the
// one before it and sessions add up.
func (a *Agent) usage(context string, used, size int64, cost *acpCost) {
	if size > 0 {
		a.Usage.Used, a.Usage.Size = used, size
	}
	if cost == nil {
		return
	}
	if a.Usage.Sessions == nil {
		a.Usage.Sessions = map[string]float64{}
	}
	a.Usage.Sessions[context] = cost.Amount
	if cost.Currency != "" {
		a.Usage.Currency = cost.Currency
	}
}

// TeamCost is the whole team's cost so far.
func (s *State) TeamCost() float64 {
	t := 0.0
	for _, a := range s.Agents {
		t += a.Usage.Cost()
	}
	return t
}

// money formats a cost in dollars: $0.42, $12.3, or "—" for nothing yet.
func money(v float64) string {
	switch {
	case v <= 0:
		return "—"
	case v < 10:
		return fmt.Sprintf("$%.2f", v)
	case v < 1000:
		return fmt.Sprintf("$%.1f", v)
	}
	return fmt.Sprintf("$%.0f", v)
}

// tokens formats a token count: 812, 41k, 1.2M.
func tokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1e6)
}

// usageCell is an agent's roster cell: cost and context fill.
func usageCell(u Usage) string {
	if len(u.Sessions) == 0 && u.Size == 0 {
		return ""
	}
	out := c("hub", pad(money(u.Cost()), 6))
	if f := u.Fill(); f >= 0 {
		k := "dim"
		switch {
		case f >= .85:
			k = "fail"
		case f >= .6:
			k = "ask"
		}
		out += c(k, fmt.Sprintf("ctx %2d%%", int(f*100+.5)))
	}
	return out
}

// usageDetail is the grown roster's line about an agent's sessions.
func usageDetail(u Usage) string {
	if len(u.Sessions) == 0 && u.Size == 0 {
		return ""
	}
	var bits []string
	if u.Size > 0 {
		bits = append(bits, fmt.Sprintf("context %s / %s tokens", tokens(u.Used), tokens(u.Size)))
	}
	if n := len(u.Sessions); n > 0 {
		cs := make([]float64, 0, n)
		for _, v := range u.Sessions {
			cs = append(cs, v)
		}
		sort.Sort(sort.Reverse(sort.Float64Slice(cs)))
		var each []string
		for _, v := range cs[:min(4, n)] {
			each = append(each, money(v))
		}
		if n > 4 {
			each = append(each, "…")
		}
		bits = append(bits, fmt.Sprintf("%d session%s: %s", n, pick(n == 1, "", "s"), strings.Join(each, " + ")))
	}
	return strings.Join(bits, " · ")
}
