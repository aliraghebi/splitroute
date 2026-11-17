# CLAUDE.md

`splitroute`: a CLI for macOS and Linux that adds system routes so chosen IP lists from
[routing-rules](https://github.com/aliraghebi/routing-rules) go direct (physical gateway) or are blocked,
next to a VPN. It runs as root, so correctness and safety beat features.

## Layout

| File | Role |
|------|------|
| `main.go` | CLI, app wiring, state file and lock, gateway/VPN detection, apply, `watch` loop, service install |
| `config.go` | `/etc/splitroute.conf` parsing (`list = direct\|block\|vpn`, `base_url`) |
| `lists.go` | Download, `SHA256SUMS` check, last-good cache, `.list` parsing and range validation |
| `routes.go` | Pure logic: plan, diff (`changes`), per-OS command builders, output parsers |

## Rules

- Stdlib only. Keep logic pure and OS-independent (`goos` is a parameter) so it's tested on any OS; only
  `app.query` / `app.change` touch the system.
- Never weaken the safety checks: ranges broader than /8 are refused, reserved ranges are skipped, the route
  count is capped, lists must match `SHA256SUMS`, and `down` removes exactly what `up` added.
- Never overwrite the saved state with an empty one unless routes were really removed.
- Every bug fix gets a table test; parsers get a fixture copied from real command output.
- English only in code, comments, commits and logs.

## Verify

```sh
gofmt -l . && go vet ./... && GOOS=linux go vet ./... && go test ./...
go run . up --dry-run
goreleaser check
```

## Release

Push a `vX.Y.Z` tag. GoReleaser (`.goreleaser.yaml`) builds darwin/linux amd64/arm64, publishes the release
and updates the cask in `aliraghebi/homebrew-tap` (secret `HOMEBREW_TAP_TOKEN`).
