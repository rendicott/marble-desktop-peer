//go:build windows

package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/rendicott/marble-desktop-peer/internal/config"
)

// Session-0 attach.
//
// A process in Windows session 0 (services, SSM, user-data) cannot BitBlt or
// SendInput against the interactive desktop: both are bound to the process's
// session, and SetThreadDesktop does not change it. When — and only when —
// this process is non-interactive and attach is enabled, desktop operations
// run in a helper process of this same binary, created with the logged-on
// user's token on winsta0\default. An interactive user session never reaches
// this file's process-creation code.
//
// The helper binds its capture thread to that desktop before any GDI call.
// lpDesktop names it for the process's initial thread; this repeats the bind
// on the thread that actually calls BitBlt, and a helper that is still in
// session 0 refuses the operation instead of reporting a capturable desktop.

const (
	createNoWindow                = 0x08000000
	extendedStartupInfoPresent    = 0x00080000
	procThreadAttributeHandleList = 0x00020002
	stillActive                   = 259
	errorNoToken                  = syscall.Errno(1008)
	errorNotAllAssigned           = syscall.Errno(1300)
	securityImpersonation         = 2
	tokenPrimaryType              = 1
	maximumAllowed                = 0x02000000
	sePrivilegeEnabled            = 0x00000002
	tokenSessionIDClass           = 12
	wtsUserNameClass              = 5
)

var (
	wtsapi32 = syscall.NewLazyDLL("wtsapi32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")

	procWTSGetActiveConsoleSessionId      = kernel32.NewProc("WTSGetActiveConsoleSessionId")
	procWTSEnumerateSessionsW             = wtsapi32.NewProc("WTSEnumerateSessionsW")
	procWTSQueryUserToken                 = wtsapi32.NewProc("WTSQueryUserToken")
	procWTSQuerySessionInformationW       = wtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory                     = wtsapi32.NewProc("WTSFreeMemory")
	procDuplicateTokenEx                  = advapi32.NewProc("DuplicateTokenEx")
	procSetTokenInformation               = advapi32.NewProc("SetTokenInformation")
	procLookupPrivilegeValueW             = advapi32.NewProc("LookupPrivilegeValueW")
	procAdjustTokenPrivileges             = advapi32.NewProc("AdjustTokenPrivileges")
	procAllocateAndInitializeSid          = advapi32.NewProc("AllocateAndInitializeSid")
	procCheckTokenMembership              = advapi32.NewProc("CheckTokenMembership")
	procFreeSid                           = advapi32.NewProc("FreeSid")
	procGetUserNameW                      = advapi32.NewProc("GetUserNameW")
	procInitializeProcThreadAttributeList = kernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute         = kernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttributeList     = kernel32.NewProc("DeleteProcThreadAttributeList")
	procGetExitCodeProcess                = kernel32.NewProc("GetExitCodeProcess")
	procTerminateProcess                  = kernel32.NewProc("TerminateProcess")
	procWaitForSingleObject               = kernel32.NewProc("WaitForSingleObject")
	procGetCurrentThreadId                = kernel32.NewProc("GetCurrentThreadId")

	procGetThreadDesktop        = user32.NewProc("GetThreadDesktop")
	procOpenWindowStationW      = user32.NewProc("OpenWindowStationW")
	procSetProcessWindowStation = user32.NewProc("SetProcessWindowStation")
	procCloseWindowStation      = user32.NewProc("CloseWindowStation")
	procOpenDesktopW            = user32.NewProc("OpenDesktopW")
	procSetThreadDesktop        = user32.NewProc("SetThreadDesktop")
)

// Window-station and desktop rights the helper needs to select winsta0\default
// and then BitBlt it. WINSTA_ALL_ACCESS is 0x37F; the desktop rights through
// DESKTOP_SWITCHDESKTOP are 0x01FF.
const (
	winstaAllAccess  = 0x37F
	desktopAllAccess = 0x01FF
	uoiNameInfo      = 2
)

