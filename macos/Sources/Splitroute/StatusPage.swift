import SwiftUI

struct StatusPage: View {
    @EnvironmentObject private var model: AppModel

    var body: some View {
        Form {
            if let s = model.status {
                Section {
                    ValueRow("Routing", s.enabled ? "On" : "Off", description: model.summary)
                    ValueRow("Gateway", s.gateway.map { "\($0.ip) on \($0.iface)" } ?? "None", description: s.gatewayError, mono: true)
                    ValueRow("VPN", s.vpn ?? "None detected")
                    ValueRow("Routes", s.routes.formatted(), description: s.missing ? "Some routes are missing from the routing table; re-applying." : nil)
                    ValueRow("Release lists downloaded", parseGoTime(s.fetched).map(relative) ?? "Never")
                    ValueRow("Service version", s.version)
                } header: {
                    Text("Connection")
                }
                if s.error != nil || !(s.warnings ?? []).isEmpty {
                    Section {
                        if let e = s.error { ErrorText(e) }
                        ForEach(s.warnings ?? [], id: \.self) { WarningText($0) }
                    } header: {
                        Text("Problems")
                    }
                }
                Section {
                    if s.lists.isEmpty { Text("No lists are routed.").foregroundStyle(.secondary) }
                    ForEach(s.lists, id: \.name) { l in
                        ValueRow(l.name, l.routes.formatted(), description: l.action.label)
                    }
                } header: {
                    Text("Routes per list")
                }
                if let imports = s.imports, !imports.isEmpty {
                    Section {
                        ForEach(imports, id: \.name) { im in
                            ValueRow(im.name, parseGoTime(im.fetchedAt).map(relative) ?? "Not downloaded",
                                     description: importDetail(im))
                        }
                    } header: {
                        Text("Imports")
                    }
                }
            } else {
                ServiceUnavailable()
            }
        }
    }

    private func importDetail(_ im: ImportStatus) -> String {
        var parts = ["\(plural(im.entries, "entry", "entries"))"]
        if let n = im.skipped, n > 0 { parts.append("\(plural(n, "line")) skipped") }
        if let c = parseGoTime(im.checkedAt) { parts.append("checked \(relative(c))") }
        if let e = im.error { parts.append("last download failed: \(e)") }
        return parts.joined(separator: " · ")
    }
}
