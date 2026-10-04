package app

import (
	"fmt"
	"log"
	"time"
)

// The peer lock: at most one paired harness drives this machine at a time.
// A protocol-v2 harness sends {"type":"lock","kind":"acquire"} before its first
// action and "release" when done. The lock survives a WebSocket reconnect from the
// same harness process (network blip) but is dropped when that harness comes back
// with a new instance_id (it restarted, so whatever held the lock is gone).
// If a harness never releases, the operator clears it from the tray / mini UI.
//
// A v1 harness knows nothing about locks: its first action takes the lock
// implicitly, and the lock is dropped on disconnect or after implicitLockIdle
// without an action.

const implicitLockIdle = 2 * time.Minute

// LockInfo is the lock as shown in status.json, the tray and lock_state messages.
type LockInfo struct {
	Held       bool       `json:"held"`
	Holder     string     `json:"holder,omitempty"`      // harness URL
	HolderName string     `json:"holder_name,omitempty"` // harness_name from hello_ack
	Since      *time.Time `json:"since,omitempty"`
	Implicit   bool       `json:"implicit,omitempty"` // taken by a v1 harness's first action
}

func (a *App) lockInfoLocked() LockInfo {
	if a.lockHolder == nil {
		return LockInfo{}
	}
	since := a.lockSince
	return LockInfo{
		Held:       true,
		Holder:     a.lockHolder.url,
		HolderName: a.lockHolder.harnessName(),
		Since:      &since,
		Implicit:   a.lockImplicit,
	}
}

// LockInfo returns the current lock holder.
func (a *App) LockInfo() LockInfo {
	a.lockMu.Lock()
	defer a.lockMu.Unlock()
	return a.lockInfoLocked()
}

func lockedErr(info LockInfo) error {
	who := info.Holder
	if info.HolderName != "" {
		who = info.HolderName + " (" + info.Holder + ")"
	}
	since := ""
	if info.Since != nil {
		since = " since " + info.Since.Local().Format("15:04:05")
	}
	return fmt.Errorf("peer is locked by harness %s%s — wait for it to release, or clear the lock from the marble-peer tray", who, since)
}

// acquireLock gives the lock to l if it is free (or already l's).
func (a *App) acquireLock(l *link, implicit bool) error {
	a.lockMu.Lock()
	if a.lockHolder != nil && a.lockHolder != l {
		if a.lockImplicit && time.Since(a.lockLastUse) > implicitLockIdle {
			log.Printf("lock: implicit lock of %s idle %s — releasing", a.lockHolder.url, implicitLockIdle)
			a.lockHolder = nil
		} else {
			err := lockedErr(a.lockInfoLocked())
			a.lockMu.Unlock()
			return err
		}
	}
	changed := a.lockHolder == nil
	if changed {
		a.lockHolder = l
		a.lockSince = time.Now()
		a.lockInstance = l.instanceID()
		a.lockImplicit = implicit
	} else if !implicit {
		// v2 harness explicitly acquiring what it already holds implicitly.
		a.lockImplicit = false
	}
	a.lockLastUse = time.Now()
	a.lockMu.Unlock()
	if changed {
		log.Printf("lock: acquired by %s implicit=%v", l.url, implicit)
		a.broadcastLock()
	}
	return nil
}

// releaseLock drops the lock if l holds it.
func (a *App) releaseLock(l *link, reason string) {
	a.lockMu.Lock()
	if a.lockHolder != l {
		a.lockMu.Unlock()
		return
	}
	a.lockHolder = nil
	a.lockMu.Unlock()
	log.Printf("lock: released by %s (%s)", l.url, reason)
	a.broadcastLock()
}

// ClearLock force-releases the lock (tray / mini UI) and stops any in-flight
// action so the old holder cannot keep driving the machine. Returns the lock
// as it was before clearing.
func (a *App) ClearLock() LockInfo {
	a.lockMu.Lock()
	prev := a.lockInfoLocked()
	a.lockHolder = nil
	a.lockMu.Unlock()
	if prev.Held {
		log.Printf("lock: cleared by operator (was %s)", prev.Holder)
		a.Q.Cancel()
		a.broadcastLock()
	}
	return prev
}

// authorizeAction reports whether l may run an action now. Legacy (v1) harnesses
// take the lock implicitly.
func (a *App) authorizeAction(l *link) error {
	if l.legacy() {
		return a.acquireLock(l, true)
	}
	a.lockMu.Lock()
	defer a.lockMu.Unlock()
	if a.lockHolder == l {
		a.lockLastUse = time.Now()
		return nil
	}
	if a.lockHolder == nil {
		return fmt.Errorf("peer lock not held — harness must acquire the lock before sending actions")
	}
	return lockedErr(a.lockInfoLocked())
}

// lockHeldByOrFree reports whether l may cancel the running action.
func (a *App) lockHeldByOrFree(l *link) bool {
	a.lockMu.Lock()
	defer a.lockMu.Unlock()
	return a.lockHolder == nil || a.lockHolder == l
}

// onHarnessHello drops a lock held across a harness restart.
func (a *App) onHarnessHello(l *link) {
	a.lockMu.Lock()
	stale := a.lockHolder == l && a.lockInstance != l.instanceID()
	a.lockMu.Unlock()
	if stale {
		a.releaseLock(l, "harness restarted")
		return
	}
	l.sendLockState(a.LockInfo())
}

// onHarnessDisconnect drops an implicit (v1) lock; v2 locks survive reconnects.
func (a *App) onHarnessDisconnect(l *link) {
	a.lockMu.Lock()
	implicit := a.lockHolder == l && a.lockImplicit
	a.lockMu.Unlock()
	if implicit {
		a.releaseLock(l, "legacy harness disconnected")
	}
}

func (a *App) broadcastLock() {
	info := a.LockInfo()
	for _, l := range a.Links() {
		l.sendLockState(info)
	}
}
