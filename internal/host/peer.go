package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/three-agent-problem/lagrange/activity"

	"github.com/three-agent-problem/halo/internal/lg"
	"github.com/three-agent-problem/halo/internal/plan"
)

// warmFor is how long after its last activity a Claude session's prompt cache is
// likely still warm (the API's default cache lifetime is 5 minutes). A question to a
// warm teammate re-reads its context at a tenth of the input price; a cold one pays
// to write it again.
const warmFor = 5 * time.Minute

// subKnow is what a subtask's worker knows, from the run's Lagrange log: the files it
// read or changed (relative to its worktree), when it last did something, and how
// many tokens its context holds.
type subKnow struct {
	Files      []activity.FileTouch
	LastActive time.Time
	Context    int64
}

// warm reports whether the session's prompt cache is likely still warm at now.
func (k subKnow) warm(now time.Time) bool {
	return !k.LastActive.IsZero() && now.Sub(k.LastActive) < warmFor
}

// subKnowledge groups the run's knowledge by subtask: each subtask owns the tasks its
// current attempt sent (subRT.tasks). Callers hold r.mu.
func subKnowledge(ks []activity.Knowledge, subs map[string]*subRT) map[string]subKnow {
	byTask := map[string]activity.Knowledge{}
	for _, k := range ks {
		byTask[k.TaskID] = k
	}
	out := map[string]subKnow{}
	for id, rt := range subs {
		var sk subKnow
		files := map[string]activity.FileTouch{}
		for _, t := range rt.tasks {
			k, ok := byTask[t]
			if !ok {
				continue
			}
			if k.LastActive.After(sk.LastActive) {
				sk.LastActive = k.LastActive
				if k.Context > 0 {
					sk.Context = k.Context
				}
			}
			for _, f := range k.Files {
				f.Path = relPath(f.Path, rt.dir)
				if old, ok := files[f.Path]; ok {
					f.Read, f.Changed = f.Read || old.Read, f.Changed || old.Changed
					if old.At.After(f.At) {
						f.At = old.At
					}
				}
				files[f.Path] = f
			}
		}
		for _, f := range files {
			sk.Files = append(sk.Files, f)
		}
		slices.SortFunc(sk.Files, func(a, b activity.FileTouch) int { return strings.Compare(a.Path, b.Path) })
		if !sk.LastActive.IsZero() {
			out[id] = sk
		}
	}
	return out
}

// relPath is path relative to the worktree dir, or path when it is outside it.
func relPath(path, dir string) string {
	if dir != "" && strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/") {
		return strings.TrimPrefix(path, strings.TrimSuffix(dir, "/")+"/")
	}
	return path
}

// knowLine says in one line what a teammate knows: "active 2m ago (warm), 31k
// tokens in context; read a.go, b.go; changed a.go".
func knowLine(k subKnow, now time.Time) string {
	var parts []string
	ago := now.Sub(k.LastActive).Truncate(time.Minute)
	warm := "cold"
	if k.warm(now) {
		warm = "warm"
	}
	if ago < time.Minute {
		parts = append(parts, fmt.Sprintf("active just now (%s)", warm))
	} else {
		parts = append(parts, fmt.Sprintf("active %s ago (%s)", strings.TrimSuffix(ago.String(), "0s"), warm))
	}
	if k.Context > 0 {
		parts[0] += fmt.Sprintf(", %dk tokens in context", (k.Context+500)/1000)
	}
	var read, changed []string
	for _, f := range k.Files {
		if f.Changed {
			changed = append(changed, f.Path)
		} else if f.Read {
			read = append(read, f.Path)
		}
	}
	if len(read) > 0 {
		parts = append(parts, "read "+listClip(read, 6))
	}
	if len(changed) > 0 {
		parts = append(parts, "changed "+listClip(changed, 6))
	}
	return strings.Join(parts, "; ")
}

func listClip(xs []string, n int) string {
	if len(xs) <= n {
		return strings.Join(xs, ", ")
	}
	return strings.Join(xs[:n], ", ") + fmt.Sprintf(" and %d more", len(xs)-n)
}

