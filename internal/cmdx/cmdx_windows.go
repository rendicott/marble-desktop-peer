//go:build windows

package cmdx

import "os/exec"

// prepare leaves CommandContext's default kill in place. WaitDelay, set by
// Command, still unblocks Wait if a child keeps the pipes open.
func prepare(cmd *exec.Cmd) {}
