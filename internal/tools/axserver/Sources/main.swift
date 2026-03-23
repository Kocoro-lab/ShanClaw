import Foundation
import ApplicationServices
import CoreGraphics
import AppKit

// MARK: - CLI Permission Modes

let args = CommandLine.arguments

if args.contains("--check-permissions") {
    // One-shot: output permission status JSON and exit.
    // Does NOT require accessibility to be granted — that's what we're checking.
    let status = checkAllPermissions()
    if let data = try? JSONSerialization.data(withJSONObject: status, options: [.sortedKeys]),
       let str = String(data: data, encoding: .utf8) {
        print(str)
    }
    exit(0)
}

if let idx = args.firstIndex(of: "--request-permission"), idx + 1 < args.count {
    // One-shot: trigger permission dialog and exit.
    let permission = args[idx + 1]
    let result = requestPermissionCLI(permission)
    if let data = try? JSONSerialization.data(withJSONObject: result, options: [.sortedKeys]),
       let str = String(data: data, encoding: .utf8) {
        print(str)
    }
    exit(0)
}

// MARK: - Normal Stdin Loop Mode

// Check accessibility permission once at startup.
guard AXIsProcessTrusted() else {
    let err = Response(id: 0, error: ErrorInfo(code: -1,
        message: "Accessibility permission not granted. Enable in: System Settings > Privacy & Security > Accessibility. Add your terminal app."))
    writeResponse(err)
    exit(1)
}

let encoder = JSONEncoder()
encoder.outputFormatting = [.sortedKeys]
let decoder = JSONDecoder()

// Persistent stdin read loop — one JSON request per line.
while let line = readLine(strippingNewline: true) {
    guard let data = line.data(using: .utf8) else { continue }

    guard let req = try? decoder.decode(Request.self, from: data) else {
        let resp = Response(id: 0, error: ErrorInfo(code: -1, message: "Invalid JSON request"))
        writeResponse(resp)
        continue
    }

    let params = req.params ?? Params(
        pid: nil, maxDepth: nil, semanticBudget: nil, filter: nil,
        path: nil, expectedRole: nil, value: nil, appName: nil,
        query: nil, role: nil, identifier: nil, type: nil,
        x: nil, y: nil, button: nil, clicks: nil,
        key: nil, modifiers: nil, dx: nil, dy: nil,
        windowTitle: nil, verify: nil, condition: nil,
        timeout: nil, interval: nil, roles: nil, maxLabels: nil
    )

    let response = dispatch(id: req.id, method: req.method, params: params)
    writeResponse(response)
}

// MARK: - Dispatch

