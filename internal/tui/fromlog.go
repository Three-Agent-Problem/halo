package tui

import (
	"encoding/json"
	"time"

	"github.com/three-agent-problem/halo/internal/lg"
)

// What Lagrange records of an agent's work (see the Lagrange README, "What the
// activity log keeps from agents"): tool calls with their input and output, the
// agent's thoughts and reply, its usage and its commits. A Halo that hosts the team
// gets the first three straight from Claude Code (Feed); one that only watches gets
// them from the log, so its live panel, roster costs and commits work too.

// usageFromLog records a usage entry for its agent. Each entry carries the
// session's cumulative cost, so applying one twice (from the log and the feed)
// changes nothing.
func (s *State) usageFromLog(e lg.Entry) {
	u, _ := e.Data["usage"].(map[string]any)
	if u == nil || s.Agents[e.Recipient] == nil {
		return
	}
	raw := map[string]any{"sessionUpdate": "usage_update", "used": u["used"], "size": u["size"]}
	if c, ok := u["cost"].(map[string]any); ok {
		raw["cost"] = c
	}
	s.feedFromLog(e, raw)
}

// toolFromLog turns a tool call's status entry into the call's transcript block.
func (s *State) toolFromLog(e lg.Entry) {
	tc, _ := e.Data["toolCall"].(map[string]any)
	if tc == nil || s.Hosted {
		return
	}
	raw := map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": tc["id"], "title": tc["title"], "kind": tc["kind"], "status": tc["status"]}
	if in, ok := tc["input"]; ok {
		raw["rawInput"] = in
	}
	if out, ok := tc["output"].(string); ok && out != "" {
		raw["rawOutput"] = out
	}
	s.feedFromLog(e, raw)
}

// chunkFromLog adds a chunk of the agent's thoughts ("agent_thought_chunk") or
// reply ("agent_message_chunk") to its transcript.
func (s *State) chunkFromLog(e lg.Entry, kind, text string) {
	if s.Hosted || text == "" {
		return
	}
	s.feedFromLog(e, map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": text}})
}

func (s *State) feedFromLog(e lg.Entry, raw map[string]any) {
	b, err := json.Marshal(raw)
	if err != nil {
		return
	}
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	s.Feed(Update{Agent: e.Recipient, Task: e.TaskID, Context: e.ContextID, Type: raw["sessionUpdate"].(string), Raw: b, At: at})
}

// commitFromLog adds a commit an agent published (lagrange/code) to the run's list.
func (s *State) commitFromLog(e lg.Entry, t *Task) bool {
	m, _ := e.Data["commit"].(map[string]any)
	sha, _ := m["sha"].(string)
	if sha == "" {
		return false
	}
	str := func(k string) string { v, _ := m[k].(string); return v }
	num := func(k string) int { v, _ := m[k].(float64); return int(v) }
	c := Commit{SHA: sha, Author: str("author"), Subject: str("subject"), Refs: str("branch"),
		Add: num("insertions"), Del: num("deletions"), Sub: t.Sub, Task: e.TaskID}
	if files, ok := m["files"].([]any); ok {
		c.Files = len(files)
	}
	if ps, ok := m["parents"].([]any); ok {
		for _, p := range ps {
			if p, ok := p.(string); ok {
				c.Parents = append(c.Parents, p)
			}
		}
	}
	c.At, _ = time.Parse(time.RFC3339, str("at"))
	if c.At.IsZero() {
		c.At = e.At
	}
	if c.Sub == "" {
		if mm := subRe.FindStringSubmatch(c.Subject); mm != nil {
			c.Sub = mm[1]
		}
	}
	s.SetCommits([]Commit{c})
	return true
}
