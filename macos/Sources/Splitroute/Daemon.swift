import Foundation

// Wire types: mirror internal/config/config.go, internal/api/api.go and internal/api/status.go.

enum Action: String, Codable, CaseIterable, Identifiable {
    case direct, block, vpn
    var id: String { rawValue }
    var label: String {
        switch self {
        case .direct: return "Direct"
        case .block: return "Block"
        case .vpn: return "VPN"
        }
    }
}

struct RouteList: Codable, Hashable, Identifiable {
    var name: String
    var action: Action
    var custom = false
    var entries: [String] = []
    /// Set for a list downloaded from a URL. Its entries live in the daemon's
    /// database, never in the config. Must survive set_config round trips.
    var importSource: ImportSource?
    var id: String { name }

    enum CodingKeys: String, CodingKey {
        case name, action, custom, entries, importSource = "import"
    }

    init(name: String, action: Action, custom: Bool = false, entries: [String] = []) {
        self.name = name
        self.action = action
        self.custom = custom
        self.entries = entries
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decode(String.self, forKey: .name)
        action = try c.decode(Action.self, forKey: .action)
        custom = try c.decodeIfPresent(Bool.self, forKey: .custom) ?? false
        entries = try c.decodeIfPresent([String].self, forKey: .entries) ?? []
        importSource = try c.decodeIfPresent(ImportSource.self, forKey: .importSource)
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(name, forKey: .name)
        try c.encode(action, forKey: .action)
        if custom { try c.encode(custom, forKey: .custom) }
        if !entries.isEmpty { try c.encode(entries, forKey: .entries) }
        try c.encodeIfPresent(importSource, forKey: .importSource)
    }
}

struct ImportSource: Codable, Hashable {
    var url: String
    var refreshHours: Int?

    enum CodingKeys: String, CodingKey {
        case url, refreshHours = "refresh_hours"
    }
}

struct ImportStatus: Decodable, Equatable {
    var name: String
    var url: String
    var entries: Int
    var skipped: Int?
    var fetchedAt: String?
    var checkedAt: String?
    var error: String?

    enum CodingKeys: String, CodingKey {
        case name, url, entries, skipped, fetchedAt = "fetched_at", checkedAt = "checked_at", error
    }
}

struct Config: Codable, Equatable {
    var baseURL: String?
    var disabled = false
    var lists: [RouteList] = []

    enum CodingKeys: String, CodingKey {
        case baseURL = "base_url", disabled, lists
    }

    init() {}

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        baseURL = try c.decodeIfPresent(String.self, forKey: .baseURL)
        disabled = try c.decodeIfPresent(Bool.self, forKey: .disabled) ?? false
        lists = try c.decodeIfPresent([RouteList].self, forKey: .lists) ?? []
    }
}

struct Gateway: Decodable, Equatable {
    var ip: String
    var iface: String
}

struct ListCount: Decodable, Equatable {
    var name: String
    var action: Action
    var routes: Int
}

struct Status: Decodable, Equatable {
    var version: String
    var enabled: Bool
    var gateway: Gateway?
    var gatewayError: String?
    var vpn: String?
    var routes: Int
    var lists: [ListCount]
    var fetched: String
    var missing: Bool
    var warnings: [String]?
    var error: String?
    var imports: [ImportStatus]?

    enum CodingKeys: String, CodingKey {
        case version, enabled, gateway, gatewayError = "gateway_error", vpn, routes, lists, fetched, missing, warnings, error, imports
    }
}

struct Preset: Decodable, Identifiable, Hashable {
    var id: String
    var name: String
    var lists: [RouteList]
}

private struct Request: Encodable {
    var cmd: String
    var config: Config?
    var enabled: Bool?
    var preset: String?
    var name: String?
    var newName: String?
    var action: Action?
    var position: Int?
    var entries: [String]?
    var url: String?

    enum CodingKeys: String, CodingKey {
        case cmd, config, enabled, preset, name, newName = "new_name", action, position, entries, url
    }
}

/// Entry counts of an entries_* edit.
struct EntryEdit: Decodable, Equatable {
    var added: Int?
    var duplicates: Int?
    var removed: Int?
    var notFound: [String]?

    enum CodingKeys: String, CodingKey {
        case added, duplicates, removed, notFound = "not_found"
    }
}

private struct Response: Decodable {
    var ok: Bool
    var error: String?
    var status: Status?
    var config: Config?
    var lists: [String]?
    var presets: [Preset]?
    var edit: EntryEdit?
}

enum DaemonError: LocalizedError, Equatable {
    case notRunning
    case denied
    case failed(String)

