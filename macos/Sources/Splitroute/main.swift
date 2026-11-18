import AppKit
import Combine
import SwiftUI

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate, NSWindowDelegate {
    private let model = AppModel()
    private var item: NSStatusItem!
    private var settingsWindow: NSWindow?
    private var bag = Set<AnyCancellable>()
    private var launchedAtLogin = false

    // The launch Apple event is only available here, not in didFinishLaunching.
    func applicationWillFinishLaunching(_ notification: Notification) {
        let e = NSAppleEventManager.shared().currentAppleEvent
        launchedAtLogin = e?.eventID == kAEOpenApplication
            && e?.paramDescriptor(forKeyword: keyAEPropData)?.enumCodeValue == keyAELaunchedAsLogInItem
    }

    /// Opening the app again (Spotlight, Finder, Dock, `open`) while it runs shows the window.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        openSettings()
        return false
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        let menu = NSMenu()
        menu.delegate = self
        item.menu = menu
        model.$status.combineLatest(model.$connError)
            .receive(on: RunLoop.main)
            .sink { [weak self] _, _ in self?.updateIcon() }
            .store(in: &bag)
        updateIcon()
        installMainMenu()
        unfocusFieldsOnOutsideClick()
        // Opened by the user (Spotlight, Finder, `open`): show the window. At login
        // only the menu bar icon appears.
        if !launchedAtLogin || CommandLine.arguments.contains("--settings") { openSettings() }
        #if DEBUG
        let args = CommandLine.arguments
        if let i = args.firstIndex(of: "--snapshot"), i + 1 < args.count {
            openSettings()
            Task { if let w = settingsWindow { await snapshotSettings(w, to: args[i + 1]) } }
        }
        // `--page lists` opens the window on that page (window captures for UI review).
        if let i = args.firstIndex(of: "--page"), i + 1 < args.count, let p = Page(rawValue: args[i + 1]) {
            openSettings()
            UserDefaults.standard.set(p.rawValue, forKey: "settingsPage")
        }
        #endif
    }

    private func updateIcon() {
        let color: NSColor
        switch model.health {
        case .active: color = .systemGreen
        case .problem: color = .systemOrange
        case .off, .notRunning: color = .systemRed
        }
        item.button?.image = menuBarIcon(dot: color)
        item.button?.toolTip = "Splitroute: \(model.summary)"
    }

    // The menu is rebuilt each time it opens, from the latest status.
    func menuNeedsUpdate(_ menu: NSMenu) {
        menu.removeAllItems()
        menu.addItem(disabled(model.summary))
        if let line = model.networkLine { menu.addItem(disabled(line)) }
        menu.addItem(.separator())
        switch model.health {
        case .notRunning:
            if model.serviceNeedsApproval {
                menu.addItem(action("Allow in Login Items…", #selector(openLoginItems)))
            } else if model.connError == .notRunning {
                menu.addItem(action("Install Service…", #selector(installService)))
            }
        case .off:
            menu.addItem(action("Turn On", #selector(turnOn)))
        case .active, .problem:
            menu.addItem(action("Turn Off", #selector(turnOff)))
        }
        if model.serviceOutdated {
            menu.addItem(action("Update Service", #selector(restartService)))
        }
        menu.addItem(action("Open App", #selector(openSettings)))
        menu.addItem(.separator())
        menu.addItem(action("Quit Splitroute", #selector(quit), key: "q"))
    }

    private func disabled(_ title: String) -> NSMenuItem {
        let i = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        i.isEnabled = false
        return i
    }

    private func action(_ title: String, _ sel: Selector, key: String = "") -> NSMenuItem {
        let i = NSMenuItem(title: title, action: sel, keyEquivalent: key)
        i.target = self
        return i
    }

    @objc private func turnOn() { Task { await model.setEnabled(true) } }
    @objc private func turnOff() { Task { await model.setEnabled(false) } }
    @objc private func installService() { Task { await model.installService() } }
    @objc private func restartService() { Task { await model.restartService() } }
    @objc private func openLoginItems() { model.openLoginItems() }
    private static let quitActionKey = "quitAction" // "turnOff" or "keep", once "Don't ask again" was checked

    /// Quitting only removes the icon; the service keeps routing. While routes are
    /// active, ask whether to turn them off first so they don't stay on unseen.
    @objc private func quit() {
        guard model.health == .active || model.health == .problem else { return NSApp.terminate(nil) }
        let d = UserDefaults.standard
        var turnOff: Bool
        switch d.string(forKey: Self.quitActionKey) {
        case "turnOff": turnOff = true
        case "keep": turnOff = false
        default:
            let alert = NSAlert()
            alert.messageText = "Splitroute is still routing."
            alert.informativeText = "Routes stay active after you quit. Open Splitroute again or use the splitroute command to turn them off later."
            alert.addButton(withTitle: "Turn Off and Quit")
            alert.addButton(withTitle: "Quit")
            alert.addButton(withTitle: "Cancel")
            alert.showsSuppressionButton = true
            alert.suppressionButton?.title = "Don't ask again"
            NSApp.activate(ignoringOtherApps: true)
            let r = alert.runModal()
            if r == .alertThirdButtonReturn { return }
            turnOff = r == .alertFirstButtonReturn
            if alert.suppressionButton?.state == .on { d.set(turnOff ? "turnOff" : "keep", forKey: Self.quitActionKey) }
        }
        guard turnOff else { return NSApp.terminate(nil) }
        Task {
            await model.setEnabled(false)
            if model.status?.enabled == false { return NSApp.terminate(nil) }
            // Never quit over a failure: the routes would stay on with no icon.
            let alert = NSAlert()
            alert.messageText = "Couldn't turn off Splitroute."
            alert.informativeText = model.actionError ?? model.summary
            NSApp.activate(ignoringOtherApps: true)
            alert.runModal()
        }
    }

    @objc private func openSettings() {
        if settingsWindow == nil {
            let w = NSWindow(
                contentRect: NSRect(x: 0, y: 0, width: 800, height: 580),
                styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
                backing: .buffered, defer: false)
            w.title = "Splitroute"
            w.toolbarStyle = .unified
            w.isReleasedWhenClosed = false
            w.delegate = self
            let host = NSHostingController(rootView: SettingsView().environmentObject(model))
            if #available(macOS 14, *) {
                // SwiftUI owns the toolbar: page title, back/forward, Liquid Glass on macOS 26.
                host.sceneBridgingOptions = [.toolbars, .title]
            } else {
                // An empty unified toolbar gives the taller title bar of System Settings.
                w.toolbar = NSToolbar(identifier: "settings")
            }
            w.contentViewController = host
            w.setContentSize(NSSize(width: 800, height: 580))
            w.center()
            w.setFrameAutosaveName("Settings")
            // Fixed width (like System Settings): clamp a wider frame saved by older versions.
            w.contentMinSize = NSSize(width: 800, height: 500)
            w.contentMaxSize = NSSize(width: 800, height: CGFloat.greatestFiniteMagnitude)
            w.setContentSize(NSSize(width: 800, height: w.contentRect(forFrameRect: w.frame).height))
            settingsWindow = w
        }
        if settingsWindow?.isVisible != true {
            UserDefaults.standard.set(Page.status.rawValue, forKey: "settingsPage")
        }
        model.settingsVisible = true
        Task { await model.loadSettings() }
        // A menu bar app (LSUIElement) is left out of Cmd+Tab and the Dock. Become a
        // regular app while the window is open, and go back to menu bar only on close.
        NSApp.setActivationPolicy(.regular)
        NSApp.activate(ignoringOtherApps: true)
        settingsWindow?.makeKeyAndOrderFront(nil)
        // Don't start in the search field; the user clicks it to search.
        DispatchQueue.main.async { self.settingsWindow?.makeFirstResponder(nil) }
    }

    func windowWillClose(_ notification: Notification) {
        model.settingsVisible = false
        NSApp.setActivationPolicy(.accessory)
    }

    /// A click outside the text field being edited ends the editing, like in
    /// System Settings: clicking a sidebar item leaves the search field. Only
    /// single-line fields (field editors); a multi-line editor keeps focus.
    private func unfocusFieldsOnOutsideClick() {
        NSEvent.addLocalMonitorForEvents(matching: .leftMouseDown) { e in
            guard let w = e.window, let editor = w.firstResponder as? NSTextView, editor.isFieldEditor,
                  let field = editor.delegate as? NSView else { return e }
            if !field.bounds.contains(field.convert(e.locationInWindow, from: nil)) {
                w.makeFirstResponder(nil)
            }
            return e
        }
    }

    /// Shown only while the window is open (regular policy): Quit goes through the
    /// routing prompt, and Edit makes Cmd+C/V/X/A/Z work in text fields.
    private func installMainMenu() {
        let main = NSMenu()
        let appMenu = NSMenu()
        appMenu.addItem(withTitle: "About Splitroute", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Hide Splitroute", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        appMenu.addItem(.separator())
        appMenu.addItem(action("Quit Splitroute", #selector(quit), key: "q"))
        let edit = NSMenu(title: "Edit")
        edit.addItem(withTitle: "Undo", action: Selector(("undo:")), keyEquivalent: "z")
        edit.addItem(withTitle: "Redo", action: Selector(("redo:")), keyEquivalent: "Z")
        edit.addItem(.separator())
        edit.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        edit.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        edit.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        edit.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        let window = NSMenu(title: "Window")
        window.addItem(withTitle: "Close", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        window.addItem(withTitle: "Minimize", action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
        for sub in [appMenu, edit, window] {
            let item = NSMenuItem()
            item.submenu = sub
            main.addItem(item)
        }
        NSApp.mainMenu = main
        NSApp.windowsMenu = window
    }
}

/// The branch symbol in the menu bar's text color, with a status dot in the
/// bottom-right corner. Drawn on demand so it follows light and dark menu bars;
/// not a template image, because a template would lose the dot's color.
func menuBarIcon(dot: NSColor) -> NSImage {
    let img = NSImage(size: NSSize(width: 18, height: 18), flipped: false) { _ in
        let cfg = NSImage.SymbolConfiguration(pointSize: 13, weight: .medium)
        if let sym = NSImage(systemSymbolName: "arrow.triangle.branch", accessibilityDescription: "Splitroute")?
            .withSymbolConfiguration(cfg) {
            let s = sym.size
            let r = NSRect(x: (16 - s.width) / 2, y: 2 + (16 - s.height) / 2, width: s.width, height: s.height)
            sym.draw(in: r)
            NSColor.labelColor.set()
            r.fill(using: .sourceAtop)
        }
        let dotRect = NSRect(x: 11, y: 0.5, width: 7, height: 7)
        // Cut a gap around the dot so it reads on any menu bar background.
        NSGraphicsContext.current?.compositingOperation = .clear
        NSBezierPath(ovalIn: dotRect.insetBy(dx: -1.5, dy: -1.5)).fill()
        NSGraphicsContext.current?.compositingOperation = .sourceOver
        dot.set()
        NSBezierPath(ovalIn: dotRect).fill()
        return true
    }
    img.isTemplate = false
    return img
}

MainActor.assumeIsolated {
    let app = NSApplication.shared
    let delegate = AppDelegate()
    app.delegate = delegate
    app.setActivationPolicy(.accessory)
    app.run()
}
