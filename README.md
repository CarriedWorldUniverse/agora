# agora

[![CI](https://github.com/CarriedWorldUniverse/agora/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/CarriedWorldUniverse/agora/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/CarriedWorldUniverse/agora?include_prereleases&sort=semver&display_name=tag)](https://github.com/CarriedWorldUniverse/agora/releases)
[![License](https://img.shields.io/github/license/CarriedWorldUniverse/agora)](LICENSE)

An interactive operator-facing CLI for the nexus cluster.

`agora` is the operator's terminal-resident presence on the nexus bus. It holds a persistent WebSocket connection to nexus (push delivery, no polling), renders chat from the cluster in real time, lets the operator type into the conversation, and gives the aspect identity it runs as a proactive notification channel for context the operator needs to know.

Under the hood, agora reuses the same machinery every autonomous aspect already runs: bridle's claudecode subprocess driver for per-turn model invocation, nexus's funnel for FIFO inbox + filter + tool dispatch, the per-aspect keyfile for identity + credentials. The novel piece is the outer shell — a TUI built with bubbletea — and the routing rule that gives operator-typed messages a private channel separate from bus chat.

## Status

Pre-release. v0 build in progress. See [`docs/spec.md`](docs/spec.md) for the design and the v0 build plan.

## Architecture (one paragraph)

agora is a thin outer shell over a per-turn claude-code engine. The shell is persistent: it owns the WS to nexus, a FIFO inbox shared between chat.deliver pushes and operator-typed input, a bubbletea TUI with a chat panel + input prompt + status line, and the routing rule that decides whether each turn's reply goes back to the bus or stays in the TUI. The engine is per-turn: each Deliberate spawns `claude --resume <sid> -p <prompt>` via bridle, inheriting claude-code's slash commands, MCP servers, native tool surface, and Task subagent. The state boundary between shell and engine is a spec invariant: anything that crosses it on each turn is enumerated, anything that doesn't is forbidden until designed deliberately.

## What this is not

- **Not a replacement for claude-code.** claude-code stays the right tool for code-editing work. agora is the right tool for chat-driven coordination work. Side-by-side, not vs.
- **Not a vessel.** [`vessel`](https://github.com/CarriedWorldUniverse/vessel) is the avatar-and-voice front-end. agora is the terminal-resident text front-end. Different shapes, same direction (operator interfaces to the cluster).
- **Not an autonomous aspect.** Autonomous aspects use `agentfunnel` in nexus's runtime. agora is specifically for operator-attended sessions — though they share the same internals where useful.

## Family

- [`nexus`](https://github.com/CarriedWorldUniverse/nexus) — the cluster substrate: broker, Frame, dispatcher, knowledge, chat, roster.
- [`bridle`](https://github.com/CarriedWorldUniverse/bridle) — the one-turn driver library. agora uses bridle's claudecode provider as its per-turn engine.
- [`cairn`](https://github.com/CarriedWorldUniverse/cairn) — repo hosting (forgejo fork).
- [`vessel`](https://github.com/CarriedWorldUniverse/vessel) — Tauri avatar + voice front-end to the cluster.

## License

Apache-2.0. See [LICENSE](LICENSE).
