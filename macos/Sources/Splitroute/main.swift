import AppKit
import Combine
import SwiftUI

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private let model = AppModel()
    private var item: NSStatusItem!
    private var settingsWindow: NSWindow?
    private var bag = Set<AnyCancellable>()

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
            if model.connError == .notRunning {
                menu.addItem(action("Install Service…", #selector(installService)))
            }
        case .off:
            menu.addItem(action("Turn On", #selector(turnOn)))
        case .active, .problem:
            menu.addItem(action("Turn Off", #selector(turnOff)))
        }
        if model.serviceOutdated {
            menu.addItem(action("Update Service…", #selector(installService)))
        }
        menu.addItem(action("Settings…", #selector(openSettings), key: ","))
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
                contentRect: NSRect(x: 0, y: 0, width: 620, height: 560),
                styleMask: [.titled, .closable, .miniaturizable, .resizable],
                backing: .buffered, defer: false)
            w.title = "Splitroute Settings"
            w.isReleasedWhenClosed = false
            w.contentView = NSHostingView(rootView: SettingsView().environmentObject(model))
            w.center()
            settingsWindow = w
        }
        NSApp.activate(ignoringOtherApps: true)
        settingsWindow?.makeKeyAndOrderFront(nil)
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
