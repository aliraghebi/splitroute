# CLAUDE.md

`splitroute`: a CLI for macOS and Linux that adds system routes so chosen IP lists from
[routing-rules](https://github.com/aliraghebi/routing-rules) go direct (physical gateway) or are blocked,
next to a VPN. It runs as root, so correctness and safety beat features.

## Layout

| File | Role |
|------|------|
| `main.go` | CLI, app wiring, state file and lock, gateway/VPN detection, apply, `watch` loop, service install |
| `config.go` | Config (JSON, and the 0.1 `list = action` format), validation, custom list entries, presets |
| `api.go` | Control socket for the menu bar app: one JSON request/response per connection |
| `lists.go` | Download, `SHA256SUMS` check, last-good cache, `.list` parsing and range validation |
| `macos/` | Swift menu bar app (SwiftPM, macOS 13+): AppKit status item, SwiftUI settings, socket client in `Daemon.swift`. `build-app.sh` bundles the Go binary into `Splitroute.app`; `splitroute.rb.in` is the cask |
| `routes.go` | Pure logic: plan, diff (`changes`), per-OS command builders, output parsers |

## Rules

- Stdlib only. Keep logic pure and OS-independent (`goos` is a parameter) so it's tested on any OS; only
  `app.query` / `app.change` touch the system.
- Never weaken the safety checks: ranges broader than /8 are refused, reserved ranges are skipped, the route
  count is capped, lists must match `SHA256SUMS`, and `down` removes exactly what `up` added.
- The daemon is the only writer of the config at runtime; everything from the socket goes through
  `Config.validate` (same checks as the file). Socket: root:admin 0660 on macOS, root-only elsewhere.
- Never overwrite the saved state with an empty one unless routes were really removed.
- Every bug fix gets a table test; parsers get a fixture copied from real command output.
- The app never touches routes or the config file: everything goes through the socket. Wire types in
  `Daemon.swift` mirror the Go JSON; change both together.
- English only in code, comments, commits and logs.

## Verify

```sh
gofmt -l . && go vet ./... && GOOS=linux go vet ./... && go test ./...
(cd macos && swift build)
go run . up --dry-run
goreleaser check
```

## Release

Push a `vX.Y.Z` tag. GoReleaser (`.goreleaser.yaml`) builds the CLI archives for darwin/linux amd64/arm64 and
publishes the release. The `app` job then builds `Splitroute_<version>_macos.zip`, uploads it and writes the
cask to `aliraghebi/homebrew-tap` (`scripts/update-cask.sh`, secret `HOMEBREW_TAP_TOKEN`).
The app is ad-hoc signed, not notarized (no Apple Developer account).
