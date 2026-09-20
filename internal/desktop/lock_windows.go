//go:build windows

package desktop

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

var (
	procOpenInputDesktop          = user32.NewProc("OpenInputDesktop")
	procCloseDesktop              = user32.NewProc("CloseDesktop")
	procGetUserObjectInformationW = user32.NewProc("GetUserObjectInformationW")
)

// queryLockWindows inspects the desktop that currently receives input. A locked
// workstation, the UAC prompt and the login screen all run on a secure desktop
// ("Winlogon") that a normal user process cannot open or capture.
func queryLockWindows(ctx context.Context) LockState {
	_ = ctx
	const desktopReadObjects = 0x0001
	const uoiName = 2
	h, _, e := procOpenInputDesktop.Call(0, 0, desktopReadObjects)
	if h == 0 {
		return LockState{Locked: true, Source: "desktop",
			Detail: fmt.Sprintf("OpenInputDesktop: %v (secure desktop: locked, UAC prompt, or disconnected session)", e)}
	}
	defer procCloseDesktop.Call(h)

	var name [128]uint16
	var need uint32
	r, _, _ := procGetUserObjectInformationW.Call(h, uoiName,
		uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)*2), uintptr(unsafe.Pointer(&need)))
	if r == 0 {
		return LockState{Locked: false, Source: "unknown", Detail: "could not query"}
	}
	n := syscall.UTF16ToString(name[:])
	if strings.EqualFold(n, "Default") {
		return LockState{Locked: false, Source: "desktop", Detail: "input desktop=Default"}
	}
	return LockState{Locked: true, Source: "desktop", Detail: "input desktop=" + n}
}
