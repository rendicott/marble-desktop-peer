//go:build windows

package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	stillActiveExit  = 259        // STILL_ACTIVE
	lockExclusive    = 0x2        // LOCKFILE_EXCLUSIVE_LOCK
	lockFailImmed    = 0x1        // LOCKFILE_FAIL_IMMEDIATELY
	lockRegionOffset = 0x7fff0000 // byte range locked, far past the PID text
)

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx = kernel32.NewProc("LockFileEx")
)

// tryFlock takes an exclusive byte-range lock far past the PID text. Windows
// range locks are mandatory, so locking byte 0 would stop the loser from
// reading the winner's PID for its error message; a range beyond EOF is legal
// and leaves the PID text readable.
func tryFlock() (*os.File, error) {
	f, err := os.OpenFile(LockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var ov syscall.Overlapped
	ov.Offset = lockRegionOffset
	r, _, e := procLockFileEx.Call(
		f.Fd(),
		lockExclusive|lockFailImmed,
		0,
		1, 0,
		uintptr(unsafe.Pointer(&ov)),
	)
	if r == 0 {
		_ = f.Close()
		return nil, e
	}
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
	const processQueryLimited = 0x1000
	h, err := syscall.OpenProcess(processQueryLimited, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActiveExit
}

func staleLockHint() string {
	return fmt.Sprintf(`del "%s" & taskkill /F /IM marble-peer.exe & marble-peer run`, LockPath())
}

func runningHint(pids []int) string {
	var b strings.Builder
	b.WriteString("  stop it:  taskkill /F")
	for _, p := range pids {
		b.WriteString(" /PID " + strconv.Itoa(p))
	}
	b.WriteString("\n  or:       taskkill /F /IM marble-peer.exe")
	return b.String()
}
