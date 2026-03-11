import CoreGraphics
import AppKit

struct InputDriver {
    static func mouseEvent(type: String, x: Double, y: Double,
                           button: String = "left", clicks: Int = 1) -> (ActionResult?, ErrorInfo?) {
        let point = CGPoint(x: x, y: y)

        switch type {
        case "click":
            let (btn, down, up) = mouseConstants(button)
            for i in 0..<clicks {
                if let event = CGEvent(mouseEventSource: nil, mouseType: down,
                                       mouseCursorPosition: point, mouseButton: btn) {
                    if clicks > 1 {
                        event.setIntegerValueField(.mouseEventClickState, value: Int64(i + 1))
                    }
                    event.post(tap: .cghidEventTap)
                }
                if let event = CGEvent(mouseEventSource: nil, mouseType: up,
                                       mouseCursorPosition: point, mouseButton: btn) {
                    if clicks > 1 {
                        event.setIntegerValueField(.mouseEventClickState, value: Int64(i + 1))
                    }
                    event.post(tap: .cghidEventTap)
                }
            }
            return (ActionResult(result: "clicked \(button) at (\(Int(x)), \(Int(y))) \(clicks)x"), nil)

        case "move":
            if let event = CGEvent(mouseEventSource: nil, mouseType: .mouseMoved,
                                   mouseCursorPosition: point, mouseButton: .left) {
                event.post(tap: .cghidEventTap)
            }
            return (ActionResult(result: "moved to (\(Int(x)), \(Int(y)))"), nil)

        default:
            return (nil, ErrorInfo(code: -1, message: "unknown mouse event type: \(type)"))
        }
    }

    static func keyEvent(key: String, modifiers: [String]) -> (ActionResult?, ErrorInfo?) {
        let validMods: Set<String> = ["command", "cmd", "shift", "option", "alt", "control", "ctrl"]
        var flags: CGEventFlags = []
        for mod in modifiers {
            let m = mod.lowercased()
            guard validMods.contains(m) else {
                return (nil, ErrorInfo(code: -1, message: "unknown modifier: \(mod) (valid: command, cmd, shift, option, alt, control, ctrl)"))
            }
            switch m {
            case "command", "cmd": flags.insert(.maskCommand)
            case "shift": flags.insert(.maskShift)
            case "option", "alt": flags.insert(.maskAlternate)
            case "control", "ctrl": flags.insert(.maskControl)
            default: break
            }
        }

        guard let keyCode = keyCodeMap[key.lowercased()] else {
            return (nil, ErrorInfo(code: -1, message: "unknown key: \(key)"))
        }

        if let down = CGEvent(keyboardEventSource: nil, virtualKey: keyCode, keyDown: true) {
            down.flags = flags
            down.post(tap: .cghidEventTap)
        }
        if let up = CGEvent(keyboardEventSource: nil, virtualKey: keyCode, keyDown: false) {
            up.flags = flags
            up.post(tap: .cghidEventTap)
        }

        let modStr = modifiers.isEmpty ? "" : modifiers.joined(separator: "+") + "+"
        return (ActionResult(result: "pressed \(modStr)\(key)"), nil)
    }

    /// Type text. Non-ASCII (CJK, emoji) routes through clipboard paste
    /// because CGEvent synthetic keystrokes produce wrong output when an IME is active
    /// (macOS reads virtualKey=0 → 'a' instead of the Unicode string).
    static func typeText(_ text: String) -> (ActionResult?, ErrorInfo?) {
        let hasNonASCII = text.unicodeScalars.contains { $0.value > 0x7F }

        if hasNonASCII || text.count > 20 {
            // Clipboard paste path — safe for CJK/emoji/long text
            let pasteboard = NSPasteboard.general

            // Save all pasteboard items (not just string) to preserve files/images/HTML
            var savedItems: [[NSPasteboard.PasteboardType: Data]] = []
            for item in pasteboard.pasteboardItems ?? [] {
                var itemData: [NSPasteboard.PasteboardType: Data] = [:]
                for type in item.types {
                    if let data = item.data(forType: type) {
                        itemData[type] = data
                    }
                }
                if !itemData.isEmpty { savedItems.append(itemData) }
            }

            pasteboard.clearContents()
            guard pasteboard.setString(text, forType: .string) else {
                return (nil, ErrorInfo(code: -1, message: "Failed to set pasteboard"))
            }
            // Cmd+V
            let vKey: CGKeyCode = 0x09
            if let down = CGEvent(keyboardEventSource: nil, virtualKey: vKey, keyDown: true) {
                down.flags = .maskCommand
                down.post(tap: .cghidEventTap)
            }
            if let up = CGEvent(keyboardEventSource: nil, virtualKey: vKey, keyDown: false) {
                up.flags = .maskCommand
                up.post(tap: .cghidEventTap)
            }
            // Wait for paste to complete before restoring clipboard
            Thread.sleep(forTimeInterval: 0.1)

            // Restore original pasteboard contents
            pasteboard.clearContents()
            if !savedItems.isEmpty {
                var pbItems: [NSPasteboardItem] = []
                for itemData in savedItems {
                    let pbItem = NSPasteboardItem()
                    for (type, data) in itemData {
                        pbItem.setData(data, forType: type)
                    }
                    pbItems.append(pbItem)
                }
                pasteboard.writeObjects(pbItems)
            }

            let method = hasNonASCII ? "paste (non-ASCII)" : "paste (long text)"
            return (ActionResult(result: "typed via \(method): \(text)"), nil)
        }

        // Short ASCII text — direct keystroke synthesis
        for char in text {
            let key = String(char).lowercased()
            let needsShift = char.isUppercase || shiftChars.contains(char)

            // For shifted symbols (!@#...), look up the base key
            let baseKey = shiftedCharMap[char] ?? key
            guard let keyCode = keyCodeMap[baseKey] ?? keyCodeMap[String(char)] else {
                // Unknown char — skip
                continue
            }

            var flags: CGEventFlags = []
            if needsShift { flags.insert(.maskShift) }

            if let down = CGEvent(keyboardEventSource: nil, virtualKey: keyCode, keyDown: true) {
                down.flags = flags
                down.post(tap: .cghidEventTap)
            }
            if let up = CGEvent(keyboardEventSource: nil, virtualKey: keyCode, keyDown: false) {
                up.flags = flags
                up.post(tap: .cghidEventTap)
            }
            Thread.sleep(forTimeInterval: 0.01)
        }
        return (ActionResult(result: "typed: \(text)"), nil)
    }

