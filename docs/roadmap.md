# Roadmap

Status: direction agreed 2026-11-17, nothing started.

## Now

Three tracks, in this order. Each ends with something that ships.

1. **Restructure** the Go code into packages, no behavior change.
2. **Routing features** in route mode (kernel routes next to the user's VPN): typed rules, user lists,
   imports, ASN, domains.
3. **macOS app redesign**: a settings window in the style of Raycast and macOS System Settings.

Tracks 2 and 3 can overlap once the new config and socket types from track 2 are fixed, since the app
only talks to the daemon through them.

## Later (not planned yet)

Our own tunnels (OpenVPN, sing-box protocols, HTTP/SOCKS proxy) and mobile apps. Notes are kept at the end
so near-term choices don't block them.

---

## 1. Restructure

Split the flat `package main` so the core can be imported later (TUN backend, `gomobile bind`):

```
cmd/splitroute/          CLI, wiring, up/down orchestration, watch loop
internal/config/         Config, validation, presets, migration from older formats
internal/rules/          entry types, validation, matching, planning (pure)
internal/lists/          download, SHA256SUMS, cache, parsers per format
internal/state/          state file and lock
internal/api/            socket server and wire types
internal/platform/       command runners, gateway/VPN detection, netwatch, service files (per OS)
internal/engine/route/   route diff, command builders, apply, presence check
```

Status: done (branch `refactor/packages`). The engine sits under `internal/` too: no public Go API until
something outside this module needs one.

- Same tests pass; `up --dry-run` output byte-identical before and after (diff it).
- `app.query` / `app.change` stay the only place commands run; passed as `platform.QueryFunc` /
  `platform.ChangeFunc` into `internal/engine/route` and `internal/platform`.
- One PR, moves only. No renames of JSON fields, files on disk or socket commands.

## 2. Routing features

### Rule model

Today: `List{name, action, entries []IPv4}`. New:

```
Entry  = {type, value}   type: cidr | domain | domain_suffix | asn
List   = {id, name, source, entries}   source: release | custom | import{url, format, refresh}
Policy = ordered [{list, action}]      action: direct | block | vpn
```

- First match in policy order; block beats direct on overlap (as today).
- IPv6 in the model from the start. Route mode can stay IPv4-only at first, but the parser and validator
  must not drop v6 silently: warn, as with domain-only lists today.
- New config version; keep reading the current JSON and the 0.1 `list = action` format.
- One validator per entry type, used by the file, the socket and imports alike.
- `Daemon.swift` wire types change in the same PR as the Go JSON.

### Steps

1. **Typed entries**: done. `rules.Entry` (cidr, domain, domain_suffix, asn); custom entries stay strings
   in the config (`example.com`, `full:www.example.com`, `AS13335`, v4/v6 ranges), so no config v2 was
   needed. Release lists parse all four `.list` line types. Route mode still routes IPv4 only.
   **Policy order** is split out: a VPN list overriding a direct one means a route via the VPN interface
   (which changes with the VPN), not just a planning change. Do it with its own design.
2. **Settings database**: done. SQLite (`modernc.org/sqlite`, +3.9 MB, pure Go) chosen over bbolt
   (+0.26 MB) for integrity checks, constraints and tooling. `internal/store`; the old config file seeds a
   new database once; `config export`/`import`. Release list cache and `state.json` stay files for now;
   state moves in with learned DNS addresses (domain step).
3. **User lists**: done (CLI and socket; the app adopts the socket commands in the redesign). Pure edits in
   `config/edit.go`, each run through `store.Update`, so one edit rewrites the config in one transaction:
   fine for hand-made lists (cap 100k entries). Imports get a direct path to `entries` instead.
4. **ASN**: routing-rules publishes per-ASN CIDR lists at build time from public BGP data (separate repo
   change). The client treats them as CIDR lists: offline, `SHA256SUMS`-checked. `AS12345` typed in a user
   list resolves via the routing-rules index, never a live third-party API from a root daemon.
5. **Imports**: done for plain text (entries, hosts files, Surge/Clash domain sets). Next formats, one
   parser each with real fixtures: AdGuard/uBlock (domain rules only), Clash rule-provider YAML, sing-box
   `.srs`, v2ray `geoip.dat`/`geosite.dat`. Safety rules are in CLAUDE.md.
6. **Domains**: `docs/domain-routing-plan.md`. Run its step 1 prototype first (resolver files at 5k scale);
   it decides whether block lists use the same mechanism. Keep the domain matcher in `internal/rules` and the
   `/etc/resolver` code in `internal/engine/route`, so a later TUN engine can reuse the matcher.

Country lists are already CIDR lists in routing-rules (`ir-cidr`); nothing to add for them.

## 3. macOS app redesign

Target: the reference screenshot (Raycast settings): sidebar with colored icon tiles, grouped rounded cards,
each row a title + gray description on the left and the control on the right, section headers between cards.

### Structure

- Replace the single `Form` window (`SettingsView.swift`, opened from `main.swift`) with a
  `NavigationSplitView` with a sidebar. Full-size content view, transparent title bar, back/forward in the
  toolbar.
- Sidebar pages:
  - **General**: on/off, start at login, service status, preset.
  - **Lists**: ready lists from routing-rules, with action picker and entry counts per type.
  - **My Lists**: user lists, editor, import (URL or file).
  - **Status**: gateway, VPN interface, active routes per list, last refresh, errors.
  - **Advanced**: list source URL, refresh interval, logs location, reinstall/uninstall the service.
  - **About**: version, links, update check.
- The menu bar menu stays and keeps quick toggles; the window is for settings.

### Look

- Use stock SwiftUI where it gets us there: `Form` with `.formStyle(.grouped)` already draws the System
  Settings cards. Build one `SettingRow(title:, description:, control:)` view for the Raycast row layout and
  use it everywhere, so the look is set in one place.
- Sidebar icons: SF Symbols on colored rounded squares (like System Settings), one color per page.
- macOS 26 draws the floating inset sidebar and the rounded window in the screenshot by itself for a
  `NavigationSplitView`. Keep the deployment target at macOS 13; on 13-15 it degrades to the classic
  sidebar. Liquid Glass APIs only behind `if #available(macOS 26, *)`. Raise the target only if a needed API
  forces it, and say why.
- Light and dark mode both, system accent color, no hard-coded colors outside the icon tiles.
- Take the layout pattern only, not Raycast's assets: our own icons and names.

### Rules that stay

- The app never touches routes or the config file; everything goes through the socket.
- A setting changed in the UI is saved by the daemon only after `Config.validate`; show the daemon's error
  inline under the row, never revert silently.

---

## Later: tunnels and mobile (notes only)

- **Kernel routes cannot carry them.** A proxy works per connection, routes only pick a gateway; iOS and
  Android expose no route table. Both need a TUN engine: we own a TUN device and the default route, and a
  userspace stack picks the outbound. Route mode stays the "next to your VPN" product; the two never run
  at once.
- **Don't write a userspace network stack.** Candidate: embed sing-box as a library behind one package, or
  run it as a subprocess. To verify in a spike: license (expected GPL-3.0-or-later, compatible with ours),
  library API stability, memory use. OpenVPN is not a sing-box protocol: subprocess or skip.
- **Mobile**: `gomobile bind` on the core running TUN mode in-process (no daemon, no socket). iOS needs
  `NEPacketTunnelProvider`, a paid Apple Developer account, and per App Store guideline 5.4 an organization
  account for VPN apps. Android: `VpnService`. Flutter vs native decided then.
- **What the near-term work must not break for this**: the core stays in importable packages (track 1);
  rule matching stays separate from the route backend (track 2).
