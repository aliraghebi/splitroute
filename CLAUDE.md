# CLAUDE.md

`splitroute`: a CLI for macOS and Linux that adds system routes so chosen IP lists from
[routing-rules](https://github.com/aliraghebi/routing-rules) go direct (physical gateway) or are blocked,
next to a VPN. It runs as root, so correctness and safety beat features.

Direction (see `docs/roadmap.md`): now: split into packages, typed rules (CIDR, domain, ASN), user lists
and imports, and a redesigned macOS settings app. Later: our own tunnels and mobile apps on the same Go core.

## Layout

| Path | Role |
|------|------|
| `cmd/splitroute/` | CLI and the `app`: wiring, `up`/`down` orchestration (`apply.go`), service loop (`watch.go`), install (`service.go`), status printing, settings access, presets and `config export|import` (`config.go`), the `list` command (`listcmd.go`), import downloads (`imports.go`); implements `api.Backend`. Owns `query`/`change`, the only functions that run commands |
| `internal/rules/` | Pure model: `Action`, typed entries (`entry.go`: CIDR, domain, domain suffix, ASN; `ParseEntry` syntax, floors, reserved ranges), `Route`, `Plan` and the route cap |
| `internal/config/` | `Config` type, `Validate`, JSON and 0.1 `list = action` parsing (import and first-run migration), presets, pure list edits (`edit.go`: create, enable, remove, rename, action, move, add/remove entries) shared by the CLI and the socket |
| `internal/store/` | Settings database (SQLite, `modernc.org/sqlite`): schema in `migrations/*.sql` (applied by `PRAGMA user_version`), `Init`/`Config`/`Update` (one transaction), `Changed` (`PRAGMA data_version`); imports (`imports.go`): state, entries, `SaveDownload`, `MarkChecked`, `Due` |
| `internal/lists/` | Release lists: download, `SHA256SUMS` check, last-good cache, `.list` parsing, `Catalog` (titles, descriptions, suggested actions; shown by the app and `splitroute lists`). Imports: `ParseText` (entries, hosts files, domain sets; fixtures in `testdata/`), `FetchImport` (conditional GET, limits), `CheckImport` |
| `internal/state/` | Saved state file and the lock around route changes |
| `internal/platform/` | OS-specific: command runner types, gateway/VPN detection and parsers, route socket (`netwatch_{darwin,linux}.go`), launchd/systemd files |
| `internal/engine/route/` | Route-mode engine: diff (`Changes`), per-OS command builders, apply, remove, presence check |
| `internal/api/` | Control socket for the menu bar app: one JSON request/response per connection, whole-config and per-list edit commands (`list_create` with `text` parses a file's contents like an import), `lists` with the catalog; `Status` wire type |
| `internal/fileutil/` | Atomic file write |
| `macos/` | Swift menu bar app (SwiftPM, macOS 13+): AppKit status item, SwiftUI settings (`SettingsView.swift` sidebar, one `*Page.swift` per page, `AddListSheet.swift` for adding lists, shared rows in `Components.swift`), socket client in `Daemon.swift`. `build-app.sh` bundles the Go binary into `Splitroute.app` and the DMG; `assets/` draws the app icon and DMG background (Swift scripts) and holds the `dmgbuild` layout; `splitroute.rb.in` is the cask |

## Rules

- Dependencies only when needed: prefer stdlib, add a package when writing it ourselves would be bigger or
  riskier (DNS messages, tunnel protocols, TUN stacks). Each new dependency: maintained, license compatible
  with GPL-3.0, pinned in `go.mod`, and the reason in the commit message. No dependency for a few lines of code.
- Keep logic pure and OS-independent (`goos` is a parameter) so it's tested on any OS. Only `app.query` /
  `app.change` (in `cmd/splitroute`) touch the system; packages get them passed in (`platform.QueryFunc`,
  `platform.ChangeFunc`), never call `exec` themselves.
- Dependencies point inward: `rules` imports nothing of ours; `cmd/splitroute` is the only place that wires
  packages together. `internal/` packages never import `cmd/`.
- Never weaken the safety checks: ranges broader than /8 (IPv4) or /16 (IPv6) are refused, reserved ranges
  are skipped, domains pass `rules.DomainEntry` (they become file names), the route count is capped, release
  lists must match `SHA256SUMS`, and `down` removes exactly what `up` added.
- Imports have no checksum, so: https only (redirects too), no credentials in URLs (the database is
  world-readable), 64 MB / 500k entries, a too-broad range refuses the whole download, `lists.CheckImport`
  refuses empty or mostly-invalid downloads, and entries are replaced only by a complete, parsed download in
  one transaction (`store.SaveDownload`, guarded by the URL it came from). A failure keeps the last good copy.
  Only `canWrite` processes download; dry-run never does.
- Settings live only in the database (`<state dir>/splitroute.db`, 0644, rollback journal: non-root readers
  must keep working, so no WAL). Every write goes through `store.Update`/`Init`, which run `Config.Validate`
  in the same transaction; reads validate too. Writers: the daemon (socket requests) and root CLI commands;
  the daemon notices other processes' writes via `store.Changed`. Socket: root:admin 0660 on macOS,
  root-only elsewhere.
- Schema changes: a new numbered file in `internal/store/migrations/`, never edit an applied one. A newer
  schema or a failed `quick_check` is refused, never reset or recreated. `/etc/splitroute.conf` is read only
  to seed a new database (and by `config import`).
- Never overwrite the saved state with an empty one unless routes were really removed.
- Every bug fix gets a table test; parsers get a fixture copied from real command output.
- The app never touches routes or the settings database: everything goes through the socket. Wire types in
  `Daemon.swift` mirror the Go JSON; change both together. The app edits with the per-list commands, never
  `set_config` from a stale copy (that would overwrite CLI edits made meanwhile).
- English only in code, comments, commits and logs.

## Verify

```sh
gofmt -l . && go vet ./... && GOOS=linux go vet ./... && go test ./...
(cd macos && swift build)   # UI review: debug build with SPLITROUTE_SOCKET=<fake> --snapshot <dir>
go run ./cmd/splitroute up --dry-run
goreleaser check
```

## Release

Push a `vX.Y.Z` tag. GoReleaser (`.goreleaser.yaml`) builds the CLI archives for darwin/linux amd64/arm64 and
publishes the release. The `app` job then builds `Splitroute_<version>_macos.dmg`, uploads it and writes the
cask to `aliraghebi/homebrew-tap` (`scripts/update-cask.sh`, secret `HOMEBREW_TAP_TOKEN`).
The app is ad-hoc signed, not notarized (no Apple Developer account).
