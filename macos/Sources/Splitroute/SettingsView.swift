import SwiftUI

/// Edits a draft of the daemon's config; nothing changes until Apply.
struct SettingsView: View {
    @EnvironmentObject private var model: AppModel
    @State private var draft = Config()
    @State private var presetID = ""
    @State private var confirmPreset = false
    @State private var editor: EditorTarget?
    @AppStorage("quitAction") private var quitAction = "" // read by AppDelegate.quit
    @AppStorage("turnOnAtOpen") private var turnOnAtOpen = false // read by AppModel

    private var dirty: Bool { model.config.map { $0 != draft } ?? false }

    var body: some View {
        Form {
            statusSection
            if model.config == nil {
                notRunningSection
            } else {
                presetSection
                listsSection
                generalSection
            }
        }
        .formStyle(.grouped)
        .applyBar(visible: model.config != nil) { applyBar }
        .frame(minWidth: 560, minHeight: 480)
        .task { await model.loadSettings() }
        .onReceive(model.$config) { if let c = $0 { draft = c } }
        .sheet(item: $editor) { target in
            CustomListEditor(
                list: target.list,
                isNew: target.index == nil,
                takenNames: Set(draft.lists.map(\.name)).subtracting([target.list.name])
            ) { saved in
                if let i = target.index { draft.lists[i] = saved } else { draft.lists.append(saved) }
            }
        }
    }

    // MARK: Sections

    private var statusSection: some View {
        Section {
            HStack {
                Circle().fill(dotColor).frame(width: 10, height: 10)
                VStack(alignment: .leading, spacing: 2) {
                    Text(model.summary).font(.headline)
                    if let line = model.networkLine {
                        Text(line).font(.caption).foregroundStyle(.secondary)
                    }
                }
                Spacer()
                if model.status != nil {
                    Toggle("Enabled", isOn: Binding(
                        get: { model.status?.enabled ?? false },
                        set: { on in Task { await model.setEnabled(on) } }
                    ))
                    .labelsHidden()
                    .toggleStyle(.switch)
                    .disabled(model.busy)
                }
            }
            if model.serviceOutdated, let v = model.status?.version {
                HStack {
                    Label("The service is version \(v); this app is \(AppModel.appVersion).", systemImage: "arrow.triangle.2.circlepath")
                    Spacer()
                    Button("Update Service") { Task { await model.installService() } }
                        .disabled(model.busy)
                }
            }
            ForEach(model.status?.warnings ?? [], id: \.self) { w in
                Label(w, systemImage: "exclamationmark.triangle").foregroundStyle(.orange)
            }
        }
    }

    private var notRunningSection: some View {
        Section {
            Text("Splitroute runs as a system service so it can change routes. Installing it asks for your administrator password once.")
                .fixedSize(horizontal: false, vertical: true)
            if model.connError == .notRunning {
                Button("Install Service") { Task { await model.installService() } }
                    .prominent()
                    .disabled(model.busy)
            }
            errorText
        }
    }

    private var presetSection: some View {
        Section {
            Picker("Preset", selection: $presetID) {
                Text("Choose…").tag("")
                ForEach(model.presets) { Text($0.name).tag($0.id) }
            }
            if let p = model.presets.first(where: { $0.id == presetID }) {
                Text(p.lists.map { "\($0.action.label): \($0.name)" }.joined(separator: " · "))
                    .font(.caption).foregroundStyle(.secondary)
            }
            HStack {
                Spacer()
                Button("Apply Preset…") { confirmPreset = true }
                    .disabled(presetID.isEmpty || model.busy)
            }
        } header: {
            Text("Preset")
        } footer: {
            Text("A preset replaces the release lists. Your custom lists and imports are kept.")
                .font(.caption).foregroundStyle(.secondary)
        }
        .confirmationDialog("Apply this preset?", isPresented: $confirmPreset) {
            Button("Apply") { Task { await model.applyPreset(presetID) } }
        } message: {
            Text(dirty ? "Your unapplied changes will be lost." : "The current release lists will be replaced.")
        }
    }