// know fetches the run's log from Lagrange and returns what each subtask knows. It
// returns nil when Lagrange can't say (the brief and ask_peer then go without).
func (r *run) know(ctx context.Context) map[string]subKnow {
	h := r.l.h
	if h.api == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d, err := h.api.Run(ctx, r.id)
	if err != nil {
		h.log.Warn("reading the run's knowledge", "run", r.id, "error", err)
		return nil
	}
	ks := activity.KnowledgeOf(d.Entries)
	r.mu.Lock()
	defer r.mu.Unlock()
	return subKnowledge(ks, r.subs)
}

// peerPick is the teammate ask_peer asks.
type peerPick struct {
	id   string // the subtask
	rt   subRT
	busy bool // its session is in the middle of a turn: ask a fork of it
	know subKnow
}

// pickPeer chooses whom to ask: subtask target when it is given, else the teammate
// whose worker most recently read or changed a file matching about. A busy teammate
// is fine (ask_peer forks its session); one that never worked isn't.
func (r *run) pickPeer(from, target, about string, know map[string]subKnow) (peerPick, error) {
	from = strings.ToUpper(strings.TrimSpace(from))
	target = strings.ToUpper(strings.TrimSpace(target))
	r.mu.Lock()
	defer r.mu.Unlock()
	if target == "" {
		if strings.TrimSpace(about) == "" {
			return peerPick{}, errors.New("say whom to ask (subtask) or what about (a file path)")
		}
		var best string
		var at time.Time
		for id, k := range know {
			if id == from {
				continue
			}
			for _, f := range k.Files {
				if t, ok := (activity.Knowledge{Files: []activity.FileTouch{f}}).Touched(about); ok && t.At.After(at) {
					best, at = id, t.At
				}
			}
		}
		if best == "" {
			return peerPick{}, fmt.Errorf("no teammate in this run has read or changed %s: read it yourself, or name a subtask", about)
		}
		target = best
	}
	s := r.planGet(target)
	switch {
	case s == nil:
		return peerPick{}, fmt.Errorf("there is no subtask %s in this run", target)
	case target == from:
		return peerPick{}, fmt.Errorf("%s is your own subtask", target)
	}
	rt := r.subs[target]
	if rt == nil || rt.session == "" || rt.worker == "" {
		return peerPick{}, fmt.Errorf("%s hasn't been worked on yet, so nobody knows it: read the code yourself or ask_lead", target)
	}
	busy := slices.Contains([]string{plan.Running, plan.Asking, plan.Review}, s.Status)
	return peerPick{id: target, rt: *rt, busy: busy, know: know[target]}, nil
}

// peerText is the message a worker gets when a teammate asks it about its subtask.
func peerText(from, fromSubtask, target, question string) string {
	return fmt.Sprintf("Question from @%s (working on %s) about your subtask %s:\n\n%s\n\n"+
		"Answer from what you already know about the code you worked on, in at most 12 lines: exact names, paths, signatures, JSON shapes, gotchas. "+
		"Don't edit files and don't start new work. If you haven't decided something yet, say so and say what you plan.", from, fromSubtask, target, question)
}

// forkAnswer is a forked session's answer and what it cost.
type forkAnswer struct {
	Text       string
	Cost       float64
	CacheRead  int64
	CacheWrite int64
}

// forkFunc asks a question of a copy of a Claude session (claude --resume
// <id> --fork-session) in dir, without touching the session itself, which may be in
// the middle of a turn.
type forkFunc func(ctx context.Context, dir, sessionID, model, question string) (forkAnswer, error)

