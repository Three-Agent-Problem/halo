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
