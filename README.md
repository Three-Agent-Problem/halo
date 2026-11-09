# Halo

Type one prompt. A lead agent (Claude Code on Opus) turns it into a plan. Workers on haiku, sonnet and opus build the pieces in their own git worktrees. A reviewer checks them, and the lead merges them into a run branch, which halo then merges into the branch you started from. You watch it all live in a terminal UI: the plan, each agent's tool calls, and the agents talking to each other.

Every message goes through a [Lagrange](https://github.com/three-agent-problem/lagrange) server as an A2A task, so every run is also a Lagrange run you can replay in its web app.

```
 you ── halo (TUI) ──A2A──▶  Lagrange  ◀──tunnels── halo agents
          ▲  /api/events       │                    ├─ @halo-lead      opus, plans and coordinates (read-only)
          └────────────────────┘                    ├─ @halo-haiku-1   workers, each subtask in its own worktree
                                                    ├─ @halo-sonnet-1
                                                    └─ @halo-reviewer  APPROVE / CHANGES, never edits
```

## Try the TUI without a server

```sh
go build -o halo ./cmd/halo && ./halo --demo
```

## Run it for real

You need a Lagrange server and an API key (Settings → API keys), plus Claude Code and its ACP adapter on the machine that runs the agents: `npm i -g @agentclientprotocol/claude-agent-acp`.

```sh
cd your-repo                                  # a git repo with at least one commit
halo init --server https://lagrange.example.com --key lgk_...
halo agents                                   # terminal 1: the roster, keep it running
halo                                          # terminal 2: the TUI; enter, type a prompt, enter
```

**The agents run Claude Code without asking per tool call.**
- Workers may do anything inside their worktree, and the reviewer and lead may not edit files.
- The worktrees live outside your repo, in `~/Library/Caches/halo/<repo>-<hash>/`, so your checkout never changes.
- When every subtask is merged into `halo/<run>`, halo merges that branch into the branch you started from (needs no uncommitted changes to tracked files; untracked files are fine) and removes the run's worktrees and branches. If that fails (conflict, dirty checkout), the branch is kept and `halo merge <run>` merges it later; `halo merge` lists runs.

A `halo.yaml` next to the repo sets the roster; without one you get one lead, four workers (haiku, two sonnets, opus) and a reviewer:

```yaml
server: https://lagrange.example.com
prefix: halo            # handles become @halo-lead, @halo-sonnet-1, ...
agents:
  - {name: lead, model: opus, role: lead}
  - {name: haiku-1, model: haiku}
  - {name: sonnet-1, model: sonnet}
  - {name: reviewer, model: sonnet, role: reviewer}
```

## Keys

| Key | Does |
|---|---|
| `enter` | Type a prompt; `enter` sends it to the lead. |
| `a` | Answer what waits on you (plan approval): `y` approve, `n` reject, `r` reply with text. |
| `tab` | Move the focus pane to the next agent. |
| `x x` | Cancel the run. |
| `q` | Quit. |

## How a run goes

The lead has only MCP tools to orchestrate with. Each tool call is a Lagrange task, and Halo enforces the rules in Go:

1. **`publish_plan`:** the lead publishes the goal and subtasks (id, spec, acceptance, model, deps). The plan streams to the TUI as the `halo.plan` artifact.
2. **`ask_human`:** the lead asks you to approve. It shows in the TUI and in your Lagrange inbox, and reaches your phone by email or Slack. No subtask starts before you approve.
3. **`delegate`:** sends a subtask whose dependencies are merged to the least busy worker of its model, in a fresh worktree of the run branch.
4. **`wait_events`:** the lead hears about each of these and reacts:
   - worker questions (`ask_lead`, answered with `answer`);
   - finished or failed subtasks;
   - reviews;
   - merge conflicts;
   - your answers.
5. **`request_review`:** the reviewer sees the diff. On APPROVE, the subtask is merged into the run branch. On CHANGES, the lead uses **`follow_up`** to send it back to the same worker, in the same Claude session. A subtask gets at most 2 review rounds.
6. **`escalate`:** moves a failed subtask one model up (haiku → sonnet → opus) and retries it.
7. **`halo merge`:** once all subtasks are merged, halo merges `halo/<run>` into the branch you started from. If that fails, `halo merge <run>` retries it later; `halo merge` alone lists runs.

Lagrange gaps found along the way are in [FINDINGS.md](FINDINGS.md).