    var errorDescription: String? {
        switch self {
        case .notRunning: return "The Splitroute service is not running."
        case .denied: return "Only administrator accounts can control Splitroute."
        case .failed(let msg): return msg
        }
    }
}

/// Client for the daemon's control socket: one JSON request, one JSON response per connection.
enum Daemon {
    #if DEBUG
    /// Debug builds: SPLITROUTE_SOCKET points the app at a fake service.
    static let socketPath = ProcessInfo.processInfo.environment["SPLITROUTE_SOCKET"] ?? "/var/run/splitroute.sock"
    #else
    static let socketPath = "/var/run/splitroute.sock"
    #endif

    static func status() async throws -> Status { try await send(Request(cmd: "status")).status! }
    static func config() async throws -> Config { try await send(Request(cmd: "get_config")).config! }
    static func setConfig(_ c: Config) async throws -> Config { try await send(Request(cmd: "set_config", config: c)).config! }
    static func setEnabled(_ on: Bool) async throws -> Config { try await send(Request(cmd: "set_enabled", enabled: on)).config! }
    static func applyPreset(_ id: String) async throws -> Config { try await send(Request(cmd: "apply_preset", preset: id)).config! }
    static func lists() async throws -> [String] { try await send(Request(cmd: "lists")).lists ?? [] }
    static func presets() async throws -> [Preset] { try await send(Request(cmd: "presets")).presets ?? [] }

    /// A per-list edit (list_create, list_enable, list_import, list_remove, list_rename,
    /// list_set_action, list_move, entries_set): the daemon applies it to its current
    /// config in one transaction, so edits from the CLI meanwhile are kept.
    static func edit(_ cmd: String, name: String, newName: String? = nil, action: Action? = nil,
                     position: Int? = nil, entries: [String]? = nil, url: String? = nil) async throws -> Config {
        let r = try await send(Request(cmd: cmd, name: name, newName: newName, action: action,
                                       position: position, entries: entries, url: url))
        guard let c = r.config else { throw DaemonError.failed("The service is a different version; reinstall it from Settings.") }
        return c
    }

    static func refreshImport(_ name: String) async throws { _ = try await send(Request(cmd: "list_refresh", name: name)) }

    private static func send(_ req: Request) async throws -> Response {
        let payload = try JSONEncoder().encode(req) + Data("\n".utf8)
        let data = try await Task.detached { try roundTrip(payload) }.value
        let resp: Response
        do {
            resp = try JSONDecoder().decode(Response.self, from: data)
        } catch {
            throw DaemonError.failed("Unexpected reply from the service: \(error.localizedDescription)")
        }
        guard resp.ok else { throw DaemonError.failed(resp.error ?? "unknown error") }
        // Every successful reply to these commands carries its payload; a missing
        // one means a daemon/app version mismatch.
        switch req.cmd {
        case "status" where resp.status == nil, "get_config" where resp.config == nil,
             "set_config" where resp.config == nil, "set_enabled" where resp.config == nil,
             "apply_preset" where resp.config == nil:
            throw DaemonError.failed("The service is a different version; reinstall it from Settings.")
        default:
            return resp
        }
    }

    /// Blocking Unix-socket round trip; runs off the main thread.
    private static func roundTrip(_ payload: Data) throws -> Data {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw posixError("socket") }
        defer { close(fd) }

        var on: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &on, socklen_t(MemoryLayout<Int32>.size))
        var tv = timeval(tv_sec: 95, tv_usec: 0) // the daemon allows 90 s ("lists" downloads)
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
        setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))

        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let path = Array(socketPath.utf8)
        withUnsafeMutableBytes(of: &addr.sun_path) { buf in
            for (i, b) in path.prefix(buf.count - 1).enumerated() { buf[i] = b }
        }
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        if rc != 0 {
            switch errno {
            case ENOENT, ECONNREFUSED: throw DaemonError.notRunning
            case EACCES, EPERM: throw DaemonError.denied
            default: throw posixError("connect")
            }
        }

        try payload.withUnsafeBytes { (buf: UnsafeRawBufferPointer) in
            var off = 0
            while off < buf.count {
                let n = write(fd, buf.baseAddress! + off, buf.count - off)
                if n < 0 { throw posixError("write") }
                off += n
            }
        }

        var out = Data()
        var chunk = [UInt8](repeating: 0, count: 64 * 1024)
        while true {
            let n = read(fd, &chunk, chunk.count)
            if n > 0 {
                out.append(chunk, count: n)
            } else if n == 0 {
                return out
            } else {
                throw posixError("read")
            }
        }
    }

    private static func posixError(_ op: String) -> DaemonError {
        .failed("\(op): \(String(cString: strerror(errno)))")
    }
}
