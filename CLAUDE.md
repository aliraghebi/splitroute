# CLAUDE.md

`splitroute`: a CLI for macOS and Linux that adds system routes so chosen IP lists from
[routing-rules](https://github.com/aliraghebi/routing-rules) go direct (physical gateway) or are blocked,
next to a VPN. It runs as root, so correctness and safety beat features.

Direction (see `docs/roadmap.md`): now: split into packages, typed rules (CIDR, domain, ASN), user lists
and imports, and a redesigned macOS settings app. Later: our own tunnels and mobile apps on the same Go core.

## Layout

| Path | Role |
|------|------|
| `cmd/splitroute/` | CLI and the `app`: wiring, `up`/`down` orchestration (`apply.go`), service loop (`watch.go`), install (`service.go`), status printing, config updates and presets (`config.go`); implements `api.Backend`. Owns `query`/`change`, the only functions that run commands |
| `internal/rules/` | Pure model: `Action`, typed entries (`entry.go`: CIDR, domain, domain suffix, ASN; `ParseEntry` syntax, floors, reserved ranges), `Route`, `Plan` and the route cap |
| `internal/config/` | Config (JSON, and the 0.1 `list = action` format), `Validate`, load/save, presets |
| `internal/lists/` | Download, `SHA256SUMS` check, last-good cache, `.list` parsing into typed entries |
| `internal/state/` | Saved state file and the lock around route changes |
| `internal/platform/` | OS-specific: command runner types, gateway/VPN detection and parsers, route socket (`netwatch_{darwin,linux}.go`), launchd/systemd files |
| `internal/engine/route/` | Route-mode engine: diff (`Changes`), per-OS command builders, apply, remove, presence check |
| `internal/api/` | Control socket for the menu bar app: one JSON request/response per connection; `Status` wire type |
| `internal/fileutil/` | Atomic file write |
| `macos/` | Swift menu bar app (SwiftPM, macOS 13+): AppKit status item, SwiftUI settings, socket client in `Daemon.swift`. `build-app.sh` bundles the Go binary into `Splitroute.app` and the DMG; `assets/` draws the app icon and DMG background (Swift scripts) and holds the `dmgbuild` layout; `splitroute.rb.in` is the cask |

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
  are skipped, domains pass `rules.DomainEntry` (they become file names), the route count is capped, lists
  must match `SHA256SUMS`, and `down` removes exactly what `up` added.
- The daemon is the only writer of the config at runtime; everything from the socket goes through
  `Config.Validate` (same checks as the file). Socket: root:admin 0660 on macOS, root-only elsewhere.
- Never overwrite the saved state with an empty one unless routes were really removed.
- Every bug fix gets a table test; parsers get a fixture copied from real command output.
- The app never touches routes or the config file: everything goes through the socket. Wire types in
  `Daemon.swift` mirror the Go JSON; change both together.
- English only in code, comments, commits and logs.

## Verify

```sh
gofmt -l . && go vet ./... && GOOS=linux go vet ./... && go test ./...
(cd macos && swift build)
go run ./cmd/splitroute up --dry-run
goreleaser check
```

## Release

Push a `vX.Y.Z` tag. GoReleaser (`.goreleaser.yaml`) builds the CLI archives for darwin/linux amd64/arm64 and
publishes the release. The `app` job then builds `Splitroute_<version>_macos.dmg`, uploads it and writes the
cask to `aliraghebi/homebrew-tap` (`scripts/update-cask.sh`, secret `HOMEBREW_TAP_TOKEN`).
The app is ad-hoc signed, not notarized (no Apple Developer account).