    static func scroll(dx: Int, dy: Int) {
        if let event = CGEvent(scrollWheelEvent2Source: nil, units: .pixel,
                               wheelCount: 2, wheel1: Int32(dy), wheel2: Int32(dx), wheel3: 0) {
            event.post(tap: .cghidEventTap)
        }
    }

    private static func mouseConstants(_ button: String) -> (CGMouseButton, CGEventType, CGEventType) {
        switch button.lowercased() {
        case "right":
            return (.right, .rightMouseDown, .rightMouseUp)
        default:
            return (.left, .leftMouseDown, .leftMouseUp)
        }
    }
}

/// Characters that require shift to produce on a US keyboard.
private let shiftChars: Set<Character> = Set("~!@#$%^&*()_+{}|:\"<>?ABCDEFGHIJKLMNOPQRSTUVWXYZ")

/// Maps shifted symbols to their base key for keycode lookup (US keyboard layout).
private let shiftedCharMap: [Character: String] = [
    "~": "`", "!": "1", "@": "2", "#": "3", "$": "4",
    "%": "5", "^": "6", "&": "7", "*": "8", "(": "9",
    ")": "0", "_": "-", "+": "=", "{": "[", "}": "]",
    "|": "\\", ":": ";", "\"": "'", "<": ",", ">": ".",
    "?": "/",
]

let keyCodeMap: [String: CGKeyCode] = [
    // Letters
    "a": 0x00, "b": 0x0B, "c": 0x08, "d": 0x02, "e": 0x0E,
    "f": 0x03, "g": 0x05, "h": 0x04, "i": 0x22, "j": 0x26,
    "k": 0x28, "l": 0x25, "m": 0x2E, "n": 0x2D, "o": 0x1F,
    "p": 0x23, "q": 0x0C, "r": 0x0F, "s": 0x01, "t": 0x11,
    "u": 0x20, "v": 0x09, "w": 0x0D, "x": 0x07, "y": 0x10,
    "z": 0x06,

    // Numbers
    "0": 0x1D, "1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15,
    "5": 0x17, "6": 0x16, "7": 0x1A, "8": 0x1C, "9": 0x19,

    // Special keys
    "return": 0x24, "enter": 0x24, "tab": 0x30, "space": 0x31,
    "delete": 0x33, "backspace": 0x33, "escape": 0x35, "esc": 0x35,

    // Arrow keys
    "left": 0x7B, "right": 0x7C, "down": 0x7D, "up": 0x7E,

    // Function keys
    "f1": 0x7A, "f2": 0x78, "f3": 0x63, "f4": 0x76,
    "f5": 0x60, "f6": 0x61, "f7": 0x62, "f8": 0x64,
    "f9": 0x65, "f10": 0x6D, "f11": 0x67, "f12": 0x6F,

    // Modifiers (for standalone use)
    "command": 0x37, "shift": 0x38, "option": 0x3A, "control": 0x3B,

    // Punctuation
    "-": 0x1B, "=": 0x18, "[": 0x21, "]": 0x1E,
    "\\": 0x2A, ";": 0x29, "'": 0x27, ",": 0x2B,
    ".": 0x2F, "/": 0x2C, "`": 0x32,

    // Navigation
    "home": 0x73, "end": 0x77, "pageup": 0x74, "pagedown": 0x79,
    "forwarddelete": 0x75,
]
