// marble-desk — CGEvent input helper for marble-peer (macOS).
// Compiled on first use into ~/.marble-peer/helpers/marble-desk
import AppKit
import ApplicationServices
import CoreGraphics
import Foundation

let helperVersion = "1"

func main() {
    let args = Array(CommandLine.arguments.dropFirst())
    guard let cmd = args.first else {
        fputs("usage: marble-desk version|perms|request-perms|screensize|click x y [button]|move x y|type TEXT|key NAME|frontmost\n", stderr)
        exit(2)
    }
    switch cmd {
    case "version":
        print(helperVersion)
    case "perms":
        print(permsJSON())
    case "request-perms":
        _ = CGRequestScreenCaptureAccess()
        let opts = [kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String: true] as CFDictionary
        _ = AXIsProcessTrustedWithOptions(opts)
        print(permsJSON())
    case "screensize":
        print(screensizeJSON())
    case "click":
        guard args.count >= 3, let x = Double(args[1]), let y = Double(args[2]) else {
            fputs("click requires x y\n", stderr)
            exit(2)
        }
        let button = args.count >= 4 ? args[3] : "1"
        click(x: x, y: y, button: button)
    case "move":
        guard args.count >= 3, let x = Double(args[1]), let y = Double(args[2]) else {
            fputs("move requires x y\n", stderr)
            exit(2)
        }
        move(x: x, y: y)
    case "type":
        let text: String
        if args.count >= 2 {
            text = args[1...].joined(separator: " ")
        } else if let data = try? FileHandle.standardInput.readToEnd(),
                  let s = String(data: data, encoding: .utf8) {
            text = s
        } else {
            fputs("type requires text\n", stderr)
            exit(2)
        }
        typeText(text)
    case "key":
        guard args.count >= 2 else {
            fputs("key requires name\n", stderr)
            exit(2)
        }
        sendKey(args[1...].joined(separator: "+"))
    case "frontmost":
        print(frontmostName())
    default:
        fputs("unknown command \(cmd)\n", stderr)
        exit(2)
    }
}

func permsJSON() -> String {
    let screen = CGPreflightScreenCaptureAccess()
    let ax = AXIsProcessTrusted()
    return "{\"screen\":\(screen),\"accessibility\":\(ax)}"
}

func screensizeJSON() -> String {
    guard let s = NSScreen.main else {
        return "{\"w\":0,\"h\":0,\"scale\":1}"
    }
    let f = s.frame
    let scale = s.backingScaleFactor
    let w = Int(f.width.rounded())
    let h = Int(f.height.rounded())
    let pxW = Int((f.width * scale).rounded())
    let pxH = Int((f.height * scale).rounded())
    return "{\"w\":\(w),\"h\":\(h),\"scale\":\(scale),\"px_w\":\(pxW),\"px_h\":\(pxH)}"
}

func frontmostName() -> String {
    NSWorkspace.shared.frontmostApplication?.localizedName ?? ""
}

func hidSource() -> CGEventSource? {
    CGEventSource(stateID: .hidSystemState)
}

func move(x: Double, y: Double) {
    let point = CGPoint(x: x, y: y)
    let ev = CGEvent(mouseEventSource: hidSource(), mouseType: .mouseMoved, mouseCursorPosition: point, mouseButton: .left)
    ev?.post(tap: .cghidEventTap)
}

func click(x: Double, y: Double, button: String) {
    let point = CGPoint(x: x, y: y)
    let src = hidSource()
    let b = button.lowercased()
    let downType: CGEventType
    let upType: CGEventType
    let mouseButton: CGMouseButton
    switch b {
    case "2", "middle", "m":
        downType = .otherMouseDown
        upType = .otherMouseUp
        mouseButton = .center
    case "3", "right", "r":
        downType = .rightMouseDown
        upType = .rightMouseUp
        mouseButton = .right
    default:
        downType = .leftMouseDown
        upType = .leftMouseUp
        mouseButton = .left
    }
    let moved = CGEvent(mouseEventSource: src, mouseType: .mouseMoved, mouseCursorPosition: point, mouseButton: .left)
    moved?.post(tap: .cghidEventTap)
    usleep(12_000)
    let down = CGEvent(mouseEventSource: src, mouseType: downType, mouseCursorPosition: point, mouseButton: mouseButton)
    down?.post(tap: .cghidEventTap)
    usleep(12_000)
    let up = CGEvent(mouseEventSource: src, mouseType: upType, mouseCursorPosition: point, mouseButton: mouseButton)
    up?.post(tap: .cghidEventTap)
}

