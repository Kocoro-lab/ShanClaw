import ApplicationServices
import AppKit

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

/// Resolves an element by path (e.g. "window[0]/AXButton[2]/AXStaticText[0]").
func resolveElement(pid: Int, path: String) -> AXUIElement? {
    let appRef = AXUIElementCreateApplication(Int32(pid))
    guard let windows = axValue(appRef, "AXWindows") as? [AXUIElement] else {
        return nil
    }

    let allParts = path.split(separator: "/")
    guard !allParts.isEmpty else { return nil }

    // Parse window index from "window[N]"
    let winPart = allParts[0]
    var winIndex = 0
    if let bracketStart = winPart.firstIndex(of: "["),
       let bracketEnd = winPart.firstIndex(of: "]") {
        winIndex = Int(winPart[winPart.index(after: bracketStart)..<bracketEnd]) ?? 0
    }
    guard winIndex < windows.count else { return nil }

    let parts = allParts.dropFirst()
    var current: AXUIElement = windows[winIndex]

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

/// Returns the center coordinates (screen space) of an AXUIElement, or nil if position/size unavailable.
func elementCenter(_ el: AXUIElement) -> (Double, Double)? {
    var posVal: CFTypeRef?
    var sizeVal: CFTypeRef?
    guard AXUIElementCopyAttributeValue(el, "AXPosition" as CFString, &posVal) == .success,
          AXUIElementCopyAttributeValue(el, "AXSize" as CFString, &sizeVal) == .success else {
        return nil
    }
    var point = CGPoint.zero
    var size = CGSize.zero
    AXValueGetValue(posVal as! AXValue, .cgPoint, &point)
    AXValueGetValue(sizeVal as! AXValue, .cgSize, &size)
    return (Double(point.x + size.width / 2), Double(point.y + size.height / 2))
}

/// Returns the frame (origin + size) of an AXUIElement in screen coordinates, or nil if unavailable.
func elementFrame(_ el: AXUIElement) -> (x: Double, y: Double, width: Double, height: Double)? {
    var posVal: CFTypeRef?
    var sizeVal: CFTypeRef?
    guard AXUIElementCopyAttributeValue(el, "AXPosition" as CFString, &posVal) == .success,
          AXUIElementCopyAttributeValue(el, "AXSize" as CFString, &sizeVal) == .success else {
        return nil
    }
    var point = CGPoint.zero
    var size = CGSize.zero
    AXValueGetValue(posVal as! AXValue, .cgPoint, &point)
    AXValueGetValue(sizeVal as! AXValue, .cgSize, &size)
    return (Double(point.x), Double(point.y), Double(size.width), Double(size.height))
}

/// Returns context about the current state of an app (window title, focused element, browser URL).
func currentContext(pid: Int) -> AppContext {
    let appRef = AXUIElementCreateApplication(Int32(pid))
    let appName: String
    if let app = NSRunningApplication(processIdentifier: Int32(pid)) {
        appName = app.localizedName ?? "Unknown"
    } else {
        appName = "Unknown"
    }

    var windowTitle = ""
    if let windows = axValue(appRef, "AXWindows") as? [AXUIElement],
       let win = windows.first {
        windowTitle = axString(win, "AXTitle") ?? ""
    }

    // Check for browser URL
    var url: String? = nil
    if let windows = axValue(appRef, "AXWindows") as? [AXUIElement],
       let win = windows.first {
        if let toolbar = findToolbarChild(of: win) {
            if let urlField = findToolbarURLField(in: toolbar) {
                if let val = axValue(urlField, "AXValue") {
                    url = "\(val)"
                }
            }
        }
    }

    var focused: String? = nil
    var focusedRef: CFTypeRef?
    if AXUIElementCopyAttributeValue(appRef, "AXFocusedUIElement" as CFString, &focusedRef) == .success,
       let ref = focusedRef {
        // CFTypeRef is non-nil; cast to AXUIElement (CoreFoundation cast always succeeds)
        let el = ref as! AXUIElement
        let role = axString(el, "AXRole") ?? ""
        let title = axString(el, "AXTitle") ?? ""
        if !role.isEmpty {
            focused = title.isEmpty ? role : "\(role) '\(title)'"
        }
    }

    return AppContext(app: appName, window: windowTitle, url: url, focusedElement: focused)
}

/// Finds a child with AXToolbar role (used by currentContext for browser URL detection).
private func findToolbarChild(of el: AXUIElement) -> AXUIElement? {
    guard let children = axChildren(el) else { return nil }
    for child in children {
        if axString(child, "AXRole") == "AXToolbar" {
            return child
        }
    }
    for child in children {
        guard let grandchildren = axChildren(child) else { continue }
        for gc in grandchildren {
            if axString(gc, "AXRole") == "AXToolbar" {
                return gc
            }
        }
    }
    return nil
}

/// Finds a text field inside a toolbar that looks like a URL bar.
private func findToolbarURLField(in el: AXUIElement) -> AXUIElement? {
    guard let children = axChildren(el) else { return nil }
    for child in children {
        let role = axString(child, "AXRole") ?? ""
        if role == "AXTextField" || role == "AXComboBox" {
            return child
        }
        if let found = findToolbarURLField(in: child) {
            return found
        }
    }
    return nil
}

/// Resolves an app name to its PID via NSWorkspace.
/// Retries up to 3 times with short delays for apps that just launched.
func resolvePID(appName: String) -> Int? {
    for attempt in 0..<3 {
        for app in NSWorkspace.shared.runningApplications {
            if let name = app.localizedName, name.lowercased() == appName.lowercased() {
                return Int(app.processIdentifier)
            }
            if let bundleName = app.bundleIdentifier?.split(separator: ".").last,
               bundleName.lowercased() == appName.lowercased() {
                return Int(app.processIdentifier)
            }
        }
        if attempt < 2 {
            Thread.sleep(forTimeInterval: 0.5)
        }
    }
    return nil
}
