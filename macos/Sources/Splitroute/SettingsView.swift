import SwiftUI

enum Page: String, CaseIterable, Identifiable {
    case general, lists, myLists, status, advanced, about
    var id: String { rawValue }

    var title: String {
        switch self {
        case .general: return "General"
        case .lists: return "Lists"
        case .myLists: return "My Lists"
        case .status: return "Status"
        case .advanced: return "Advanced"
        case .about: return "About"
        }
    }

    var subtitle: String {
        switch self {
        case .general: return "Turn routing on or off, pick a preset, and choose how the app starts and quits."
        case .lists: return "Ready-made lists from routing-rules, rebuilt daily."
        case .myLists: return "Your own lists, and lists downloaded from a URL."
        case .status: return "What the service sees and the routes it has applied."
        case .advanced: return "List source, logs and the system service."
        case .about: return "Version and links."
        }
    }

    var symbol: String {
        switch self {
        case .general: return "gearshape.fill"
        case .lists: return "list.bullet.rectangle.fill"
        case .myLists: return "person.crop.rectangle.stack.fill"
        case .status: return "waveform.path.ecg"
        case .advanced: return "wrench.and.screwdriver.fill"
        case .about: return "info.circle.fill"
        }
    }

    var color: Color {
        switch self {
        case .general: return .gray
        case .lists: return .blue
        case .myLists: return .orange
        case .status: return .green
        case .advanced: return .indigo
        case .about: return .teal
        }
    }
}

/// The settings window: a sidebar of pages. Every change is sent to the
/// service when it is made; there is no Apply.
struct SettingsView: View {
    @EnvironmentObject private var model: AppModel
    @AppStorage("settingsPage") private var page: Page = .general

    var body: some View {
        NavigationSplitView {
            List(Page.allCases, selection: Binding(get: { page }, set: { if let p = $0 { page = p } })) { p in
                Label { Text(p.title) } icon: { IconTile(symbol: p.symbol, color: p.color) }
                    .tag(p)
            }
            .navigationSplitViewColumnWidth(min: 170, ideal: 190, max: 260)
        } detail: {
            Group {
                switch page {
                case .general: GeneralPage()
                case .lists: ListsPage()
                case .myLists: MyListsPage()
                case .status: StatusPage()
                case .advanced: AdvancedPage()
                case .about: AboutPage()
                }
            }
            .formStyle(.grouped)
            .frame(minWidth: 480)
        }
        .frame(minWidth: 700, minHeight: 500)
    }
}

/// Shown on pages that need the service when it can't be reached.
struct ServiceUnavailable: View {
    @EnvironmentObject private var model: AppModel

    var body: some View {
        Section {
            SettingRow(model.connError?.localizedDescription ?? "Connecting to the service…",
                       description: "Splitroute runs as a system service so it can change routes. Installing it asks for your administrator password once.",
                       error: model.actionError) {
                if model.connError == .notRunning {
                    Button("Install Service") { Task { await model.installService() } }
                        .prominent()
                        .disabled(model.busy)
                }
            }
        }
    }
}

extension AppModel {
    /// The service answers and its config is loaded.
    var ready: Bool { connError == nil && config != nil }
}
