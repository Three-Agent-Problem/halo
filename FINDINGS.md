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

- **G1. Tool calls appear in events only when they finish.** Each one is a `task` entry with `summary: "Finished <tool> <arg>"` and empty `data`. There is no start event, no input and no `toolCall` data part, so a UI can't show "running `go test` (12 s)…". Repro: the spike's events, seq 8–14.
- **G2. `lagrange tunnel --acp` can't set a model, a cwd per task, MCP servers or tool rules** (`cmd/lagrange/main.go:391` passes a nil `a2aacp.Config`). Halo hosts its executors in-process to work around it. Flags like `--model`, `--allow` and `--mcp` would cover most of it.
- **G4. No token or cost usage in events.** The TUI can't show tokens or $ per agent or per run (the mock had them). ACP reports usage per turn; the relay could record it on the final status entry.
- **G5. Message `metadata` isn't in activity entries.** Halo tags subtasks with a `[S3]` text prefix so the TUI can tie tasks to subtasks. Recording a whitelisted metadata key (or all of it, capped) would remove the hack.
- **G3. No way to get an API key without a browser** for a fresh self-hosted server, short of `POST /api/login` plus `POST /api/keys` with a cookie jar. `lagrange token` mints address tokens only.
