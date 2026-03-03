import Foundation
import AppKit
import ApplicationServices

struct Input: Decodable {
    let action: String
    let pid: Int?
    let max_depth: Int?
    let filter: String?
    let path: String?
    let expected_role: String?
    let value: String?
}

struct Element: Encodable {
    let ref: String
    let role: String
    var subrole: String?
    var title: String?
    var desc: String?
    var value: String?
    var enabled: Bool?
    var selected: Bool?
    var children: [Element]?
}

struct ReadTreeOutput: Encodable {
    let app: String
    let pid: Int
    let window: String
    let elements: [Element]
    let ref_paths: [String: [String: String]]
}

struct ActionOutput: Encodable {
    let result: String
    var role: String?
}

struct ErrorOutput: Encodable {
    let error: String
}

func axValue(_ el: AXUIElement, _ attr: String) -> CFTypeRef? {
    var val: CFTypeRef?
    let err = AXUIElementCopyAttributeValue(el, attr as CFString, &val)
    return err == .success ? val : nil
}

func axString(_ el: AXUIElement, _ attr: String) -> String? {
    axValue(el, attr) as? String
}

func axBool(_ el: AXUIElement, _ attr: String) -> Bool? {
    guard let val = axValue(el, attr) else { return nil }
    if let num = val as? NSNumber { return num.boolValue }
    return nil
}

func axChildren(_ el: AXUIElement) -> [AXUIElement]? {
    axValue(el, "AXChildren") as? [AXUIElement]
}

let interactiveRoles: Set<String> = [
    "AXButton", "AXTextField", "AXTextArea", "AXCheckBox",
    "AXRadioButton", "AXPopUpButton", "AXComboBox", "AXSlider",
    "AXMenuItem", "AXLink", "AXRow", "AXMenuButton",
    "AXIncrementor", "AXColorWell", "AXDisclosureTriangle"
]

var refCounter = 0
var refPaths: [String: [String: String]] = [:]

func walkTree(_ el: AXUIElement, depth: Int, maxDepth: Int, filter: String, path: String) -> Element? {
    guard depth <= maxDepth else { return nil }
    guard let role = axString(el, "AXRole") else { return nil }

    let subrole = axString(el, "AXSubrole")
    let title = axString(el, "AXTitle")
    let desc = axString(el, "AXDescription")
    var valStr: String? = nil
    if let v = axValue(el, "AXValue") {
        let s = "\(v)"
        valStr = s.count > 100 ? String(s.prefix(100)) + "..." : s
    }
    let enabled = axBool(el, "AXEnabled")
    let selected = axBool(el, "AXSelected")

    var childElements: [Element]? = nil
    if let kids = axChildren(el), depth < maxDepth {
        var childIndex: [String: Int] = [:]
        var results: [Element] = []
        for kid in kids {
            guard let kidRole = axString(kid, "AXRole") else { continue }
            let idx = childIndex[kidRole, default: 0]
            childIndex[kidRole] = idx + 1
            let childPath = "\(path)/\(kidRole)[\(idx)]"
            if let child = walkTree(kid, depth: depth + 1, maxDepth: maxDepth, filter: filter, path: childPath) {
                results.append(child)
            }
        }
        if !results.isEmpty { childElements = results }
    }

    if filter == "interactive" {
        let isInteractive = interactiveRoles.contains(role)
        let hasInteractiveChildren = childElements != nil && !childElements!.isEmpty
        if !isInteractive && !hasInteractiveChildren { return nil }
    }

    refCounter += 1
    let ref = "e\(refCounter)"
    refPaths[ref] = ["path": path, "role": role]

    var elem = Element(ref: ref, role: role)
    if let s = subrole, !s.isEmpty { elem.subrole = s }
    if let t = title, !t.isEmpty { elem.title = t }
    if let d = desc, !d.isEmpty { elem.desc = d }
    if let v = valStr, !v.isEmpty { elem.value = v }
    if let e = enabled, !e { elem.enabled = e }
    if let s = selected, s { elem.selected = s }
    elem.children = childElements

    return elem
}

func readTree(pid: Int, maxDepth: Int, filter: String) {
    let appRef = AXUIElementCreateApplication(Int32(pid))
    let appName: String
    if let app = NSRunningApplication(processIdentifier: Int32(pid)) {
        appName = app.localizedName ?? "Unknown"
    } else {
        appName = "Unknown"
    }

    guard let windows = axValue(appRef, "AXWindows") as? [AXUIElement], let win = windows.first else {
        outputJSON(ErrorOutput(error: "No windows found for \(appName) (pid \(pid)). Is the app running and visible?"))
        return
    }

    let winTitle = axString(win, "AXTitle") ?? ""
    refCounter = 0
    refPaths = [:]
    var elements: [Element] = []
    if let kids = axChildren(win) {
        var childIndex: [String: Int] = [:]
        for kid in kids {
            guard let kidRole = axString(kid, "AXRole") else { continue }
            let idx = childIndex[kidRole, default: 0]
            childIndex[kidRole] = idx + 1
            let path = "window[0]/\(kidRole)[\(idx)]"
            if let elem = walkTree(kid, depth: 1, maxDepth: maxDepth, filter: filter, path: path) {
                elements.append(elem)
            }
        }
    }

    let output = ReadTreeOutput(app: appName, pid: pid, window: winTitle, elements: elements, ref_paths: refPaths)
    outputJSON(output)
}

