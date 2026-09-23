// Package shellexec runs a command on the peer machine and captures its
// output as text (stdout, stderr, exit code).
//
// Why this exists: before this, the only way to inspect peer state (files,
// logs, installed software, env, config) was screenshot -> click a terminal
// -> type a command -> screenshot again -> read pixels. Any output that
// scrolled off a ~40-line PowerShell window, or that produced no visible
// output at all, was indistinguishable from a command that never ran. See
// field report "peer-gui-loop-report" (2026-09-23): a ~150-iteration loop
// retyping the same command because its result could never be confirmed.
package shellexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// MaxOutputBytes caps stdout and stderr independently — keeps the websocket
// envelope bounded even if a command floods output (e.g. a recursive listing
// of C:\).
const MaxOutputBytes = 64 * 1024

// DefaultTimeout and MaxTimeout bound how long a command may run. The caller
// (app.exec's handleAction) already bounds the whole action by env.DeadlineMS
// (default 120s, hard cap 5m) — these are a second, per-exec-call clamp so a
// caller that forgets timeout_sec still gets a sane default.
const (
	DefaultTimeout = 60 * time.Second
	MaxTimeout     = 5 * time.Minute
)

// Result is the outcome of a peer-side command execution.
type Result struct {
	Shell     string `json:"shell"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
}

// Run executes command in a platform shell (powershell.exe on Windows, bash/sh
// elsewhere) and captures stdout/stderr separately. A non-zero exit code is a
// normal result (err is nil); err is only returned for transport-level
// failures (couldn't start, or the context deadline was hit).
func Run(ctx context.Context, command, cwd string, timeoutSec int) (Result, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return Result{}, errors.New("computer_exec: command required")
	}
	timeout := DefaultTimeout
	if timeoutSec > 0 {
		timeout = time.Duration(timeoutSec) * time.Second
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name, args, shell := shellCommand(command)
	cmd := exec.CommandContext(cctx, name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	out, otrunc := capText(stdout.Bytes())
	errOut, etrunc := capText(stderr.Bytes())
	res := Result{Shell: shell, Stdout: out, Stderr: errOut, Truncated: otrunc || etrunc}

	if cctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res, fmt.Errorf("computer_exec: command timed out after %s", timeout)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil // non-zero exit is a normal result, not a transport error
	}
	if err != nil {
		return res, fmt.Errorf("computer_exec: %w", err)
	}
	return res, nil
}

// shellCommand picks the platform shell. Windows PowerShell 5.1 is present on
// every supported Windows release; -NoProfile/-NonInteractive keep it
// non-interactive and fast (mirrors runPowerShell in internal/browser).
func shellCommand(command string) (name string, args []string, shell string) {
	if runtime.GOOS == "windows" {
		return "powershell.exe",
			[]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command},
			"powershell"
	}
	sh := "/bin/sh"
	if p, err := exec.LookPath("bash"); err == nil {
		sh = p
	}
	return sh, []string{"-c", command}, sh
}

func capText(b []byte) (string, bool) {
	if len(b) <= MaxOutputBytes {
		return string(b), false
	}
	return string(b[:MaxOutputBytes]) + "\n…[truncated]", true
}
