//go:build !windows

package desktop

import "context"

func queryLockWindows(ctx context.Context) LockState {
	_ = ctx
	return LockState{Locked: false, Source: "unknown", Detail: "not windows"}
}
