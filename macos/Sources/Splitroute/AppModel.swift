import AppKit
import Foundation
import ServiceManagement

@MainActor
final class AppModel: ObservableObject {
    enum Health { case active, off, problem, notRunning }

    @Published private(set) var status: Status?
    @Published private(set) var connError: DaemonError?
    @Published private(set) var config: Config?
    @Published private(set) var presets: [Preset] = []
    @Published private(set) var available: [String] = []
    @Published var actionError: String?
    @Published private(set) var busy = false

    private static let pollInterval: UInt64 = 5_000_000_000

    init() {
        Task { [weak self] in
            while let self {
                await self.refreshStatus()
                await self.autoInstallIfNeeded()
                await self.turnOnAtOpenIfNeeded()
                try? await Task.sleep(nanoseconds: Self.pollInterval)
            }
        }
    }

    var health: Health {
        guard connError == nil, let s = status else { return .notRunning }
        if !s.enabled { return .off }
        if s.error != nil || s.missing || s.routes == 0 { return .problem }
        return .active
    }

    var summary: String {
        switch health {
        case .notRunning: return connError?.localizedDescription ?? "Connecting…"
        case .off: return "Turned off"
        case .active: return "Active: \(status!.routes.formatted()) routes"
        case .problem:
            guard let s = status else { return "" }
            if let e = s.error { return "Problem: \(e)" }
            if s.missing { return "Routes are missing; re-applying…" }
            return "No routes applied"
        }
    }

    static let appVersion = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"

    /// The running service is a different build than this app (after an upgrade
    /// the old daemon keeps running until it is reinstalled).
    var serviceOutdated: Bool {
        guard let v = status?.version, Self.appVersion != "dev" else { return false }
        return v != Self.appVersion
    }

    var networkLine: String? {
        guard let s = status else { return nil }
        let gw = s.gateway.map { "Gateway \($0.ip) on \($0.iface)" } ?? "No gateway"
        return gw + " · " + (s.vpn.map { "VPN \($0)" } ?? "no VPN detected")
    }

    func refreshStatus() async {
        do {
            let s = try await Daemon.status()
            if s != status { status = s }
            if connError != nil { connError = nil }
        } catch let e as DaemonError {
            if connError != e { connError = e }
            status = nil
        } catch {
            connError = .failed(error.localizedDescription)
        }
    }

    /// Loads what the settings window needs; the release list names need the network.
    func loadSettings() async {
        await perform {
            self.config = try await Daemon.config()
            self.presets = try await Daemon.presets()
        }
        if let names = try? await Daemon.lists() { available = names }
    }

    func setEnabled(_ on: Bool) async {
        await perform { self.config = try await Daemon.setEnabled(on) }
    }

    func save(_ c: Config) async {
        await perform { self.config = try await Daemon.setConfig(c) }
    }

    func applyPreset(_ id: String) async {
        await perform { self.config = try await Daemon.applyPreset(id) }
    }

    private func perform(_ work: @escaping () async throws -> Void) async {
        busy = true
        defer { busy = false }
        do {
            try await work()
            actionError = nil
        } catch {
            actionError = error.localizedDescription
        }
        await refreshStatus()
    }

    // MARK: Service install (root, via the standard admin password prompt)

    /// The daemon binary shipped inside the app bundle.
    private var daemonPath: String? { Bundle.main.path(forResource: "splitroute", ofType: nil) }

    private static let servicePlist = "/Library/LaunchDaemons/io.github.aliraghebi.splitroute.plist"
    private static let promptedKey = "autoInstallPromptedVersion"
    private static let uninstalledKey = "serviceUninstalledByUser"

    func installService() async {
        let fresh = !FileManager.default.fileExists(atPath: Self.servicePlist)
        UserDefaults.standard.removeObject(forKey: Self.uninstalledKey)
        await runPrivileged("install", prompt: "Splitroute needs your password to install its routing service.")
        // On a first install, open at login so the routing state stays visible
        // after a reboot. Not on reinstalls: the user may have turned it off.
        if fresh, connError == nil, !launchAtLogin { setLaunchAtLogin(true) }
    }

    func uninstallService() async {
        UserDefaults.standard.set(true, forKey: Self.uninstalledKey)
        await runPrivileged("uninstall", prompt: "Splitroute needs your password to remove its routing service.")
    }

    /// Asks for the password without a click: on first launch when the service
    /// was never installed, and after an app upgrade while the old daemon runs.
    /// At most once per app version, so a canceled prompt doesn't return on every
    /// launch. The plist check keeps a daemon that is still starting at login from
    /// triggering it, and a service the user removed stays removed.
    private func autoInstallIfNeeded() async {
        let d = UserDefaults.standard
        guard !busy, d.string(forKey: Self.promptedKey) != Self.appVersion else { return }
        let neverInstalled = connError == .notRunning
            && !FileManager.default.fileExists(atPath: Self.servicePlist)
            && !d.bool(forKey: Self.uninstalledKey)
        guard neverInstalled || serviceOutdated else { return }
        d.set(Self.appVersion, forKey: Self.promptedKey)
        await installService()
    }

    private var openChecked = false

    /// "Turn on when the app opens": once per launch, at the first status from the
    /// service. Only once, so turning routing off later in the session sticks.
    private func turnOnAtOpenIfNeeded() async {
        guard !openChecked, let s = status else { return }
        openChecked = true
        if !s.enabled, UserDefaults.standard.bool(forKey: "turnOnAtOpen") { await setEnabled(true) }
    }

    private func runPrivileged(_ command: String, prompt: String) async {
        guard let exe = daemonPath else {
            actionError = "The splitroute binary is missing from the app bundle."
            return
        }
        busy = true
        defer { busy = false }
        // `install` points the service at this binary, so it follows app updates
        // as long as the app stays at the same path.
        let shell = "'\(exe.replacingOccurrences(of: "'", with: "'\\''"))' \(command)"
        let script = "do shell script \"\(shell.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\""))\" with prompt \"\(prompt)\" with administrator privileges"
        let result: (Int32, String) = await Task.detached {
            let p = Process()
            p.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
            p.arguments = ["-e", script]
            let err = Pipe()
            p.standardError = err
            p.standardOutput = Pipe()
            do { try p.run() } catch { return (-1, error.localizedDescription) }
            p.waitUntilExit()
            let msg = String(data: err.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
            return (p.terminationStatus, msg)
        }.value
        if result.0 != 0 && !result.1.contains("User canceled") {
            actionError = result.1.trimmingCharacters(in: .whitespacesAndNewlines)
        } else {
            actionError = nil
        }
        // The service needs a moment to open its socket.
        try? await Task.sleep(nanoseconds: 1_500_000_000)
        await refreshStatus()
        if command == "install", connError == nil { await loadSettings() }
        if command == "uninstall" { config = nil }
    }

    // MARK: Launch at login

    var launchAtLogin: Bool { SMAppService.mainApp.status == .enabled }

    func setLaunchAtLogin(_ on: Bool) {
        do {
            if on { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
            actionError = nil
        } catch {
            actionError = "Launch at login: \(error.localizedDescription)"
        }
        objectWillChange.send()
    }
}
