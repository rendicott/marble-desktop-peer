//go:build unix

package cmdx

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// prepare makes cmd the leader of a new process group and kills that group
// when the context is cancelled, so grandchildren die with the shell.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		proc := cmd.Process
		if proc == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-proc.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		if err != nil {
			return proc.Kill()
		}
		return nil
	}
}
