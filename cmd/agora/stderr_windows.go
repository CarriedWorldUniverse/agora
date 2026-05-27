//go:build windows

package main

import "os"

// redirectStderr is a no-op on Windows. The unix implementation uses
// syscall.Dup/Dup2 which aren't available on Windows in the same
// shape; the operator hasn't reported the "panic-to-stderr trashes
// alt-screen" symptom on Windows yet, so this is left as a
// best-effort stub. Refactor with platform-appropriate FD redirection
// (windows-specific stdhandle manipulation via Kernel32) if it
// becomes a real symptom.
func redirectStderr(_ *os.File) (restore func(), err error) {
	return func() {}, nil
}
