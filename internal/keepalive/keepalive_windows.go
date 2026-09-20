//go:build windows

package keepalive

import (
	"context"
	"log"
	"runtime"
	"syscall"
)

const (
	esContinuous      = 0x80000000
	esSystemRequired  = 0x00000001
	esDisplayRequired = 0x00000002
)

var procSetThreadExecutionState = syscall.NewLazyDLL("kernel32.dll").NewProc("SetThreadExecutionState")

// startWindows holds ES_CONTINUOUS | ES_SYSTEM_REQUIRED | ES_DISPLAY_REQUIRED.
// The request is scoped to the calling thread, so a goroutine pins itself to one
// OS thread for the peer's lifetime and clears the flags when it exits.
func startWindows(parent context.Context) *Guard {
	ctx, cancel := context.WithCancel(parent)
	g := &Guard{cancel: cancel, done: make(chan struct{})}
	ready := make(chan bool, 1)
	go func() {
		defer close(g.done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		prev, _, e := procSetThreadExecutionState.Call(esContinuous | esSystemRequired | esDisplayRequired)
		if prev == 0 {
			log.Printf("keepalive: SetThreadExecutionState: %v", e)
			ready <- false
			return
		}
		log.Printf("keepalive: SetThreadExecutionState display+system required")
		ready <- true
		<-ctx.Done()
		_, _, _ = procSetThreadExecutionState.Call(esContinuous)
	}()
	<-ready
	return g
}
