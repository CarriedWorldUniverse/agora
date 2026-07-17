// Package tui is the lean bubbletea TUI (agora-spec-tui.md): the real
// interactive entrypoint cmd/agora launches. It replaces the v0-legacy
// broker-mediated chat TUI (internal/ui, retired at U15 — see cmd/agora's
// doc comment and the U15 build report for exactly what was removed).
//
// Build unit: U15 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-tui.md.
//
// Files:
//   - stream.go: the two-region streaming model (§2) — newline-gated,
//     table-holdback-aware commit of raw text into an append-only stable
//     region. The correctness core this unit is graded on.
//   - cell.go: the v1 cell kinds (§1) and their rendering.
//   - diff.go: diff/patch cell rendering (§7).
//   - composer.go: the composer state machine (§4/§4a) — trigger
//     detection, atomic tokens, paste collapse, history, queue-while-
//     running, the %-override parser.
//   - approval.go: the modal option→(decision,scope) mapping (§3) —
//     permission-shaped approval kinds, the question card, and the plan
//     gate, each producing the exact contracts.Input the io session
//     protocol (U2) and approvals pipeline (U7) expect.
//   - backend.go: the Backend seam + its real implementation, a
//     session-protocol client (io.ClientFrame/ServerFrame over a unix
//     socket or websocket) — production wiring, proven end-to-end against
//     the real io.Session/io.ServeConn machinery in backend_test.go (no
//     daemon binary exists yet, U18; this is what a daemon's ServeConn
//     speaks).
//   - model.go: the bubbletea Model/Update/View, following §0's
//     non-negotiable idea — the transcript lives in the terminal's own
//     scrollback (printed via Printer/tea.Println), never a viewport.Model.
//   - theme.go: a Theme injected everywhere rendering happens, so golden
//     snapshot tests get byte-stable, colorless (PlainTheme) output.
package tui
