//go:build !windows

package keepalive

import "context"

func startWindows(parent context.Context) *Guard {
	g := &Guard{done: make(chan struct{})}
	close(g.done)
	return g
}