func typeText(_ text: String) {
    let src = hidSource()
    for ch in text {
        if ch == "\n" || ch == "\r" {
            tapKey(virtualKey: 0x24, flags: [], source: src) // Return
            continue
        }
        if ch == "\t" {
            tapKey(virtualKey: 0x30, flags: [], source: src)
            continue
        }
        let s = String(ch)
        let utf16 = Array(s.utf16)
        let down = CGEvent(keyboardEventSource: src, virtualKey: 0, keyDown: true)
        down?.keyboardSetUnicodeString(stringLength: utf16.count, unicodeString: utf16)
        down?.post(tap: .cghidEventTap)
        let up = CGEvent(keyboardEventSource: src, virtualKey: 0, keyDown: false)
        up?.keyboardSetUnicodeString(stringLength: utf16.count, unicodeString: utf16)
        up?.post(tap: .cghidEventTap)
        usleep(8_000)
    }
}

func sendKey(_ spec: String) {
    var parts = spec.split(separator: "+").map { String($0) }
    if parts.isEmpty {
        fputs("empty key\n", stderr)
        exit(2)
    }
    let keyName = parts.removeLast()
    var flags: CGEventFlags = []
    for p in parts {
        switch p.lowercased() {
        case "ctrl", "control":
            flags.insert(.maskControl)
        case "alt", "option", "opt":
            flags.insert(.maskAlternate)
        case "shift":
            flags.insert(.maskShift)
        case "cmd", "command", "super", "meta", "win":
            flags.insert(.maskCommand)
        case "fn", "function":
            flags.insert(.maskSecondaryFn)
        default:
            fputs("unknown modifier \(p)\n", stderr)
            exit(2)
        }
    }
    guard let vk = virtualKey(keyName) else {
        // Fall back to unicode of the last token (e.g. a single character).
        typeText(keyName)
        return
    }
    tapKey(virtualKey: vk, flags: flags, source: hidSource())
}

func tapKey(virtualKey: CGKeyCode, flags: CGEventFlags, source: CGEventSource?) {
    let down = CGEvent(keyboardEventSource: source, virtualKey: virtualKey, keyDown: true)
    down?.flags = flags
    down?.post(tap: .cghidEventTap)
    usleep(8_000)
    let up = CGEvent(keyboardEventSource: source, virtualKey: virtualKey, keyDown: false)
    up?.flags = flags
    up?.post(tap: .cghidEventTap)
}

func virtualKey(_ name: String) -> CGKeyCode? {
    let n = name.trimmingCharacters(in: .whitespacesAndNewlines)
    let lower = n.lowercased()
    switch lower {
    case "return", "enter": return 0x24
    case "tab": return 0x30
    case "space", " ": return 0x31
    case "backspace", "delete": return 0x33
    case "escape", "esc": return 0x35
    case "forwarddelete", "del": return 0x75
    case "home": return 0x73
    case "end": return 0x77
    case "pageup", "prior": return 0x74
    case "pagedown", "next": return 0x79
    case "left", "arrow_left": return 0x7B
    case "right", "arrow_right": return 0x7C
    case "down", "arrow_down": return 0x7D
    case "up", "arrow_up": return 0x7E
    case "f1": return 0x7A
    case "f2": return 0x78
    case "f3": return 0x63
    case "f4": return 0x76
    case "f5": return 0x60
    case "f6": return 0x61
    case "f7": return 0x62
    case "f8": return 0x64
    case "f9": return 0x65
    case "f10": return 0x6D
    case "f11": return 0x67
    case "f12": return 0x6F
    default:
        break
    }
    if n.count == 1, let ch = n.lowercased().unicodeScalars.first {
        let map: [Character: CGKeyCode] = [
            "a": 0x00, "s": 0x01, "d": 0x02, "f": 0x03, "h": 0x04, "g": 0x05,
            "z": 0x06, "x": 0x07, "c": 0x08, "v": 0x09, "b": 0x0B, "q": 0x0C,
            "w": 0x0D, "e": 0x0E, "r": 0x0F, "y": 0x10, "t": 0x11,
            "1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15, "6": 0x16, "5": 0x17,
            "9": 0x19, "7": 0x1A, "8": 0x1C, "0": 0x1D,
            "o": 0x1F, "u": 0x20, "i": 0x22, "p": 0x23, "l": 0x25, "j": 0x26,
            "k": 0x28, "n": 0x2D, "m": 0x2E,
        ]
        if let vk = map[Character(ch)] {
            return vk
        }
    }
    return nil
}

main()
