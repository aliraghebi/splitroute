import SwiftUI

/// Custom lists and imports.
struct MyListsPage: View {
    @EnvironmentObject private var model: AppModel
    @State private var editing: CustomTarget?
    @State private var importing = false
    @State private var confirmRemove: RouteList?

    private var lists: [RouteList] { model.config?.lists ?? [] }
    private var custom: [RouteList] { lists.filter { $0.custom } }
    private var imports: [RouteList] { lists.filter { $0.importSource != nil } }

    var body: some View {
        Form {
            if !model.ready {
                ServiceUnavailable()
            } else {
                customSection
                importSection
            }
        }
        .sheet(item: $editing) { t in
            CustomListEditor(original: t.list?.name, name: t.list?.name ?? "", action: t.list?.action ?? .direct,
                             text: (t.list?.entries ?? []).joined(separator: "\n"),
                             takenNames: Set(lists.map(\.name)).subtracting([t.list?.name ?? ""]))
                .environmentObject(model)
        }
        .sheet(isPresented: $importing) {
            ImportEditor(takenNames: Set(lists.map(\.name))).environmentObject(model)
        }
        .confirmationDialog("Remove \(confirmRemove?.name ?? "")?", isPresented: Binding(
            get: { confirmRemove != nil }, set: { if !$0 { confirmRemove = nil } }
        ), presenting: confirmRemove) { l in
            Button("Remove", role: .destructive) { Task { await model.removeList(l.name) } }
        } message: { l in
            Text("Its \(plural(l.entries.filter { !$0.hasPrefix("#") }.count, "entry", "entries")) are deleted. Export the settings first (splitroute config export) to keep a copy.")
        }
    }

    private var customSection: some View {
        Section {
            if custom.isEmpty {
                Text("Your own addresses, ranges, domains and AS numbers, routed like a release list.").foregroundStyle(.secondary)
            }
            ForEach(custom) { l in
                SettingRow(l.name, description: customLine(l), error: model.error("list:\(l.name)")) {
                    ActionPicker(action: l.action) { a in Task { await model.setAction(l.name, a) } }
                    RowButton(symbol: "pencil", help: "Edit \(l.name)") { editing = CustomTarget(list: l) }
                    RowButton(symbol: "minus.circle", help: "Remove \(l.name)") {
                        if l.entries.isEmpty { Task { await model.removeList(l.name) } } else { confirmRemove = l }
                    }
                }
            }
            HStack {
                Spacer()
                Button("New List…") { editing = CustomTarget(list: nil) }
            }
        } header: {
            Text("Custom lists")
        }
    }

    private var importSection: some View {
        Section {
            if imports.isEmpty {
                Text("Lists downloaded from an https URL (one entry per line, hosts files or domain sets) and refreshed daily.")
                    .foregroundStyle(.secondary)
            }
            ForEach(imports) { l in
                let st = model.status?.imports?.first { $0.name == l.name }
                SettingRow(l.name, description: importLine(l, st), error: model.error("list:\(l.name)")) {
                    ActionPicker(action: l.action) { a in Task { await model.setAction(l.name, a) } }
                    RowButton(symbol: "arrow.clockwise", help: "Download \(l.name) again") {
                        Task { await model.refreshImport(l.name) }
                    }
                    RowButton(symbol: "minus.circle", help: "Remove \(l.name)") {
                        Task { await model.removeList(l.name) }
                    }
                }
                if let e = st?.error { WarningText("Last download failed: \(e)") }
            }
            HStack {
                Spacer()
                Button("Import from URL…") { importing = true }
            }
        } header: {
            Text("Imports")
        } footer: {
            ActionsFooter()
        }
    }

    private func customLine(_ l: RouteList) -> String {
        let n = l.entries.filter { !$0.trimmingCharacters(in: .whitespaces).hasPrefix("#") }.count
        return ([plural(n, "entry", "entries")] + [routesLine(model, l.name)].compactMap { $0 }).joined(separator: " · ")
    }

    private func importLine(_ l: RouteList, _ st: ImportStatus?) -> String {
        var parts = [l.importSource?.url ?? ""]
        if let st {
            if let d = parseGoTime(st.fetchedAt) {
                parts.append("\(plural(st.entries, "entry", "entries")), downloaded \(relative(d))")
            } else {
                parts.append("Not downloaded yet")
            }
        }
        if let r = routesLine(model, l.name) { parts.append(r) }
        return parts.joined(separator: "\n")
    }
}

struct CustomTarget: Identifiable {
    let id = UUID()
    let list: RouteList? // nil: a new list
}

/// Creates or edits a custom list. Saving sends the changes to the service and
/// closes only when it accepted them; its error stays in the sheet.
struct CustomListEditor: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @State var original: String?
    @State var name: String
    @State var action: Action
    @State var text: String
    let takenNames: Set<String>
    @State private var error: String?

