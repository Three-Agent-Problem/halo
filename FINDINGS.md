# Findings: Lagrange as seen from Halo

Every gap found while building Halo on Lagrange, with a repro. Fix these in Lagrange, not in Halo.

## P0 spike (2026-11-09, lagrange 50f8b2d, claude-agent-acp 0.101.1)

`go run ./cmd/spike` with `LAGRANGE_SERVER` and `LAGRANGE_KEY` set.

| # | Assumption | Result |
|---|---|---|
| 1 | `claude-agent-acp` takes a model through `a2aacp.Config.Settings` (`{"model":"haiku"}`) | ✅ the agent reported "Claude Haiku 5.5" |
| 2 | `/api/events` works with an API key and shows the work | ✅ works with an API key; see gap G1 |
| 3 | An MCP tool call from a worker becomes an A2A call to another address inside the worker's run | ✅ logged as `kind: message`, worker → lead, `parentTaskId` = the worker's task, with `request`, `reply` and `replyContent` |
| 4 | A per-session cwd (`Config.Session`) | ✅ the file landed in `ctx-<id>/` |
| 5 | Claude Code tolerates a blocking MCP tool | ✅ a 20 s `ask_lead` was fine (longer waits not tested yet) |
| 6 | An Opus lead follows the tool protocol | ⏳ not run yet: `halo agents` needs the user's go-ahead (it runs Claude Code with tools auto-approved) |

Notes:
- Lagrange's relay forwards message `metadata` to the agent (`relay/relay.go:302`), so subtask, branch and run information can ride on it.
- A new task in the same platform context gets no remote context (each task link has its own `RemoteContextID`), so the agent starts a fresh ACP session. Halo keeps a worker's memory across review rounds with its own `a2aacp.SessionStore`, keyed by subtask.
- Streamed answers arrive as many small artifact appends. Clients must join them by `artifactId`.

## Gaps

All nine were fixed in Lagrange on 2026-11-10, and Halo uses the fixes (lagrange `ef1fdf6`). Each entry keeps the original finding, then says what fixed it.

- **G1. Tool calls appear in events only when they finish.** ✅ *Fixed:* `acp.Converter` reports each call when it starts (`in_progress`, with its input) and when it ends (with its output, capped at 4 KB), as a `toolCall` data part the activity log keeps in `data.toolCall`. The Lagrange Runs page shows each call as one step with its duration. Halo's agents use it.
- **G2. `lagrange tunnel --acp` can't set a model, a cwd per task, MCP servers or tool rules.** ✅ *Fixed:* `--model`, `--set`, `--caller-settings`, `--allow`/`--deny`, `--mcp`, `--per-context dir|worktree` and `--thoughts`. Halo still hosts its executors in-process, because its lead needs Halo's own MCP tools and plan state.
- **G3. No way to get an API key without a browser.** ✅ *Fixed:* `lagrange login --email you@example.com` (password from `LAGRANGE_PASSWORD` or stdin) and, on the server, `lagrange token --api-key --email you@example.com`.
- **G4. No token or cost usage in events.** ✅ *Fixed:* a usage update becomes a `usage` entry (`data.usage`, plus `data.costDelta`), and each run carries its `cost`. Halo's roster reads usage from the log, so a watch-only Halo shows costs too, and they survive a restart.
- **G5. Message `metadata` isn't in activity entries.** ✅ *Fixed:* tags (`delegate.TagsKey`, `{"subtask": "S3"}`) are recorded as `data.tags`. Halo tags every delegated task with its run and subtask, and its messages no longer start with `[S3] ` (old logs still parse).
- **G6. Lagrange has no notion of a code change.** ✅ *Fixed:* the `code` package defines commit artifacts (sha, branch, files, stat and the patch), recorded as `data.commit` and shown on the Runs page. Halo's workers publish each commit, and the commits panel takes them from the log, falling back to the published patch for the diff when the commit isn't in the local repository.
- **G7. A resumed conversation needs the root task's context.** ✅ *Fixed:* `Run.contextId`. Halo takes it from the runs list.
- **G8. An agent's raw output stays on the machine that runs it.** ✅ *Fixed for everything but shells:* tool calls with input and output (G1), thoughts (the `thoughts` artifact), replies and commits all reach the log. A watch-only Halo builds panel 3's live transcript from them. Shells (panel 8) read the local process table and stay on the hosting machine.
- **G9. The platform API's send has no file parts.** ✅ *Fixed:* `POST /api/send` takes `files` (base64 bytes or URLs) as A2A file parts, plus `tags` and `metadata`; `lagrange send --file`. Halo attaches pasted images to the prompt (up to 5 MB) and keeps their paths in the text for agents on the same machine.

