//go:build !windows

package toolrunner

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcGroup puts cmd in its OWN new process group so a negative-pid signal
// reaches every descendant (backgrounded children, nested shells), not just
// the direct /bin/sh child — review fix 1's orphan-on-timeout defect.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcGroup SIGKILLs the whole process group led by cmd (see setProcGroup),
// via a negative-pid kill.
func killProcGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
