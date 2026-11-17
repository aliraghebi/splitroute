import SwiftUI

/// Release lists from routing-rules.
struct ListsPage: View {
    @EnvironmentObject private var model: AppModel

    private var release: [RouteList] {
        (model.config?.lists ?? []).filter { !$0.custom && $0.importSource == nil }
    }

    private var missing: [String] {
        model.available.filter { n in !(model.config?.lists ?? []).contains { $0.name == n } }
    }

    var body: some View {
        Form {
            PageHeader(page: .lists)
            if !model.ready {
                ServiceUnavailable()
            } else {
                Section {
                    if release.isEmpty {
                        Text("No release lists: traffic for these ranges follows the VPN.").foregroundStyle(.secondary)
                    }
                    ForEach(release) { l in
                        SettingRow(l.name, description: routesLine(model, l.name), error: model.error("list:\(l.name)")) {
                            ActionPicker(action: l.action) { a in Task { await model.setAction(l.name, a) } }
                            RowButton(symbol: "minus.circle", help: "Remove \(l.name)") {
                                Task { await model.removeList(l.name) }
                            }
                        }
                    }
                } header: {
                    Text("Enabled")
                } footer: {
                    ActionsFooter()
                }
                Section {
                    SettingRow("Add a list", description: addDescription, error: model.error("lists")) {
                        Menu("Add") {
                            ForEach(missing, id: \.self) { n in
                                Button(n) { Task { await model.enableRelease(n) } }
                            }
                        }
                        .fixedSize()
                        .disabled(missing.isEmpty || model.busy)
                    }
                }
            }
        }
    }

    private var addDescription: String {
        if model.available.isEmpty { return "Can't reach the latest release to see which lists it has." }
        if missing.isEmpty { return "All \(model.available.count) lists of the latest release are added." }
        return "\(plural(missing.count, "more list")) in the latest release. New lists start as Direct."
    }
}

/// "1,234 routes applied", or nothing when the list has none.
@MainActor func routesLine(_ model: AppModel, _ name: String) -> String? {
    guard let n = model.status?.lists.first(where: { $0.name == name })?.routes, n > 0 else { return nil }
    return "\(plural(n, "route")) applied"
}

struct ActionsFooter: View {
    var body: some View {
        Text("Direct: through your normal connection, outside the VPN. Block: dropped. VPN: left alone. Changes apply right away. Only IPv4 ranges are routed for now; domain-only lists have no effect yet.")
            .font(.caption)
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }
}
