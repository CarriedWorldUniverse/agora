//go:build windows

package toolrunner

import (
	"os"
	"os/exec"
)

// setProcGroup is a no-op on Windows: the Unix process-group primitives
// (Setpgid + negative-pid kill) don't exist. run_command shells out to
// /bin/sh and is unix-oriented — Windows is a build/CI target for the package,
// not a runtime target for this family. See killProcGroup.
func setProcGroup(cmd *exec.Cmd) {}

// killProcGroup best-effort kills the direct child on Windows (no process-group
// tree kill without a job object). Combined with cmd.WaitDelay this still
// bounds Execute; full descendant cleanup on Windows is out of scope
// (run_command targets unix).
func killProcGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}