    private var listsSection: some View {
        Section {
            if draft.lists.isEmpty {
                Text("No lists: all traffic follows the VPN.").foregroundStyle(.secondary)
            }
            ForEach(Array(draft.lists.enumerated()), id: \.element.id) { i, l in
                row(l, index: i)
            }
            HStack {
                Menu("Add Release List") {
                    let missing = model.available.filter { n in !draft.lists.contains { $0.name == n } }
                    if missing.isEmpty { Text(model.available.isEmpty ? "Can't reach the release" : "All lists added") }
                    ForEach(missing, id: \.self) { n in
                        Button(n) { draft.lists.append(RouteList(name: n, action: .direct)) }
                    }
                }
                .fixedSize()
                Button("New Custom List…") {
                    editor = EditorTarget(index: nil, list: RouteList(name: "", action: .direct, custom: true))
                }
                Spacer()
            }
        } header: {
            Text("Lists")
        } footer: {
            Text("Direct: through your normal connection, outside the VPN. Block: dropped. VPN: left alone. Only IPv4 ranges are routed for now; domain-only lists (ads, nsfw) have no effect yet.")
                .font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private var generalSection: some View {
        Section {
            Toggle("Open at login", isOn: Binding(
                get: { model.launchAtLogin },
                set: { model.setLaunchAtLogin($0) }
            ))
            Toggle("Turn on routing when the app opens", isOn: $turnOnAtOpen)
            Picker("When quitting while routing", selection: $quitAction) {
                Text("Ask").tag("")
                Text("Turn off routing").tag("turnOff")
                Text("Keep routing").tag("keep")
            }
            LabeledContent("System service") {
                Button("Uninstall…") { Task { await model.uninstallService() } }
                    .disabled(model.busy)
            }
        } footer: {
            Text("The service runs on its own: with Keep routing, quitting only closes the menu bar icon.")
                .font(.caption).foregroundStyle(.secondary)
        }
    }

    private var applyBar: some View {
        HStack {
            errorText
            Spacer()
            if model.busy { ProgressView().controlSize(.small) }
            Button("Revert") { if let c = model.config { draft = c } }
                .disabled(!dirty || model.busy)
            Button("Apply") { Task { await model.save(draft) } }
                .keyboardShortcut(.defaultAction)
                .prominent()
                .disabled(!dirty || model.busy)
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 12)
    }

    private func row(_ l: RouteList, index i: Int) -> some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text(l.name)
                Text(subtitle(l)).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            Picker("Action", selection: $draft.lists[i].action) {
                ForEach(Action.allCases) { Text($0.label).tag($0) }
            }
            .labelsHidden()
            .frame(width: 100)
            if l.custom {
                Button { editor = EditorTarget(index: i, list: l) } label: { Image(systemName: "pencil") }
                    .buttonStyle(.borderless)
                    .help("Edit")
            }
            Button { draft.lists.remove(at: i) } label: { Image(systemName: "trash") }
                .buttonStyle(.borderless)
                .help("Remove")
        }
        .padding(.vertical, 2)
    }

    private func subtitle(_ l: RouteList) -> String {
        var parts: [String]
        if l.importSource != nil {
            let st = model.status?.imports?.first(where: { $0.name == l.name })
            parts = [st.map { "Import, \($0.entries.formatted()) entries" + ($0.error != nil ? " (last download failed)" : "") } ?? "Import"]
        } else {
            parts = [l.custom ? "Custom, \(l.entries.count) \(l.entries.count == 1 ? "entry" : "entries")" : "Release list"]
        }
        if let n = model.status?.lists.first(where: { $0.name == l.name })?.routes {
            parts.append("\(n.formatted()) routes applied")
        }
        return parts.joined(separator: " · ")
    }

    @ViewBuilder private var errorText: some View {
        if let e = model.actionError {
            Text(e).foregroundStyle(.red).font(.callout).textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private var dotColor: Color {
        switch model.health {
        case .active: return .green
        case .problem: return .orange
        case .off, .notRunning: return .red
        }
    }
}

struct EditorTarget: Identifiable {
    let id = UUID()
    let index: Int?
    let list: RouteList
}

struct CustomListEditor: View {
    @State var list: RouteList
    let isNew: Bool
    let takenNames: Set<String>
    let onSave: (RouteList) -> Void
    @State private var text = ""
    @Environment(\.dismiss) private var dismiss

    private var trimmedName: String { list.name.trimmingCharacters(in: .whitespaces) }

    private var nameProblem: String? {
        if trimmedName.isEmpty { return "Enter a name." }
        if trimmedName.count > 64 { return "Use at most 64 characters." }
        if takenNames.contains(trimmedName) { return "Another list already has this name." }
        return nil
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(isNew ? "New Custom List" : "Edit Custom List").font(.headline)
            Form {
                TextField("Name", text: $list.name)
                Picker("Action", selection: $list.action) {
                    ForEach(Action.allCases) { Text($0.label).tag($0) }
                }
            }
            Text("One per line: an IP address or range (203.0.113.7, 198.51.100.0/24, 2400:cb00::/32), a domain with its subdomains (example.com), one exact name (full:www.example.com) or an AS number (AS13335). Lines starting with # are notes. Only IPv4 is routed for now; the rest is saved for upcoming domain and ASN routing.")
                .font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            TextEditor(text: $text)
                .font(.system(.body, design: .monospaced))
                .frame(minHeight: 180)
                .border(Color.secondary.opacity(0.3))
            if let p = nameProblem, !list.name.isEmpty {
                Text(p).font(.caption).foregroundStyle(.red)
            }
            HStack {
                Spacer()
                Button("Cancel", role: .cancel) { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button(isNew ? "Add" : "Done") {
                    list.name = trimmedName
                    list.entries = text.split(whereSeparator: \.isNewline)
                        .map { $0.trimmingCharacters(in: .whitespaces) }
                        .filter { !$0.isEmpty }
                    onSave(list)
                    dismiss()
                }
                .keyboardShortcut(.defaultAction)
                .prominent()
                .disabled(nameProblem != nil)
            }
            Text("Entries are checked when you apply the settings.")
                .font(.caption).foregroundStyle(.secondary)
        }
        .padding(20)
        .frame(width: 460)
        .onAppear { text = list.entries.joined(separator: "\n") }
    }
}

extension View {
    /// Liquid Glass on macOS 26+, the classic prominent style before.
    @ViewBuilder func prominent() -> some View {
        if #available(macOS 26, *) { buttonStyle(.glassProminent) } else { buttonStyle(.borderedProminent) }
    }

    /// A bottom bar over the scroll view: on macOS 26+ it gets the glass scroll edge effect.
    @ViewBuilder func applyBar(visible: Bool, @ViewBuilder _ bar: () -> some View) -> some View {
        if !visible {
            self
        } else if #available(macOS 26, *) {
            safeAreaBar(edge: .bottom) { bar() }
        } else {
            safeAreaInset(edge: .bottom, spacing: 0) { bar().background(.bar) }
        }
    }
}