func resolveElement(pid: Int, path: String) -> AXUIElement? {
    let appRef = AXUIElementCreateApplication(Int32(pid))
    guard let windows = axValue(appRef, "AXWindows") as? [AXUIElement], let win = windows.first else {
        return nil
    }
    let parts = path.split(separator: "/").dropFirst()
    var current: AXUIElement = win

    for part in parts {
        guard let bracketStart = part.firstIndex(of: "["),
              let bracketEnd = part.firstIndex(of: "]") else { return nil }
        let role = String(part[part.startIndex..<bracketStart])
        guard let index = Int(part[part.index(after: bracketStart)..<bracketEnd]) else { return nil }

        guard let children = axChildren(current) else { return nil }
        var roleCount = 0
        var found = false
        for child in children {
            if axString(child, "AXRole") == role {
                if roleCount == index {
                    current = child
                    found = true
                    break
                }
                roleCount += 1
            }
        }
        if !found { return nil }
    }
    return current
}

func performClick(pid: Int, path: String, expectedRole: String?) {
    guard let el = resolveElement(pid: pid, path: path) else {
        outputJSON(ErrorOutput(error: "Element not found at path '\(path)'. UI may have changed — call read_tree to refresh."))
        return
    }
    let role = axString(el, "AXRole") ?? "unknown"
    if let expected = expectedRole, role != expected {
        outputJSON(ErrorOutput(error: "Expected \(expected) but found \(role) at path. UI may have changed — call read_tree to refresh."))
        return
    }
    let title = axString(el, "AXTitle") ?? ""
    let err = AXUIElementPerformAction(el, "AXPress" as CFString)
    if err == .success {
        outputJSON(ActionOutput(result: "pressed \(role) '\(title)'", role: role))
    } else {
        outputJSON(ErrorOutput(error: "AXPress failed on \(role) '\(title)' (error \(err.rawValue)). Action may not be supported."))
    }
}

func setValue(pid: Int, path: String, value: String, expectedRole: String?) {
    guard let el = resolveElement(pid: pid, path: path) else {
        outputJSON(ErrorOutput(error: "Element not found at path '\(path)'. UI may have changed — call read_tree to refresh."))
        return
    }
    let role = axString(el, "AXRole") ?? "unknown"
    if let expected = expectedRole, role != expected {
        outputJSON(ErrorOutput(error: "Expected \(expected) but found \(role) at path. UI may have changed — call read_tree to refresh."))
        return
    }
    AXUIElementSetAttributeValue(el, "AXFocused" as CFString, true as CFTypeRef)
    let err = AXUIElementSetAttributeValue(el, "AXValue" as CFString, value as CFTypeRef)
    if err == .success {
        outputJSON(ActionOutput(result: "set value on \(role) to '\(value)'", role: role))
    } else {
        outputJSON(ErrorOutput(error: "set_value failed on \(role) (error \(err.rawValue)). Element may not be settable."))
    }
}

func getValue(pid: Int, path: String) {
    guard let el = resolveElement(pid: pid, path: path) else {
        outputJSON(ErrorOutput(error: "Element not found at path '\(path)'. UI may have changed — call read_tree to refresh."))
        return
    }
    let role = axString(el, "AXRole") ?? "unknown"
    let val = axValue(el, "AXValue")
    let valStr = val != nil ? "\(val!)" : ""
    outputJSON(ActionOutput(result: valStr, role: role))
}

func outputJSON<T: Encodable>(_ value: T) {
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.sortedKeys]
    if let data = try? encoder.encode(value) {
        print(String(data: data, encoding: .utf8)!)
    }
}

guard let inputData = readLine(strippingNewline: true)?.data(using: .utf8) else {
    outputJSON(ErrorOutput(error: "No input on stdin"))
    exit(1)
}

guard let input = try? JSONDecoder().decode(Input.self, from: inputData) else {
    outputJSON(ErrorOutput(error: "Invalid JSON input"))
    exit(1)
}

if !AXIsProcessTrusted() {
    outputJSON(ErrorOutput(error: "Accessibility permission not granted. Enable in: System Settings > Privacy & Security > Accessibility. Add your terminal app."))
    exit(1)
}

let pid: Int
if let p = input.pid {
    pid = p
} else {
    guard let app = NSWorkspace.shared.frontmostApplication else {
        outputJSON(ErrorOutput(error: "Cannot determine frontmost application"))
        exit(1)
    }
    pid = Int(app.processIdentifier)
}

switch input.action {
case "read_tree":
    readTree(pid: pid, maxDepth: input.max_depth ?? 4, filter: input.filter ?? "all")
case "click", "press":
    guard let path = input.path else {
        outputJSON(ErrorOutput(error: "action '\(input.action)' requires 'path'"))
        exit(1)
    }
    performClick(pid: pid, path: path, expectedRole: input.expected_role)
case "set_value":
    guard let path = input.path, let value = input.value else {
        outputJSON(ErrorOutput(error: "set_value requires 'path' and 'value'"))
        exit(1)
    }
    setValue(pid: pid, path: path, value: value, expectedRole: input.expected_role)
case "get_value":
    guard let path = input.path else {
        outputJSON(ErrorOutput(error: "get_value requires 'path'"))
        exit(1)
    }
    getValue(pid: pid, path: path)
default:
    outputJSON(ErrorOutput(error: "Unknown action: \(input.action)"))
    exit(1)
}
