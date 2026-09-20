//go:build !windows

package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func tryFlock() (*os.File, error) {
	f, err := os.OpenFile(LockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	// Write PID only after we own the lock (do not truncate until then).
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Sync()
	return f, nil
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Signal 0 checks existence without killing.
	err := syscall.Kill(pid, 0)
	return err == nil
}

func staleLockHint() string {
	return fmt.Sprintf("rm -f %s && pkill -x marble-peer; marble-peer run", LockPath())
}

func runningHint(pids []int) string {
	parts := make([]string, len(pids))
	for i, p := range pids {
		parts[i] = strconv.Itoa(p)
	}
	list := strings.Join(parts, " ")
	return fmt.Sprintf("  stop it:  kill %s\n  or:       pkill -x marble-peer\n  force:    kill -9 %s; rm -f %s",
		list, list, LockPath())
}
