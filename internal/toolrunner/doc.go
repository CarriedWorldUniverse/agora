// Package toolrunner is agora's native, agora-side tool execution surface —
// Phase 1 of the engine adapter (NEX-775). agora will drive bridle's
// claude-sdk lane in FUNNEL mode: agora's tools are passed to Claude and
// executed agora-side, not by bridle's own ToolRunner. This package builds
// that agora-native surface with NO bridle dependency; a later phase wraps
// it into bridle's ToolRunner shape.
//
// Layout:
//   - call.go: Call/Result, the Family interface every native tool family
//     implements.
//   - roots.go: Roots, the writable-root set (working dir + add_dirs) and
//     the lexical containment/protected-path checks shared by the fs family
//     and the approval classifier.
//   - fs.go: the fs family (read_file/write_file/edit_file/list_dir/glob/
//     grep) — hard, symlink-aware path containment plus the read-before-
//     write staleness guard.
//   - exec.go: the exec family (run_command) — timeout + captured output.
//     Sandbox/execpolicy enforcement is PARKED (agora-spec-io.md §3a
//     "enforcement mechanism ... remains parked"); this package only
//     enforces the timeout and captures output.
//   - classify.go: Classify, the pure approval-kind classifier (U-B2) —
//     DEVIATIONS.md §5's exact wire payload shapes.
//   - mcp.go: MCPSource, the narrow interface Surface folds MCP tools
//     through, and the mcp__-prefix routing (U-B3).
//   - surface.go: Surface, the registry merging N families + MCPSource into
//     one []contracts.ToolSpec and one Execute dispatch.
//
// Spec: docs/spec/agora-spec-mcp.md §5a (native families + fs-watcher),
// agora-spec-io.md §3a (working dir + add_dir write envelope),
// docs/spec/DEVIATIONS.md §5 (approval payload wire shapes).
package toolrunner
