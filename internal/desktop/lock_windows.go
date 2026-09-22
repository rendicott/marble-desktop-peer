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
	procGetProcessWindowStation   = user32.NewProc("GetProcessWindowStation")

	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGetCurrentProcessId  = kernel32.NewProc("GetCurrentProcessId")
	procProcessIdToSessionId = kernel32.NewProc("ProcessIdToSessionId")
)

// wsfVisible is USEROBJECTFLAGS.dwFlags' bit for an interactive window
// station (set on "WinSta0"; unset on the non-interactive station services —
// and anything spawned by them, including a plain SSH shell — run under).
const wsfVisible = 0x0001
const uoiFlags = 1

type userObjectFlags struct {
	fInherit  int32
	fReserved int32
	dwFlags   uint32
}

// nonInteractiveSession detects the case Windows OpenSSH hits by default: the
// server (and any shell it spawns) runs in session 0, the non-interactive
// "Services" session, which has no desktop a GUI can draw on or receive input
// from. That is a structural limitation, not a locked/secure-desktop state —
// OpenInputDesktop, BitBlt and SendInput all fail here no matter what account
// runs the process. Distinguishing it from a genuinely locked workstation lets
// doctor point at the real fix (run from an interactive session) instead of
// "unlock the screen".
func nonInteractiveSession() (bool, string) {
	pid, _, _ := procGetCurrentProcessId.Call()
	var sessionID uint32
	if r, _, _ := procProcessIdToSessionId.Call(pid, uintptr(unsafe.Pointer(&sessionID))); r != 0 && sessionID == 0 {
		return true, "session 0 (the non-interactive Services session — where a plain SSH shell on Windows typically lands)"
	}
	hwinsta, _, _ := procGetProcessWindowStation.Call()
	if hwinsta == 0 {
		return false, ""
	}
	var flags userObjectFlags
	var need uint32
	r, _, _ := procGetUserObjectInformationW.Call(hwinsta, uoiFlags,
		uintptr(unsafe.Pointer(&flags)), unsafe.Sizeof(flags), uintptr(unsafe.Pointer(&need)))
	if r != 0 && flags.dwFlags&wsfVisible == 0 {
		return true, "a non-interactive window station"
	}
	return false, ""
}

// queryLockWindows inspects the desktop that currently receives input. A locked
// workstation, the UAC prompt and the login screen all run on a secure desktop
// ("Winlogon") that a normal user process cannot open or capture. Running with
// no interactive window station at all (e.g. over plain SSH, landing in
// session 0) looks similar but needs a different fix — see nonInteractiveSession.
func queryLockWindows(ctx context.Context) LockState {
	_ = ctx
	if yes, why := nonInteractiveSession(); yes {
		return LockState{Locked: true, Source: "session",
			Detail: fmt.Sprintf("running in %s — there is no interactive desktop to screenshot or send input to. "+
				"Launch marble-peer from an interactive session (console/RDP) or, for remote/headless setup, "+
				"a scheduled task run with `schtasks /create ... /it` (interactive session 1). "+
				"Plain SSH alone cannot run the desktop features.", why)}
	}
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