func dispatch(id: Int64, method: String, params: Params) -> Response {
    switch method {
    case "ping":
        return Response(id: id, result: AnyCodable(["ok": true]))

    case "read_tree":
        let pid = params.pid ?? frontmostPID()
        guard pid > 0 else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "Cannot determine frontmost application"))
        }
        let budget: Int
        if let sb = params.semanticBudget {
            budget = sb
        } else if let md = params.maxDepth {
            budget = md * 6 // backward compat heuristic
        } else {
            budget = 25
        }
        let filter = params.filter ?? "all"
        guard let result = readTree(pid: pid, budget: budget, filter: filter) else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "No windows found. Is the app running and visible?"))
        }
        return Response(id: id, result: AnyCodable(result))

    case "click", "press":
        guard let pid = params.pid, let path = params.path else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "\(method) requires 'pid' and 'path'"))
        }
        let (result, err) = performClick(pid: pid, path: path, expectedRole: params.expectedRole)
        if let err = err { return Response(id: id, error: err) }
        return Response(id: id, result: AnyCodable(result!))

    case "set_value":
        guard let pid = params.pid, let path = params.path, let value = params.value else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "set_value requires 'pid', 'path', and 'value'"))
        }
        let (result, err) = setValue(pid: pid, path: path, value: value, expectedRole: params.expectedRole)
        if let err = err { return Response(id: id, error: err) }
        return Response(id: id, result: AnyCodable(result!))

    case "get_value":
        guard let pid = params.pid, let path = params.path else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "get_value requires 'pid' and 'path'"))
        }
        let (result, err) = getValue(pid: pid, path: path)
        if let err = err { return Response(id: id, error: err) }
        return Response(id: id, result: AnyCodable(result!))

    case "find":
        let pid = params.pid ?? frontmostPID()
        guard pid > 0 else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "Cannot determine target app"))
        }
        let results = findElements(pid: pid, query: params.query, role: params.role, identifier: params.identifier)
        return Response(id: id, result: AnyCodable(results))

    case "resolve_pid":
        guard let appName = params.appName else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "resolve_pid requires 'app_name'"))
        }
        guard let pid = resolvePID(appName: appName) else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "App '\(appName)' not found or not running"))
        }
        return Response(id: id, result: AnyCodable(["pid": pid]))

    case "mouse_event":
        guard let type = params.type, let x = params.x, let y = params.y else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "mouse_event requires 'type', 'x', 'y'"))
        }
        let (result, err) = InputDriver.mouseEvent(
            type: type, x: x, y: y,
            button: params.button ?? "left",
            clicks: params.clicks ?? 1
        )
        if let err = err { return Response(id: id, error: err) }
        var r = result!
        r.context = currentContext(pid: frontmostPID())
        return Response(id: id, result: AnyCodable(r))

    case "key_event":
        guard let key = params.key else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "key_event requires 'key'"))
        }
        let (result, err) = InputDriver.keyEvent(key: key, modifiers: params.modifiers ?? [])
        if let err = err { return Response(id: id, error: err) }
        var r = result!
        r.context = currentContext(pid: frontmostPID())
        return Response(id: id, result: AnyCodable(r))

    case "type_text":
        guard let text = params.value else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "type_text requires 'value'"))
        }
        let (result, err) = InputDriver.typeText(text)
        if let err = err { return Response(id: id, error: err) }
        var r = result!
        r.context = currentContext(pid: frontmostPID())
        return Response(id: id, result: AnyCodable(r))

    case "scroll":
        let dx = params.dx ?? 0
        let dy = params.dy ?? 0
        let pid = params.pid ?? frontmostPID()
        let (result, err) = performScroll(pid: pid, path: params.path, dx: dx, dy: dy)
        if let err = err { return Response(id: id, error: err) }
        return Response(id: id, result: AnyCodable(result!))

    case "focus":
        guard let appName = params.appName else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "focus requires 'app_name'"))
        }
        let (result, err) = FocusManager.focusApp(
            appName: appName,
            windowTitle: params.windowTitle,
            verify: params.verify ?? false
        )
        if let err = err { return Response(id: id, error: err) }
        return Response(id: id, result: AnyCodable(result!))

    case "frontmost":
        let (result, err) = FocusManager.frontmost()
        if let err = err { return Response(id: id, error: err) }
        return Response(id: id, result: AnyCodable(result!))

    case "list_windows":
        let pid = params.pid ?? frontmostPID()
        guard pid > 0 else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "Cannot determine target app"))
        }
        let windows = FocusManager.listWindows(pid: pid)
        return Response(id: id, result: AnyCodable(windows))

    case "wait_for":
        guard let condition = params.condition else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "wait_for requires 'condition'"))
        }
        let pid = params.pid ?? frontmostPID()
        guard pid > 0 else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "Cannot determine target app"))
        }
        let timeout = params.timeout ?? 10.0
        let interval = params.interval ?? 0.5
        let (result, err) = waitFor(
            pid: pid, condition: condition, value: params.value,
            query: params.query, role: params.role,
            timeout: timeout, interval: interval
        )
        if let err = err { return Response(id: id, error: err) }
        return Response(id: id, result: AnyCodable(result!))

    case "annotate":
        let pid = params.pid ?? frontmostPID()
        guard pid > 0 else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "Cannot determine frontmost application"))
        }
        let maxLabels = params.maxLabels ?? 50
        guard let result = annotateElements(pid: pid, roles: params.roles, maxLabels: maxLabels) else {
            return Response(id: id, error: ErrorInfo(code: -1, message: "No windows found. Is the app running and visible?"))
        }
        return Response(id: id, result: AnyCodable(result))

    default:
        return Response(id: id, error: ErrorInfo(code: -1, message: "Unknown method: \(method)"))
    }
}

// MARK: - Helpers

func frontmostPID() -> Int {
    guard let app = NSWorkspace.shared.frontmostApplication else { return 0 }
    return Int(app.processIdentifier)
}