// claudeFork is the forkFunc that runs the claude CLI. The fork may read files but
// not change them or run commands.
func claudeFork(ctx context.Context, dir, sessionID, model, question string) (forkAnswer, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	args := []string{"-p", "--resume", sessionID, "--fork-session", "--output-format", "json",
		"--disallowedTools", "Edit,Write,MultiEdit,NotebookEdit,Bash"}
	if model != "" {
		args = append(args, "--model", model)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(question)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return forkAnswer{}, fmt.Errorf("claude fork: %v: %s", err, clip(strings.TrimSpace(stderr.String()), 300))
	}
	var res struct {
		Result string  `json:"result"`
		Cost   float64 `json:"total_cost_usd"`
		Error  bool    `json:"is_error"`
		Usage  struct {
			CacheRead  int64 `json:"cache_read_input_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return forkAnswer{}, fmt.Errorf("claude fork: %v", err)
	}
	if res.Error {
		return forkAnswer{}, fmt.Errorf("claude fork: %s", clip(res.Result, 300))
	}
	return forkAnswer{Text: strings.TrimSpace(res.Result), Cost: res.Cost, CacheRead: res.Usage.CacheRead, CacheWrite: res.Usage.CacheWrite}, nil
}

// askPeer answers a worker's question from a teammate who already knows the code:
// subtask names it, or about (a file path) finds the teammate that last read or
// changed that file. An idle teammate gets the question in its own session, through
// Lagrange. A busy one keeps working: a fork of its session answers.
func (h *Host) askPeer(ctx context.Context, agent, key, target, about, question string) (*mcp.CallToolResult, out, error) {
	w := h.workers[agent]
	if w == nil {
		return nil, out{}, errors.New("unknown agent")
	}
	w.mu.Lock()
	b := w.work[key]
	w.mu.Unlock()
	if b == nil || b.meta.Run == "" || h.lead == nil {
		return ok("There is no run for this task, so there are no teammates to ask.")
	}
	h.lead.mu.Lock()
	r := h.lead.runs[b.meta.Run]
	h.lead.mu.Unlock()
	if r == nil {
		return ok("The run is gone, so there are no teammates to ask.")
	}
	p, err := r.pickPeer(b.meta.Subtask, target, about, r.know(ctx))
	if err != nil {
		return ok("%s", err.Error())
	}
	text := peerText(w.handle, b.meta.Subtask, p.id, question)
	started := time.Now()
	if p.busy {
		ans, err := h.forkPeer(ctx, p, text)
		h.log.Info("ask_peer", "from", w.handle, "to", p.rt.worker, "subtask", p.id, "fork", true, "cost", ans.Cost,
			"cacheRead", ans.CacheRead, "cacheWrite", ans.CacheWrite, "took", time.Since(started).Round(time.Second), "error", err)
		if err != nil {
			return ok("Couldn't ask @%s: %v. Read it yourself or ask_lead.", p.rt.worker, err)
		}
		r.notice("@%s asked @%s (busy on %s), answered by a fork of its session: $%.4f", w.handle, p.rt.worker, p.id, ans.Cost)
		return ok("@%s on %s (it's still working, so a copy of its session answered; it may change its plans): %s", p.rt.worker, p.id, ans.Text)
	}
	h.mu.Lock()
	h.busy[p.rt.worker]++
	h.mu.Unlock()
	defer h.release(p.rt.worker)
	m := meta{Kind: "peer", Run: b.meta.Run, Subtask: p.id, Worktree: p.rt.dir, Branch: p.rt.branch, Session: p.rt.session, From: w.handle}
	res, err := h.caller(w.handle).Send(ctx, lg.Call{To: p.rt.worker, Parent: b.platform, Text: text, Meta: m.asMap(), Immediate: true})
	if err != nil {
		return nil, out{}, err
	}
	t, isTask := res.(*a2a.Task)
	if !isTask {
		return ok("@%s on %s: %s", p.rt.worker, p.id, lg.Text(res))
	}
	state, ans, err := h.await(ctx, string(t.ID))
	if err != nil {
		return nil, out{}, err
	}
	h.log.Info("ask_peer", "from", w.handle, "to", p.rt.worker, "subtask", p.id, "fork", false, "warm", p.know.warm(started),
		"took", time.Since(started).Round(time.Second))
	if state != a2a.TaskStateCompleted {
		return ok("@%s couldn't answer (%s): %s", p.rt.worker, state, ans)
	}
	return ok("@%s on %s: %s", p.rt.worker, p.id, ans)
}

// forkPeer asks a fork of a busy teammate's Claude session.
func (h *Host) forkPeer(ctx context.Context, p peerPick, text string) (forkAnswer, error) {
	pw := h.workers[p.rt.worker]
	if pw == nil {
		return forkAnswer{}, errors.New("unknown teammate")
	}
	if !strings.Contains(h.cfg.Command(pw.agent), "claude") {
		return forkAnswer{}, fmt.Errorf("@%s isn't Claude Code, so its session can't be forked while it works", p.rt.worker)
	}
	sess, found, err := h.sessions.get(p.rt.worker + " " + p.rt.session)
	if err != nil || !found || sess.ID == "" {
		return forkAnswer{}, fmt.Errorf("@%s's session isn't known yet", p.rt.worker)
	}
	fork := h.fork
	if fork == nil {
		fork = claudeFork
	}
	return fork(ctx, p.rt.dir, sess.ID, pw.agent.Model, text)
}
