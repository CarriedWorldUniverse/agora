package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
)

// terminalRestoreEscapes are the ANSI sequences that undo every TUI
// mode agora's bubbletea program enables (alt-screen, legacy
// mouse-tracking modes from older builds, hidden cursor). Idempotent —
// safe to write even when bubbletea already cleaned up. Written raw to
// stdout so we don't depend on any of the higher-level state still being
// intact.
//
// Operator-reported 2026-05-27: when an unrecovered panic in a
// bubbletea-internal goroutine killed agora, none of bubbletea's
// defers fired and the terminal stayed in mouse-tracking mode —
// every mouse movement spammed visible hex into the now-detached
// terminal session. This guard fires from main's defer chain so a
// normally-returning main always restores the terminal even when
// bubbletea's path didn't.
//
// Sequence breakdown:
//
//	\x1b[?1003l  disable mouse-all-motion (leftover from older builds)
//	\x1b[?1006l  disable SGR mouse encoding
//	\x1b[?1000l  disable basic X10 mouse tracking
//	\x1b[?1049l  exit alt-screen → main-screen restored
//	\x1b[?25h    show cursor (bubbletea hides it)
//
// Does NOT help in failure modes where main's defers never run
// (SIGKILL, double-panic, runtime crash). For those, the only
// recourse is `reset` in the terminal or closing the tab.
const terminalRestoreEscapes = "\x1b[?1003l\x1b[?1006l\x1b[?1000l\x1b[?1049l\x1b[?25h"

// restoreTerminalEscapes writes the terminal-restore sequence to
// stdout best-effort. Errors are silent — the process is exiting
// anyway and the operator will see whatever happens to their tty.
func restoreTerminalEscapes() {
	_, _ = os.Stdout.WriteString(terminalRestoreEscapes)
}

// installCrashCapture routes Go runtime crash output (unrecovered
// panic stacks from any goroutine, including bubbletea's internal
// render/input goroutines) to the supplied log file. The crash
// output goes to FD 2 by default; runtime/debug.SetCrashOutput
// duplicates it to an additional FD that survives process death.
//
// Without this, an unrecovered panic in a bubbletea-internal
// goroutine scrolls past on the alt-screen and is lost forever —
// the operator sees a momentary mess and the process dies with no
// post-mortem artifact. With it, the same panic lands in the log
// file and the operator can `grep panic` to find it.
//
// Compatible with the stderr redirect in stderr_unix.go: that one
// pipes os.Stderr writes during TUI mode, SetCrashOutput catches
// runtime-level crashes (lower-level). Both can be active without
// conflict.
//
// f MUST be opened with O_WRONLY|O_APPEND (per SetCrashOutput's
// contract). openLogger satisfies that.
//
// Available since Go 1.23. agora's go.mod is 1.26+.
func installCrashCapture(f *os.File, log *slog.Logger) {
	if f == nil {
		return
	}
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		if log != nil {
			log.Warn("agora: SetCrashOutput failed; runtime panic stacks won't be captured",
				"err", fmt.Sprintf("%v", err))
		}
	}
}
