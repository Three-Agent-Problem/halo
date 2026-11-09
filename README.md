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

You need a Lagrange server and an API key (Settings → API keys), plus Claude Code and its ACP adapter: `npm i -g @agentclientprotocol/claude-agent-acp`.

```sh
cd your-repo      # a git repo with at least one commit
halo              # first run asks for the server and the key
```

- **Halo hosts the team itself.** It starts the lead, the workers and the reviewer, and they work on the repository you started it in.
  - Quitting while a run is in flight asks first, because the agents stop with Halo.
  - **One team, one host.** If the team is already online in another Halo, this one opens watch-only. Use `halo -watch` to watch on purpose.
  - `halo agents` still runs the team headless, for hosting it on another machine.
- **The team is global.** It lives in `~/Library/Application Support/halo/roster.yaml` and you edit it in panel 7. A `halo.yaml` in a repository overrides it for that repository only.
- **Halo opens with an empty screen.** The plan panel offers your last run, and `/resume` reopens any past run: its whole history loads at once, and your next prompt continues that conversation, so the lead remembers it. Claude sessions are saved to disk, so this works across restarts too.

**The agents run Claude Code without asking per tool call.**
- Workers may do anything inside their own worktree; the reviewer and the lead may not edit files.
- The worktrees live outside your repo, in `~/Library/Caches/halo/<repo>-<hash>/`.
- When every subtask is merged, Halo merges the run into the branch you have checked out, then deletes its worktrees and branches.

## Panels and keys

Like lazygit: every panel has a number and `enter` opens the selected item. `+` grows the focused panel into the space next to it: first its whole column (the panels above and below collapse to one-line bars), then the orbit's width too, then the whole screen. `-` below normal collapses a panel to a one-line bar with a summary, which stays closed while you cycle with `tab`; one `+` brings it back. Moving the focus puts grown panels back to normal. The orbit and comms never collapse.

| Panel | Shows | `enter` opens |
|---|---|---|
| `1` plan | subtasks, models, progress and what each running worker does now; grown: a kanban (pending · running · waiting · done, `h`/`l` `j`/`k` move); full: a timeline with a lane per agent and a NOW list | the subtask: status, attempts, its commits and every message about it |
| `2` orbit | every message travelling through Lagrange | (grows it) |
| `3` agent | live: the agent's raw Claude Code output (thinking, messages, each tool call with its output); `v` switches to the tool calls Lagrange saw, `h`/`l` switch agents | the whole entry: a tool call's input and full output |
| `4` comms | agent ⇄ agent messages, wrapped (`f` filters by agent) | the whole message |
| `5` commits | the run's commits as a branch graph, live: one lane per subtask branch, colored by its worker's model, merges as `◆`; grown: stats, refs, a legend | the diff (`[` `]` previous/next) |
| `6` events | the raw Lagrange activity feed | the entry as JSON |
| `7` roster | the team; `n` add · `m` model · `d d` remove · `R` restart | (watch that agent in 3) |
| `8` shells | every command the agents run and every server they leave up, with its ports; `o` opens the port, `d d` stops it and its children | the live output, command, cwd and ports |

| Key | Does |
|---|---|
| `1`–`8`, `tab` | focus a panel |
| `+` / `-` | grow / shrink it: bar → normal → column → wide → full |
| `j` `k` `g` `G` `ctrl+d` `ctrl+u` | move, scroll |
| `enter` / `esc` | open / back |
| `i` | prompt the lead |
| `/` | commands: `/clear` (start fresh), `/resume` (reopen a past run), `/merge` (land the run now), `/agents`, `/help` |
| `a` | answer what waits on you: `y` approve, `n` reject, `r` reply |
| `x x` | cancel the run |
| `?` | every key; type to search |
| `q` | quit |

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
