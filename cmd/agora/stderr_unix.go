//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// redirectStderr points file descriptor 2 (stderr) at f for the
// duration of the bubbletea Run loop. Returns a restore closure the
// caller defers; calling it dups the saved original FD back into 2
// and closes the saved copy.
//
// Why: Go's runtime writes panic stack traces and goroutine dumps
// directly to FD 2 (not os.Stderr — reassigning the variable is
// insufficient). Bubbletea owns the alt-screen on stdout; if a side
// goroutine panics during TUI mode, the runtime's stderr write lands
// on the same terminal and visibly corrupts the rendered UI plus
// confuses the still-active mouse-tracking byte stream. Operator-
// reported 2026-05-27 ("mess of error messages and mouse movement
// hex"). Redirecting stderr to the log file means panic output is
// captured for later inspection instead of trashing the screen.
//
// On Windows, see stderr_windows.go for the no-op equivalent —
// dup/dup2 semantics differ and the operator hasn't hit this on
// Windows yet.
func redirectStderr(f *os.File) (restore func(), err error) {
	if f == nil {
		return func() {}, nil
	}
	// Save the original stderr FD so we can restore on exit.
	savedFD, dupErr := syscall.Dup(2)
	if dupErr != nil {
		return func() {}, fmt.Errorf("agora: dup stderr: %w", dupErr)
	}
	if err := syscall.Dup2(int(f.Fd()), 2); err != nil {
		_ = syscall.Close(savedFD)
		return func() {}, fmt.Errorf("agora: dup2 stderr → log: %w", err)
	}
	return func() {
		// Restore the original stderr FD. If restore fails the
		// program is already exiting, so we can only do best-effort.
		_ = syscall.Dup2(savedFD, 2)
		_ = syscall.Close(savedFD)
	}, nil
}