    private var trimmedName: String { name.trimmingCharacters(in: .whitespaces) }

    private var nameProblem: String? {
        if trimmedName.isEmpty { return "Enter a name." }
        if trimmedName.count > 64 { return "Use at most 64 characters." }
        if takenNames.contains(trimmedName) { return "Another list already has this name." }
        return nil
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Form {
                Section {
                    TextField("Name", text: $name)
                    Picker("Action", selection: $action) {
                        ForEach(Action.allCases) { Text($0.label).tag($0) }
                    }
                } header: {
                    Text(original == nil ? "New Custom List" : "Edit \(original!)").font(.headline)
                }
                Section {
                    TextEditor(text: $text)
                        .font(.system(.body, design: .monospaced))
                        .frame(minHeight: 200)
                } header: {
                    Text("Entries")
                } footer: {
                    Text("One per line: an address or range (203.0.113.7, 198.51.100.0/24, 2400:cb00::/32), a domain with its subdomains (example.com), one exact name (full:www.example.com) or an AS number (AS13335). Lines starting with # are notes. Only IPv4 is routed for now; the rest is kept for upcoming domain and ASN routing.")
                        .font(.caption).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .formStyle(.grouped)
            VStack(alignment: .leading, spacing: 8) {
                if let e = error ?? (name.isEmpty ? nil : nameProblem) { ErrorText(e) }
                HStack {
                    Spacer()
                    if model.busy { ProgressView().controlSize(.small) }
                    Button("Cancel", role: .cancel) { dismiss() }
                        .keyboardShortcut(.cancelAction)
                    Button(original == nil ? "Add" : "Save") { Task { await save() } }
                        .keyboardShortcut(.defaultAction)
                        .prominent()
                        .disabled(nameProblem != nil || model.busy)
                }
            }
            .padding(.horizontal, 20)
            .padding(.bottom, 16)
        }
        .frame(width: 520, height: 520)
    }

    private func save() async {
        let entries = text.split(whereSeparator: \.isNewline)
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        let r = await model.saveCustom(original: original, name: trimmedName, action: action, entries: entries)
        original = r.name
        error = r.error
        if r.error == nil { dismiss() }
    }
}

struct ImportEditor: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss
    let takenNames: Set<String>
    @State private var name = ""
    @State private var url = ""
    @State private var action: Action = .block
    @State private var error: String?

    private var trimmedName: String { name.trimmingCharacters(in: .whitespaces) }
    private var trimmedURL: String { url.trimmingCharacters(in: .whitespaces) }

    private var problem: String? {
        if trimmedName.isEmpty || trimmedURL.isEmpty { return "" }
        if trimmedName.count > 64 { return "Use at most 64 characters for the name." }
        if takenNames.contains(trimmedName) { return "Another list already has this name." }
        guard let u = URLComponents(string: trimmedURL), u.scheme?.lowercased() == "https", u.host?.isEmpty == false else {
            return "Enter an https URL."
        }
        if u.user != nil || u.password != nil { return "Remove the user name and password from the URL." }
        return nil
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Form {
                Section {
                    TextField("Name", text: $name)
                    TextField("URL", text: $url, prompt: Text("https://example.com/hosts.txt"))
                    Picker("Action", selection: $action) {
                        ForEach(Action.allCases) { Text($0.label).tag($0) }
                    }
                } header: {
                    Text("Import from URL").font(.headline)
                } footer: {
                    Text("Plain lists (one entry per line), hosts files and Surge/Clash domain sets. The service downloads it now and every day; a failed download keeps the last good copy. Every user of this Mac can see the URL, so don't use one that contains a token or password.")
                        .font(.caption).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .formStyle(.grouped)
            VStack(alignment: .leading, spacing: 8) {
                if let e = error ?? problem, !e.isEmpty { ErrorText(e) }
                HStack {
                    Spacer()
                    if model.busy { ProgressView().controlSize(.small) }
                    Button("Cancel", role: .cancel) { dismiss() }
                        .keyboardShortcut(.cancelAction)
                    Button("Import") {
                        Task {
                            error = await model.importList(name: trimmedName, url: trimmedURL, action: action)
                            if error == nil { dismiss() }
                        }
                    }
                    .keyboardShortcut(.defaultAction)
                    .prominent()
                    .disabled(problem != nil || model.busy)
                }
            }
            .padding(.horizontal, 20)
            .padding(.bottom, 16)
        }
        .frame(width: 520, height: 360)
    }
}
