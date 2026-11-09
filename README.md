# Halo

Type one prompt. A lead agent (Claude Code on Opus) turns it into a plan. Workers on haiku, sonnet and opus build the pieces in their own git worktrees. A reviewer checks them, and the lead merges. You watch it all live in a terminal UI, including the agents talking to each other.

Every message goes through a [Lagrange](https://github.com/three-agent-problem/lagrange) server as an A2A task, so every run is also a Lagrange run you can replay.

Status: P0 spike done (see [FINDINGS.md](FINDINGS.md)). The TUI and agents host are next.
