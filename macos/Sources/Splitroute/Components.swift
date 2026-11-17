import SwiftUI

/// The row on every settings page: a title with a gray description on the left,
/// the control on the right, the daemon's error under both. The settings look
/// is set here, so pages don't style rows themselves.
struct SettingRow<Control: View>: View {
    let title: String
    var description: String?
    var error: String?
    let control: Control

    init(_ title: String, description: String? = nil, error: String? = nil, @ViewBuilder control: () -> Control) {
        self.title = title
        self.description = description
        self.error = error
        self.control = control()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .center, spacing: 16) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                    if let description, !description.isEmpty {
                        Text(description)
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                Spacer(minLength: 12)
                control
            }
            if let error { ErrorText(error) }
        }
        .padding(.vertical, 2)
    }
}

extension SettingRow where Control == EmptyView {
    init(_ title: String, description: String? = nil, error: String? = nil) {
        self.init(title, description: description, error: error) { EmptyView() }
    }
}

/// A row that shows a value instead of a control (status pages).
struct ValueRow: View {
    let title: String
    let value: String
    var description: String?

    init(_ title: String, _ value: String, description: String? = nil) {
        self.title = title
        self.value = value
        self.description = description
    }

    var body: some View {
        SettingRow(title, description: description) {
            Text(value).foregroundStyle(.secondary).textSelection(.enabled).multilineTextAlignment(.trailing)
        }
    }
}

struct ErrorText: View {
    let message: String
    init(_ message: String) { self.message = message }

    var body: some View {
        Label(message, systemImage: "exclamationmark.circle.fill")
            .font(.subheadline)
            .foregroundStyle(.red)
            .textSelection(.enabled)
            .fixedSize(horizontal: false, vertical: true)
    }
}

struct WarningText: View {
    let message: String
    init(_ message: String) { self.message = message }

    var body: some View {
        Label(message, systemImage: "exclamationmark.triangle.fill")
            .font(.subheadline)
            .foregroundStyle(.orange)
            .textSelection(.enabled)
            .fixedSize(horizontal: false, vertical: true)
    }
}

/// An SF Symbol on a colored rounded square, like System Settings' sidebar.
/// The only place with hard-coded colors.
struct IconTile: View {
    let symbol: String
    let color: Color
    var size: CGFloat = 20

    var body: some View {
        Image(systemName: symbol)
            .font(.system(size: size * 0.55, weight: .semibold))
            .foregroundStyle(.white)
            .frame(width: size, height: size)
            .background(RoundedRectangle(cornerRadius: size * 0.26, style: .continuous).fill(color.gradient))
    }
}

/// The first card of a page: its icon, name and what it is for.
struct PageHeader: View {
    let page: Page

    var body: some View {
        Section {
            HStack(spacing: 14) {
                IconTile(symbol: page.symbol, color: page.color, size: 40)
                VStack(alignment: .leading, spacing: 3) {
                    Text(page.title).font(.title3.weight(.semibold))
                    Text(page.subtitle).font(.subheadline).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(.vertical, 4)
        }
    }
}

/// Direct / Block / VPN, applied as soon as it changes.
struct ActionPicker: View {
    let action: Action
    let set: (Action) -> Void

    var body: some View {
        Picker("Action", selection: Binding(get: { action }, set: { if $0 != action { set($0) } })) {
            ForEach(Action.allCases) { Text($0.label).tag($0) }
        }
        .labelsHidden()
        .fixedSize()
    }
}

/// A small icon button for row actions.
struct RowButton: View {
    let symbol: String
    let help: String
    let action: () -> Void

    var body: some View {
        Button(action: action) { Image(systemName: symbol) }
            .buttonStyle(.borderless)
            .help(help)
            .accessibilityLabel(help)
    }
}

extension View {
    /// Liquid Glass on macOS 26+, the classic prominent style before.
    @ViewBuilder func prominent() -> some View {
        if #available(macOS 26, *) { buttonStyle(.glassProminent) } else { buttonStyle(.borderedProminent) }
    }
}

/// Go's time.Time in JSON (RFC 3339, with or without fractional seconds).
/// The zero time ("0001-01-01T00:00:00Z") is nil.
func parseGoTime(_ s: String?) -> Date? {
    guard let s, !s.isEmpty else { return nil }
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    var d = f.date(from: s)
    if d == nil {
        f.formatOptions = [.withInternetDateTime]
        d = f.date(from: s)
    }
    guard let d, d > Date(timeIntervalSince1970: 0) else { return nil }
    return d
}

func relative(_ d: Date) -> String {
    RelativeDateTimeFormatter().localizedString(for: d, relativeTo: Date())
}

func plural(_ n: Int, _ one: String, _ many: String? = nil) -> String {
    "\(n.formatted()) \(n == 1 ? one : (many ?? one + "s"))"
}
