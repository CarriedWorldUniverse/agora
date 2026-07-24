# agora

[![CI](https://github.com/CarriedWorldUniverse/agora/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/CarriedWorldUniverse/agora/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/CarriedWorldUniverse/agora?include_prereleases&sort=semver&display_name=tag)](https://github.com/CarriedWorldUniverse/agora/releases)
[![License](https://img.shields.io/github/license/CarriedWorldUniverse/agora)](LICENSE)

A terminal-resident agent harness — its own turn engine, tool surface, and approval model, over a pluggable model provider.

`agora` runs an agentic loop against your working directory: the model reads and edits files, runs commands, fetches pages, delegates to subagents, and calls MCP servers, with every side-effecting action gated by an approval policy you configure. Threads persist, context is curated and compacted as it grows, and the same engine drives an interactive TUI, a headless JSONL pipe, and a long-lived daemon.

```sh
agora                      # interactive TUI in the current directory
agora pipe                 # headless: JSONL in, JSONL out
agora daemon               # long-lived host for attached sessions
agora workflow run f.star  # scripted multi-agent orchestration
```

## Status

Built and in use, and used to build itself. The core loop is complete: turns with
interrupt/resume, a native tool surface, scoped approvals, lifecycle hooks,
subagent delegation, skills, workflows, MCP over stdio, and durable threads.
Per-subsystem design docs live in [`docs/spec/`](docs/spec); the conformance
suite in [`conformance/`](conformance) pins the golden flows.

Known gaps are tracked as open issues.

## Architecture (one paragraph)

`internal/turnengine` owns the turn: it assembles the prompt, calls the provider through [`bridle`](https://github.com/CarriedWorldUniverse/bridle) (the provider abstraction — claudesdk in production, a fake in tests), routes each tool call through `internal/toolrunner`'s families (fs, exec, web, memory, planning, agent-spawn, plus folded-in MCP servers), and gates every call through `internal/approval` before it executes. `internal/ctxmgr` curates and compacts the thread as it grows; `internal/persistence` makes it durable. Front-ends — `internal/tui` (Bubble Tea), `agora pipe`, and `internal/daemon` — all construct the engine through one shared seam, so no lane drifts from another.

## Capabilities

| Area | What's there |
|---|---|
| Tools | `read_file` `write_file` `edit_file` `list_dir` `glob` `grep` `run_command` `web_fetch` `memory_*` `agent` `question` `plan` |
| Approvals | Per-kind policy (exec/patch/read/escalation/mcp), scoped grants (once, session, command-prefix, host), builtin presets from prompt-everything to never-escalate |
| Sandboxing | Working-dir containment on writes; exec classified sandbox-first, with symlink-aware escape detection; SSRF guard on network reads |
| Context | Curation, compaction, fact extraction, skills catalog with a token budget, AGENTS.md discovery |
| Extensibility | MCP servers over stdio (`.mcp.json`), lifecycle hooks (10 events), custom subagent types (`.agora/agents/*.md`), Starlark workflows |
| Persistence | SQLite-backed threads, resume, fork, agent graph |

## Configuration

Read from `.agora/` (project layer) and `~/.agora/` (user layer), project winning:

| File | Purpose |
|---|---|
| `config.json` | defaults (e.g. `default_effort`) |
| `models.json` | model registry/aliases |
| `hooks.json` | lifecycle hooks |
| `agents/*.md` | custom subagent types |
| `skills/*/SKILL.md` | skills |
| `.mcp.json` | MCP servers |
| `AGENTS.md` | project instructions |

`.claude/agents` and `.claude/skills` are also read, for compatibility with existing setups.

## What this is not

- **Not a cluster client.** agora used to be a nexus DM front-end; that shape moved on. It talks to a model provider and your filesystem, not to the broker.
- **Not a vessel.** [`vessel`](https://github.com/CarriedWorldUniverse/vessel) is the avatar-and-voice front-end. agora is the terminal-resident one.
- **Not tied to one model vendor.** The provider seam is `bridle`; claudesdk is today's default, not a requirement of the design.

## Family

- [`bridle`](https://github.com/CarriedWorldUniverse/bridle) — the provider abstraction agora runs on.
- [`nexus`](https://github.com/CarriedWorldUniverse/nexus) — the cluster substrate: broker, Frame, dispatcher, knowledge, chat, roster.
- [`cairn`](https://github.com/CarriedWorldUniverse/cairn) — repo hosting (native go-git).
- [`vessel`](https://github.com/CarriedWorldUniverse/vessel) — Tauri avatar + voice front-end to the cluster.

## License

Apache-2.0. See [LICENSE](LICENSE).