func writeResponse(_ resp: Response) {
    if let data = try? encoder.encode(resp),
       var str = String(data: data, encoding: .utf8) {
        str += "\n"
        FileHandle.standardOutput.write(str.data(using: .utf8)!)
    }
}

// MARK: - Permission Checks (CLI Mode)

func checkAllPermissions() -> [String: String] {
    return [
        "accessibility": AXIsProcessTrusted() ? "granted" : "denied",
        "screen_recording": checkScreenRecording(),
        "automation": checkAutomation(),
    ]
}

func checkScreenRecording() -> String {
    // CGPreflightScreenCaptureAccess() returns true if granted, false otherwise.
    // Available macOS 10.15+. Does NOT trigger a prompt.
    if CGPreflightScreenCaptureAccess() {
        return "granted"
    }
    return "denied"
}

func checkAutomation() -> String {
    // Use AEDeterminePermissionToAutomateTarget to check Automation permission
    // WITHOUT triggering a consent dialog. Available macOS 10.14+.
    // We check permission to send events to System Events (com.apple.systemevents).

    // Ensure System Events is running — AEDeterminePermissionToAutomateTarget
    // returns procNotFound (-600) if the target app isn't running.
    // System Events is a background-only daemon, safe to launch.
    ensureSystemEventsRunning()

    let addressDesc = NSAppleEventDescriptor(bundleIdentifier: "com.apple.systemevents")
    let status = AEDeterminePermissionToAutomateTarget(
        addressDesc.aeDesc,     // target
        typeWildCard,           // theAEEventClass
        typeWildCard,           // theAEEventID
        false                   // askUserIfNeeded: false = passive check, no prompt
    )

    switch status {
    case noErr:
        return "granted"
    case OSStatus(errAEEventNotPermitted):
        return "denied"
    case OSStatus(-1744): // errAEEventWouldRequireUserConsent
        return "denied"
    case OSStatus(procNotFound):
        // System Events still not running after launch attempt
        return "unknown"
    default:
        return "unknown"
    }
}

func ensureSystemEventsRunning() {
    let running = NSWorkspace.shared.runningApplications.contains {
        $0.bundleIdentifier == "com.apple.systemevents"
    }
    if running { return }

    guard let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: "com.apple.systemevents") else {
        return
    }
    let config = NSWorkspace.OpenConfiguration()
    config.activates = false
    let sem = DispatchSemaphore(value: 0)
    NSWorkspace.shared.openApplication(at: url, configuration: config) { _, _ in
        sem.signal()
    }
    _ = sem.wait(timeout: .now() + 2.0)
}

func requestPermissionCLI(_ permission: String) -> [String: String] {
    switch permission {
    case "accessibility":
        // AXIsProcessTrustedWithOptions with prompt: true opens System Settings
        // to the Accessibility pane and highlights this app.
        let opts = [kAXTrustedCheckOptionPrompt.takeUnretainedValue(): true] as CFDictionary
        let granted = AXIsProcessTrustedWithOptions(opts)
        return [
            "permission": "accessibility",
            "status": granted ? "granted" : "prompted",
            "message": granted ? "" : "Permission dialog shown. Enable in: System Settings > Privacy & Security > Accessibility",
        ]

    case "screen_recording":
        // CGRequestScreenCaptureAccess() triggers the system dialog on first call.
        // Returns true if already granted.
        let granted = CGRequestScreenCaptureAccess()
        return [
            "permission": "screen_recording",
            "status": granted ? "granted" : "prompted",
            "message": granted ? "" : "Permission dialog shown. Enable in: System Settings > Privacy & Security > Screen Recording",
        ]

    case "automation":
        // Trigger the "wants to control" dialog by attempting an Apple Event.
        let script = NSAppleScript(source: """
            tell application "System Events" to get name of first process whose frontmost is true
        """)
        var errorInfo: NSDictionary?
        script?.executeAndReturnError(&errorInfo)
        let granted = errorInfo == nil
        return [
            "permission": "automation",
            "status": granted ? "granted" : "prompted",
            "message": granted ? "" : "Permission dialog shown. Enable in: System Settings > Privacy & Security > Automation",
        ]

    default:
        return [
            "permission": permission,
            "status": "unknown",
            "message": "Unsupported permission: \(permission)",
        ]
    }
}
