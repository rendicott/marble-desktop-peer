//go:build !windows

package desktop

import "fmt"

// RunSessionWorker is the Windows session-0 helper. Other platforms have no
// session 0 and no worker.
func RunSessionWorker() error {
	return fmt.Errorf("desktop session worker is windows-only")
}