// Held for the worker's lifetime. Closing the window station or desktop that
// the process is bound to drops the connection.
var (
	workerWinSta syscall.Handle
	workerDesk   syscall.Handle
)

// startupInfoEx is STARTUPINFOEXW. syscall.StartupInfo matches STARTUPINFOW;
// the attribute-list pointer sits immediately after it. Cb is sizeof the whole
// value so CreateProcessAsUser reads the list when EXTENDED_STARTUPINFO_PRESENT
// is set. That list is what stops the child inheriting every SYSTEM handle.
type startupInfoEx struct {
	syscall.StartupInfo
	attrList uintptr
}

type wtsSessionInfo struct {
	SessionID uint32
	_         uint32
	Name      *uint16
	State     uint32
	_         uint32
}

type luid struct {
	Low  uint32
	High int32
}

type luidAndAttr struct {
	Luid luid
	Attr uint32
}

type tokenPrivs struct {
	Count uint32
	Privs [1]luidAndAttr
}

type workerReq struct {
	ID     int    `json:"id"`
	Op     string `json:"op"`
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	Button string `json:"button,omitempty"`
	Text   string `json:"text,omitempty"`
	Key    string `json:"key,omitempty"`
}

type workerResp struct {
	ID     int    `json:"id"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	W      int    `json:"w,omitempty"`
	H      int    `json:"h,omitempty"`
	PNG    string `json:"png,omitempty"`
	Title  string `json:"title,omitempty"`
	App    string `json:"app,omitempty"`
	Locked bool   `json:"locked,omitempty"`
	Source string `json:"source,omitempty"`
	Detail string `json:"detail,omitempty"`
}

var (
	localSystemOnce sync.Once
	localSystemYes  bool
	tcbOnce         sync.Once
	deskBridge      sessionBridge
)

func session0AttachEnabled() bool {
	var cfg *bool
	if f, err := config.Load(); err == nil {
		cfg = f.Session0Desktop
	}
	return session0AttachDecision(os.Getenv("MARBLE_PEER_SESSION0_DESKTOP"), cfg, runningAsLocalSystem())
}

func runningAsLocalSystem() bool {
	localSystemOnce.Do(func() { localSystemYes = detectLocalSystem() })
	return localSystemYes
}

func detectLocalSystem() bool {
	if tokenIsLocalSystem() {
		return true
	}
	buf := make([]uint16, 256)
	n := uint32(len(buf))
	r, _, _ := procGetUserNameW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return false
	}
	return strings.EqualFold(syscall.UTF16ToString(buf), "SYSTEM")
}

func tokenIsLocalSystem() bool {
	// S-1-5-18 (NT AUTHORITY\SYSTEM). Checking the process token needs no
	// privilege a user process doesn't already have.
	var auth struct{ Value [6]byte }
	auth.Value[5] = 5 // SECURITY_NT_AUTHORITY
	var sid uintptr
	r, _, _ := procAllocateAndInitializeSid.Call(
		uintptr(unsafe.Pointer(&auth)),
		1,
		18, // SECURITY_LOCAL_SYSTEM_RID
		0, 0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&sid)),
	)
	if r == 0 || sid == 0 {
		return false
	}
	defer procFreeSid.Call(sid)
	var member int32
	r, _, _ = procCheckTokenMembership.Call(0, sid, uintptr(unsafe.Pointer(&member)))
	return r != 0 && member != 0
}

func ensureTCB() {
	tcbOnce.Do(func() {
		if err := enablePrivilege("SeTcbPrivilege"); err != nil {
			log.Printf("desktop: SeTcbPrivilege: %v", err)
		}
	})
}

func enablePrivilege(name string) error {
	proc, err := syscall.GetCurrentProcess()
	if err != nil {
		return err
	}
	var tok syscall.Token
	if err := syscall.OpenProcessToken(proc, syscall.TOKEN_ADJUST_PRIVILEGES|syscall.TOKEN_QUERY, &tok); err != nil {
		return err
	}
	defer syscall.CloseHandle(syscall.Handle(tok))
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var id luid
	r, _, e := procLookupPrivilegeValueW.Call(0, uintptr(unsafe.Pointer(namePtr)), uintptr(unsafe.Pointer(&id)))
	if r == 0 {
		return e
	}
	tp := tokenPrivs{Count: 1, Privs: [1]luidAndAttr{{Luid: id, Attr: sePrivilegeEnabled}}}
	r, _, e = procAdjustTokenPrivileges.Call(uintptr(tok), 0, uintptr(unsafe.Pointer(&tp)), 0, 0, 0)
	if r == 0 {
		return e
	}
	if e == errorNotAllAssigned {
		return fmt.Errorf("%s is not held by this process", name)
	}
	return nil
}

// openAttachToken resolves the logged-on session and returns a primary token
// for it. The caller closes the token.
func openAttachToken() (attachSession, syscall.Token, error) {
	if unsafe.Sizeof(wtsSessionInfo{}) != 24 || unsafe.Sizeof(uintptr(0)) != 8 {
		return attachSession{}, 0, errors.New("attach denied: unexpected WTS_SESSION_INFO layout")
	}
	ensureTCB()
	console, _, _ := procWTSGetActiveConsoleSessionId.Call()
	var raw unsafe.Pointer
	var count uint32
	r, _, e := procWTSEnumerateSessionsW.Call(0, 0, 1, uintptr(unsafe.Pointer(&raw)), uintptr(unsafe.Pointer(&count)))
	if r == 0 || raw == nil {
		if e == nil || e == syscall.Errno(0) {
			e = syscall.EINVAL
		}
		return attachSession{}, 0, fmt.Errorf("attach denied: enumerate sessions: %v", e)
	}
	defer procWTSFreeMemory.Call(uintptr(raw))

	var sessions []attachSession
	var deniedID uint32
	var deniedErr error
	stride := unsafe.Sizeof(wtsSessionInfo{})
	for i := uint32(0); i < count; i++ {
		si := (*wtsSessionInfo)(unsafe.Add(raw, uintptr(i)*stride))
		if si.SessionID == 0 {
			continue
		}
		var tok syscall.Handle
		tr, _, te := procWTSQueryUserToken.Call(uintptr(si.SessionID), uintptr(unsafe.Pointer(&tok)))
		if tr == 0 {
			if te == errorNoToken {
				continue
			}
			if deniedErr == nil {
				deniedID = si.SessionID
				deniedErr = te
			}
			continue
		}
		syscall.CloseHandle(tok)
		sessions = append(sessions, attachSession{
			ID:    si.SessionID,
			State: si.State,
			User:  sessionUsername(si.SessionID),
		})
	}
	picked, ok := pickAttachSession(uint32(console), sessions)
	if !ok {
		if deniedErr != nil {
			return attachSession{}, 0, errors.New(attachDeniedNote(deniedID, deniedErr))
		}
		return attachSession{}, 0, errors.New(noInteractiveSessionNote)
	}
	dup, err := duplicateSessionToken(picked.ID)
	if err != nil {
		return attachSession{}, 0, errors.New(attachDeniedNote(picked.ID, err))
	}
	return picked, dup, nil
}

func duplicateSessionToken(sessionID uint32) (syscall.Token, error) {
	var tok syscall.Handle
	r, _, e := procWTSQueryUserToken.Call(uintptr(sessionID), uintptr(unsafe.Pointer(&tok)))
	if r == 0 {
		if e == nil || e == syscall.Errno(0) {
			e = syscall.EACCES
		}
		return 0, e
	}
	defer syscall.CloseHandle(tok)
	var dup syscall.Handle
	r, _, e = procDuplicateTokenEx.Call(
		uintptr(tok),
		maximumAllowed,
		0,
		securityImpersonation,
		tokenPrimaryType,
		uintptr(unsafe.Pointer(&dup)),
	)
	if r == 0 {
		if e == nil || e == syscall.Errno(0) {
			e = syscall.EACCES
		}
		return 0, e
	}
	sid := sessionID
	// Best-effort: a duplicated token keeps the session, but set it explicitly
	// so CreateProcessAsUser lands in the interactive session.
	_, _, _ = procSetTokenInformation.Call(uintptr(dup), tokenSessionIDClass, uintptr(unsafe.Pointer(&sid)), unsafe.Sizeof(sid))
	return syscall.Token(dup), nil
}

func sessionUsername(id uint32) string {
	var buf *uint16
	var n uint32
	r, _, _ := procWTSQuerySessionInformationW.Call(0, uintptr(id), wtsUserNameClass, uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n)))
	if r == 0 || buf == nil || n < 2 {
		return ""
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf)))
	chars := n / 2
	s := unsafe.Slice(buf, chars)
	return trimSpace(syscall.UTF16ToString(s))
}

func trimSpace(s string) string {
	i := 0
	j := len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\n' || s[j-1] == '\r' || s[j-1] == 0) {
		j--
	}
	return s[i:j]
}

type sessionBridge struct {
	mu      sync.Mutex
	hProc   syscall.Handle
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	dec     *json.Decoder
	session attachSession
	nextID  int
}

func (b *sessionBridge) call(ctx context.Context, req workerReq) (workerResp, attachSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	resp, sess, err := b.roundLocked(ctx, req, true)
	return resp, sess, err
}

func (b *sessionBridge) roundLocked(ctx context.Context, req workerReq, retry bool) (workerResp, attachSession, error) {
	if err := b.ensureLocked(); err != nil {
		return workerResp{}, attachSession{}, err
	}
	sess := b.session
	b.nextID++
	req.ID = b.nextID
	if err := json.NewEncoder(b.stdin).Encode(req); err != nil {
		b.stopLocked()
		if retry && ctx.Err() == nil {
			return b.roundLocked(ctx, req, false)
		}
		return workerResp{}, sess, err
	}
	type result struct {
		resp workerResp
		err  error
	}
	ch := make(chan result, 1)
	dec := b.dec
	go func() {
		var resp workerResp
		err := dec.Decode(&resp)
		ch <- result{resp, err}
	}()
	select {
	case <-ctx.Done():
		b.stopLocked()
		return workerResp{}, sess, ctx.Err()
	case r := <-ch:
		if r.err != nil || r.resp.ID != req.ID {
			b.stopLocked()
			if retry && ctx.Err() == nil {
				return b.roundLocked(ctx, req, false)
			}
			if r.err == nil {
				r.err = errors.New("desktop worker protocol desync")
			}
			return workerResp{}, sess, r.err
		}
		return r.resp, sess, nil
	}
}

func (b *sessionBridge) ensureLocked() error {
	target, token, err := openAttachToken()
	if err != nil {
		// The logged-on session went away. Don't leave a helper in it.
		b.stopLocked()
		return err
	}
	if b.alive() && b.session.ID == target.ID {
		syscall.CloseHandle(syscall.Handle(token))
		b.session.User = target.User
		return nil
	}
	b.stopLocked()
	return b.startLocked(target, token)
}

func (b *sessionBridge) alive() bool {
	if b.hProc == 0 {
		return false
	}
	var code uint32
	r, _, _ := procGetExitCodeProcess.Call(uintptr(b.hProc), uintptr(unsafe.Pointer(&code)))
	return r != 0 && code == stillActive
}

func (b *sessionBridge) stopLocked() {
	if b.hProc != 0 {
		_, _, _ = procTerminateProcess.Call(uintptr(b.hProc), 1)
		_, _, _ = procWaitForSingleObject.Call(uintptr(b.hProc), 5000)
		_ = syscall.CloseHandle(b.hProc)
		b.hProc = 0
	}
	if b.stdin != nil {
		_ = b.stdin.Close()
		b.stdin = nil
	}
	if b.stdout != nil {
		_ = b.stdout.Close()
		b.stdout = nil
	}
	if b.stderr != nil {
		_ = b.stderr.Close()
		b.stderr = nil
	}
	b.dec = nil
	b.session = attachSession{}
}

func (b *sessionBridge) startLocked(target attachSession, token syscall.Token) error {
	defer syscall.CloseHandle(syscall.Handle(token))
	inR, inW, err := inheritablePipe()
	if err != nil {
		return errors.New(attachDeniedNote(target.ID, err))
	}
	outR, outW, err := inheritablePipe()
	if err != nil {
		closeHandles(inR, inW)
		return errors.New(attachDeniedNote(target.ID, err))
	}
	errR, errW, err := inheritablePipe()
	if err != nil {
		closeHandles(inR, inW, outR, outW)
		return errors.New(attachDeniedNote(target.ID, err))
	}
	exe, err := os.Executable()
	if err != nil {
		closeHandles(inR, inW, outR, outW, errR, errW)
		return errors.New(attachDeniedNote(target.ID, err))
	}
	pi, err := createSessionProcess(token, exe, [3]syscall.Handle{inR, outW, errW})
	// The child has its own copies. Closing these lets the child see EOF when
	// the parent later closes its ends.
	closeHandles(inR, outW, errW)
	if err != nil {
		closeHandles(inW, outR, errR)
		return errors.New(attachDeniedNote(target.ID, err))
	}
	_ = syscall.CloseHandle(pi.Thread)
	b.hProc = pi.Process
	b.stdin = os.NewFile(uintptr(inW), "session-worker-stdin")
	b.stdout = os.NewFile(uintptr(outR), "session-worker-stdout")
	b.stderr = os.NewFile(uintptr(errR), "session-worker-stderr")
	b.dec = json.NewDecoder(b.stdout)
	b.session = target
	go drainWorkerStderr(b.stderr)
	log.Printf("desktop: helper started in session %d (%s) on winsta0\\default", target.ID, target.User)
	return nil
}

func inheritablePipe() (r, w syscall.Handle, err error) {
	sa := syscall.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(syscall.SecurityAttributes{})),
		InheritHandle: 1,
	}
	err = syscall.CreatePipe(&r, &w, &sa, 0)
	runtime.KeepAlive(&sa)
	return r, w, err
}

func closeHandles(hs ...syscall.Handle) {
	for _, h := range hs {
		if h != 0 {
			_ = syscall.CloseHandle(h)
		}
	}
}

func createSessionProcess(token syscall.Token, exe string, child [3]syscall.Handle) (syscall.ProcessInformation, error) {
	var zero syscall.ProcessInformation
	app, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		return zero, err
	}
	cmdRaw, err := syscall.UTF16FromString(quoteWinArg(exe) + " desktop-worker")
	if err != nil {
		return zero, err
	}
	desk, err := syscall.UTF16PtrFromString(`winsta0\default`)
	if err != nil {
		return zero, err
	}
	list, listKeep, err := newHandleList(child[:])
	if err != nil {
		return zero, err
	}
	defer procDeleteProcThreadAttributeList.Call(list)

	ex := startupInfoEx{}
	ex.Cb = uint32(unsafe.Sizeof(ex))
	ex.Desktop = desk
	ex.Flags = syscall.STARTF_USESTDHANDLES | syscall.STARTF_USESHOWWINDOW
	ex.ShowWindow = syscall.SW_HIDE
	ex.StdInput = child[0]
	ex.StdOutput = child[1]
	ex.StdErr = child[2]
	ex.attrList = list

	var pi syscall.ProcessInformation
	err = syscall.CreateProcessAsUser(
		token,
		app,
		&cmdRaw[0],
		nil, nil,
		true,
		createNoWindow|extendedStartupInfoPresent,
		nil, nil,
		&ex.StartupInfo,
		&pi,
	)
	runtime.KeepAlive(cmdRaw)
	runtime.KeepAlive(ex)
	runtime.KeepAlive(listKeep)
	runtime.KeepAlive(child)
	if err != nil {
		return zero, err
	}
	return pi, nil
}

// newHandleList builds a PROC_THREAD_ATTRIBUTE_LIST containing only handles,
// so the user-session worker does not inherit the SYSTEM process's other handles.
func newHandleList(handles []syscall.Handle) (uintptr, []uint64, error) {
	var size uintptr
	_, _, e := procInitializeProcThreadAttributeList.Call(0, 1, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		if e == nil || e == syscall.Errno(0) {
			e = syscall.EINVAL
		}
		return 0, nil, e
	}
	buf := make([]uint64, (size+7)/8)
	ptr := uintptr(unsafe.Pointer(&buf[0]))
	r, _, e := procInitializeProcThreadAttributeList.Call(ptr, 1, 0, uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return 0, nil, e
	}
	r, _, e = procUpdateProcThreadAttribute.Call(
		ptr, 0,
		procThreadAttributeHandleList,
		uintptr(unsafe.Pointer(&handles[0])),
		uintptr(len(handles))*unsafe.Sizeof(handles[0]),
		0, 0,
	)
	if r == 0 {
		procDeleteProcThreadAttributeList.Call(ptr)
		return 0, nil, e
	}
	return ptr, buf, nil
}

func drainWorkerStderr(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			log.Printf("desktop worker: %s", trimSpace(string(buf[:n])))
		}
		if err != nil {
			return
		}
	}
}

func screenshotSession0(ctx context.Context, out string) (int, int, error) {
	resp, _, err := deskBridge.call(ctx, workerReq{Op: "shot"})
	if err != nil {
		return 0, 0, err
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "screenshot failed"
		}
		return 0, 0, errors.New(resp.Error)
	}
	raw, err := base64.StdEncoding.DecodeString(resp.PNG)
	if err != nil {
		return 0, 0, fmt.Errorf("screenshot: worker image: %w", err)
	}
	if err := os.WriteFile(out, raw, 0o600); err != nil {
		return 0, 0, err
	}
	return resp.W, resp.H, nil
}

func clickSession0(ctx context.Context, x, y int, button string) error {
	return session0Err(ctx, workerReq{Op: "click", X: x, Y: y, Button: button})
}

func typeSession0(ctx context.Context, text string) error {
	return session0Err(ctx, workerReq{Op: "type", Text: text})
}

func keySession0(ctx context.Context, key string) error {
	return session0Err(ctx, workerReq{Op: "key", Key: key})
}

func activeWindowSession0(ctx context.Context) (string, string, error) {
	resp, _, err := deskBridge.call(ctx, workerReq{Op: "window"})
	if err != nil {
		return "", "", err
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "active window failed"
		}
		return "", "", errors.New(resp.Error)
	}
	return resp.Title, resp.App, nil
}

func probeClickSession0(ctx context.Context) error {
	return session0Err(ctx, workerReq{Op: "probe"})
}

func session0Available(ctx context.Context) (bool, string) {
	resp, sess, err := deskBridge.call(ctx, workerReq{Op: "available"})
	if err != nil {
		return false, err.Error()
	}
	if !resp.OK {
		if resp.Error != "" {
			return false, resp.Error
		}
		return false, secureDesktopNote
	}
	note := attachOKNote(sess.ID, sess.User)
	if resp.W > 0 && resp.H > 0 {
		note = fmt.Sprintf("%s, primary display %dx%d px", note, resp.W, resp.H)
	}
	return true, note
}

func queryLockSession0(ctx context.Context) LockState {
	resp, _, err := deskBridge.call(ctx, workerReq{Op: "lock"})
	if err != nil {
		return LockState{Locked: true, Source: "session", Detail: err.Error()}
	}
	if !resp.OK {
		msg := resp.Error
		if msg == "" {
			msg = noInteractiveSessionNote
		}
		return LockState{Locked: true, Source: "session", Detail: msg}
	}
	return LockState{Locked: resp.Locked, Source: resp.Source, Detail: resp.Detail}
}

func session0Err(ctx context.Context, req workerReq) error {
	resp, _, err := deskBridge.call(ctx, req)
	if err != nil {
		return err
	}
	if !resp.OK {
		if resp.Error == "" {
			return errors.New(req.Op + " failed")
		}
		return errors.New(resp.Error)
	}
	return nil
}

// bindWorkerDesktop puts this thread on winsta0\default when it is not already
// there. A helper created with lpDesktop is normally already there; a thread
// that came up on WinDisc (session 0, or a disconnected station) is moved.
// Failure is logged and, when the process is still in session 0, every
// operation is refused by dispatchWorker.
func bindWorkerDesktop() error {
	if workerOnUserDesktop() {
		return nil
	}
	st := windowStationName()
	dk := threadDesktopName()
	wrongName := (st != "" && !strings.EqualFold(st, "WinSta0")) || (dk != "" && !strings.EqualFold(dk, "Default"))
	if !wrongName && !workerInSession0() {
		// Station name could not be read and this is not session 0. Leave the
		// thread where CreateProcessAsUser put it rather than guessing.
		return nil
	}
	if err := switchToInteractiveDesktop(); err != nil {
		return err
	}
	if workerInSession0() {
		return fmt.Errorf("still in session 0 (%s)", capturePlace())
	}
	return nil
}

func switchToInteractiveDesktop() error {
	stationName, err := syscall.UTF16PtrFromString("winsta0")
	if err != nil {
		return err
	}
	hWin, _, e := procOpenWindowStationW.Call(uintptr(unsafe.Pointer(stationName)), 0, winstaAllAccess)
	runtime.KeepAlive(stationName)
	if hWin == 0 {
		return fmt.Errorf("OpenWindowStation(winsta0): %v", callErr(e))
	}
	if r, _, e := procSetProcessWindowStation.Call(hWin); r == 0 {
		procCloseWindowStation.Call(hWin)
		return fmt.Errorf("SetProcessWindowStation: %v", callErr(e))
	}
	workerWinSta = syscall.Handle(hWin)

	deskName, err := syscall.UTF16PtrFromString("default")
	if err != nil {
		return err
	}
	hDesk, _, e := procOpenDesktopW.Call(uintptr(unsafe.Pointer(deskName)), 0, 0, desktopAllAccess)
	runtime.KeepAlive(deskName)
	if hDesk == 0 {
		return fmt.Errorf("OpenDesktop(default): %v", callErr(e))
	}
	if r, _, e := procSetThreadDesktop.Call(hDesk); r == 0 {
		procCloseDesktop.Call(hDesk)
		return fmt.Errorf("SetThreadDesktop: %v", callErr(e))
	}
	workerDesk = syscall.Handle(hDesk)
	return nil
}

func workerOnUserDesktop() bool {
	sid, known := currentSessionID()
	if !known || sid == 0 {
		return false
	}
	return strings.EqualFold(windowStationName(), "WinSta0") && strings.EqualFold(threadDesktopName(), "Default")
}

func workerInSession0() bool {
	sid, known := currentSessionID()
	return known && sid == 0
}

func currentSessionID() (uint32, bool) {
	pid, _, _ := procGetCurrentProcessId.Call()
	var sid uint32
	r, _, _ := procProcessIdToSessionId.Call(pid, uintptr(unsafe.Pointer(&sid)))
	if r == 0 {
		return 0, false
	}
	return sid, true
}

func capturePlace() string {
	sess := "session ?"
	if sid, ok := currentSessionID(); ok {
		sess = fmt.Sprintf("session %d", sid)
	}
	return fmt.Sprintf("%s, window station %s, desktop %s",
		sess, orUnknown(windowStationName()), orUnknown(threadDesktopName()))
}

func windowStationName() string {
	h, _, _ := procGetProcessWindowStation.Call()
	if h == 0 {
		return ""
	}
	return userObjectName(h)
}

func threadDesktopName() string {
	tid, _, _ := procGetCurrentThreadId.Call()
	h, _, _ := procGetThreadDesktop.Call(tid)
	if h == 0 {
		return ""
	}
	return userObjectName(h)
}

func userObjectName(h uintptr) string {
	var name [128]uint16
	var need uint32
	r, _, _ := procGetUserObjectInformationW.Call(h, uoiNameInfo,
		uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)*2), uintptr(unsafe.Pointer(&need)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(name[:])
}

func orUnknown(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// RunSessionWorker serves desktop operations on stdin/stdout. The parent starts
// it inside the interactive session; it must call the local GDI path, never
// the session-0 router.
func RunSessionWorker() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	log.SetOutput(os.Stderr)
	if err := bindWorkerDesktop(); err != nil {
		log.Printf("desktop worker: bind winsta0\\default: %v (%s)", err, capturePlace())
	} else {
		log.Printf("desktop worker: %s", capturePlace())
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req workerReq
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		resp := dispatchWorker(req)
		resp.ID = req.ID
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
}

func dispatchWorker(req workerReq) workerResp {
	if workerInSession0() {
		return workerResp{OK: false, Error: fmt.Sprintf(
			"desktop worker is still in session 0 (window station %s, desktop %s); capture has to run on the logged-on session's winsta0\\default",
			orUnknown(windowStationName()), orUnknown(threadDesktopName()))}
	}
	switch req.Op {
	case "shot":
		return workerShot()
	case "click":
		if err := clickHere(context.Background(), req.X, req.Y, req.Button); err != nil {
			return workerResp{OK: false, Error: err.Error()}
		}
		return workerResp{OK: true}
	case "type":
		if err := typeHere(context.Background(), req.Text); err != nil {
			return workerResp{OK: false, Error: err.Error()}
		}
		return workerResp{OK: true}
	case "key":
		if err := keyHere(context.Background(), req.Key); err != nil {
			return workerResp{OK: false, Error: err.Error()}
		}
		return workerResp{OK: true}
	case "window":
		title, app, err := activeWindowHere(context.Background())
		if err != nil {
			return workerResp{OK: false, Error: err.Error()}
		}
		return workerResp{OK: true, Title: title, App: app}
	case "probe":
		if err := probeClickHere(context.Background()); err != nil {
			return workerResp{OK: false, Error: err.Error()}
		}
		return workerResp{OK: true}
	case "available":
		ok, note := availableInteractive()
		w, h := screenSize()
		if !ok {
			return workerResp{OK: false, Error: note, W: w, H: h}
		}
		return workerResp{OK: true, W: w, H: h}
	case "lock":
		st := queryLockInteractive(context.Background())
		return workerResp{OK: true, Locked: st.Locked, Source: st.Source, Detail: st.Detail}
	default:
		return workerResp{OK: false, Error: "unknown op " + req.Op}
	}
}

func workerShot() workerResp {
	f, err := os.CreateTemp("", "marble-desk-*.png")
	if err != nil {
		return workerResp{OK: false, Error: err.Error()}
	}
	name := f.Name()
	_ = f.Close()
	defer os.Remove(name)
	w, h, err := screenshotHere(context.Background(), name)
	if err != nil {
		return workerResp{OK: false, Error: err.Error()}
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return workerResp{OK: false, Error: err.Error()}
	}
	return workerResp{OK: true, W: w, H: h, PNG: base64.StdEncoding.EncodeToString(b)}
}
