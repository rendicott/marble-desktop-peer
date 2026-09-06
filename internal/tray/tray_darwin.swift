// marble-peer macOS menu bar extra. Spawned by marble-peer run.
// Env: MARBLE_PEER_PID, MARBLE_PEER_STATUS_URL, MARBLE_PEER_MINIUI
import AppKit
import Darwin
import Foundation

let helperVersion = "1"

if CommandLine.arguments.dropFirst().first == "version" {
    print(helperVersion)
    exit(0)
}

class TrayApp: NSObject, NSApplicationDelegate {
    var item: NSStatusItem!
    var statusItem: NSMenuItem!
    var idItem: NSMenuItem!
    var confirmItem: NSMenuItem!
    var timer: Timer?
    let parent = Int32(ProcessInfo.processInfo.environment["MARBLE_PEER_PID"] ?? "0") ?? 0
    var statusURL = ProcessInfo.processInfo.environment["MARBLE_PEER_STATUS_URL"] ?? ""
    var miniUI = ProcessInfo.processInfo.environment["MARBLE_PEER_MINIUI"] ?? ""

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        if let btn = item.button {
            btn.title = "●"
            btn.toolTip = "Marble Peer"
        }
        let menu = NSMenu()
        statusItem = NSMenuItem(title: "Status: starting…", action: nil, keyEquivalent: "")
        statusItem.isEnabled = false
        menu.addItem(statusItem)
        idItem = NSMenuItem(title: "Computer: —", action: nil, keyEquivalent: "")
        idItem.isEnabled = false
        menu.addItem(idItem)
        menu.addItem(NSMenuItem.separator())
        confirmItem = NSMenuItem(title: "No pending confirmations", action: #selector(openConfirm), keyEquivalent: "")
        confirmItem.target = self
        confirmItem.isEnabled = false
        menu.addItem(confirmItem)
        let openItem = NSMenuItem(title: "Open mini UI", action: #selector(openMini), keyEquivalent: "o")
        openItem.target = self
        menu.addItem(openItem)
        let stopItem = NSMenuItem(title: "Stop current action", action: #selector(stopAction), keyEquivalent: "")
        stopItem.target = self
        menu.addItem(stopItem)
        menu.addItem(NSMenuItem.separator())
        let quitItem = NSMenuItem(title: "Quit marble-peer", action: #selector(quitPeer), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)
        item.menu = menu

        refresh()
        // 2s poll — do not use a tight loop (see docs/idle-cpu-status-storm.md).
        timer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: true) { [weak self] _ in
            self?.refresh()
        }
        if let timer {
            RunLoop.main.add(timer, forMode: .common)
        }
    }

    func fetchStatus() -> [String: Any] {
        guard !statusURL.isEmpty, let url = URL(string: statusURL) else { return [:] }
        var req = URLRequest(url: url, timeoutInterval: 2)
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        let sem = DispatchSemaphore(value: 0)
        var result: [String: Any] = [:]
        URLSession.shared.dataTask(with: req) { data, _, _ in
            defer { sem.signal() }
            if let data, let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
                result = obj
            }
        }.resume()
        _ = sem.wait(timeout: .now() + 2.5)
        return result
    }

    func refresh() {
        if parent > 0 && kill(parent, 0) != 0 {
            NSApp.terminate(nil)
            return
        }
        let st = fetchStatus()
        let state = (st["state"] as? String) ?? "unknown"
        let cid = (st["computer_id"] as? String) ?? "—"
        let browserOK = (st["browser_ready"] as? Bool) ?? false
        let browser = browserOK ? "browser" : "no-browser"
        let pending = st["pending_confirms"] as? [[String: Any]] ?? []
        let nconf = (st["confirm_count"] as? Int) ?? pending.count
        statusItem.title = "Status: \(state) (\(browser))"
        idItem.title = "Computer: \(cid)"
        if let addr = st["miniui_addr"] as? String, !addr.isEmpty {
            miniUI = addr
            if statusURL.isEmpty {
                statusURL = addr + "/status.json"
            }
        }
        if nconf > 0 {
            confirmItem.title = "⚠️ Confirm action (\(nconf)) — click"
            confirmItem.isEnabled = true
            item.button?.toolTip = "Marble Peer — CONFIRM needed (\(nconf))"
        } else {
            confirmItem.title = "No pending confirmations"
            confirmItem.isEnabled = false
            var tip = "Marble Peer — \(state)"
            if cid != "—" { tip += " (\(cid))" }
            item.button?.toolTip = tip
        }
    }

    @objc func openMini() {
        let url = miniUI
        if let u = URL(string: url) {
            NSWorkspace.shared.open(u)
        }
    }

    @objc func openConfirm() {
        let st = fetchStatus()
        if let pending = st["pending_confirms"] as? [[String: Any]],
           let first = pending.first,
           let url = first["url"] as? String,
           let u = URL(string: url) {
            NSWorkspace.shared.open(u)
            return
        }
        openMini()
    }

    @objc func stopAction() {
        post("/stop")
    }

    @objc func quitPeer() {
        post("/quit")
        if parent > 0 {
            kill(parent, SIGTERM)
        }
        NSApp.terminate(nil)
    }

    func post(_ path: String) {
        let base = miniUI.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        guard let url = URL(string: base + path) else { return }
        var req = URLRequest(url: url, timeoutInterval: 3)
        req.httpMethod = "POST"
        req.httpBody = Data()
        let sem = DispatchSemaphore(value: 0)
        URLSession.shared.dataTask(with: req) { _, _, _ in sem.signal() }.resume()
        _ = sem.wait(timeout: .now() + 3)
    }
}

let app = NSApplication.shared
let delegate = TrayApp()
app.delegate = delegate
app.run()