## Telegram and run reports (2026-11-10)

Halo wanted to keep a run moving from a phone and to end each run with a shareable summary. Both went into Lagrange as generic features, and Halo is their first user.

- **R1. Questions were a Halo-only format.** The multiple-choice form (`ext/halo/questions`) lived in Halo, so Lagrange's inbox could only show it as text. ✅ *Moved:* `lagrange/ask` (`https://threeagents.dev/ext/questions`, the old key still parses). The web inbox renders option chips, and Telegram renders buttons.
- **R2. Nothing marked "this is an approval".** A plan approval looked like any question. ✅ *Added:* `ask.ApprovalKey` (`ext/approval`) in the message metadata, plus `ask.ApprovalField`, which also recognizes a structuredinput form with exactly one boolean (the procurement buyer's `{approved, note?}`). Halo sets the key on the plan approval.
- **R3. No notion of a run's status beyond task states.** ✅ *Added:* `lagrange/report` (`ext/report`), an artifact with a headline, status, progress, built items, stats, outcome and an optional notice. The relay records it, `/api/runs` returns the latest one, the Runs page shows it as a card, and Telegram forwards its notices and the final one. Halo publishes it on every plan change and when the run lands.
- **R4. Notifications were email and Slack only, and one-way.** ✅ *Added:* `notify.Channel` and the `telegram` package: a per-person bot, long polling (no public URL), one poller per bot behind an advisory lock, buttons with one-time tokens, and answers through the same `Platform.answer` checks as the inbox. Its reach is a closed `Deps` struct (one answer function, three reads), and a test pins it.
- **R5. Cost is only known to the host.** The report's stats come from Halo's plan, but the team's cost is Halo's roster sum, so the TUI adds it to the card while Lagrange's run card shows the run's `cost` separately. A report could carry cost itself once agents report usage per run reliably.

## Usage limits (2026-11-19)

- **U1. A usage limit killed the run.** When the 5-hour limit hit, every agent's turn failed, the lead's root task failed, and nothing could continue it. Logging in to another account didn't help, because running `claude` processes keep their login. ✅ *Fixed in Halo:* claude-agent-acp ends the turn with a JSON-RPC error that names the limit ("Internal error: You've hit your 5-hour limit · resets 7:50pm (Europe/Rome)", data `{"errorKind":"rate_limit"}`). Halo spots it (`host/limit.go`), pauses the lead's turn, the worker or the review instead of failing them, and asks the person whether to wait or continue now. Then it restarts every agent process (`host/procs.go` swaps the a2aacp executor; tasks in progress finish on the old one) and continues each paused piece in its own session. The TUI has `/login` and `/continue`.
- **U2. A run's state lives only in memory.** Claude's conversations are on disk, so a restarted Halo loads them again. Halo's own state is not: the plan, which worktree and session belong to which subtask, review rounds. Quitting Halo mid-run loses it, and `/resume` starts a new run with a fresh plan. ⏳ *Open:* save each run to `<cache>/runs/<short>.json` and rebuild it on start.

## Open TUI issues (2026-11-19)

Reported by the user after real runs.

- **T1. The summary card can't be copied or scrolled.** When a run ends, `c` doesn't copy the summary. ⏳ *Wanted:* the card says "press o to open", which opens the summary in a terminal markdown viewer you can scroll up and down. You can also ask the lead about it from there.
- **T2. You can't talk about a finished run.** Once a run ends, `/btw` says "no run to ask about · just send it as a prompt" (`tui/app.go` `btw`, gated on `m.running()`). A plain prompt asks y/n for a new run. ⏳ *Wanted:* a finished run on screen stays talkable. A message about it goes to the lead in the same conversation without starting a run, and the TUI says what will happen before it happens.
- **T3. It isn't clear what happened when something ends.** When a subtask or run finishes, the screen doesn't say what was done or what comes next. ⏳ *Wanted:* a plain line for each ending: what finished, what it changed, and what happens now (review, merge, the next subtask, or waiting on you).
