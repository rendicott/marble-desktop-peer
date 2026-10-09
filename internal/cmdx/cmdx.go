// Package cmdx starts subprocesses that a cancelled context can actually
// finish waiting for.
//
// exec.CommandContext kills only the direct child. A shell's grandchild
// (security waiting on a keychain prompt, osascript walking an accessibility
// tree, sleep &) keeps the stdout pipe open, and Cmd.Wait then blocks until
// that grandchild exits. The peer runs actions on a depth-1 queue, so one
// such wait wedges every later computer_* call while /status stays healthy.
package cmdx

import (
	"context"
	"os/exec"
	"time"
)

// WaitDelay is how long Wait may block on a child that ignores cancel or on
// pipes an orphaned grandchild still holds. After this, the pipes are closed
// and Wait returns.
const WaitDelay = 3 * time.Second

// Command is exec.CommandContext with WaitDelay set and, on unix, a cancel
// that kills the process group rather than only the direct child.
func Command(ctx context.Context, name string, arg ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arg...)
	cmd.WaitDelay = WaitDelay
	prepare(cmd)
	return cmd
}
