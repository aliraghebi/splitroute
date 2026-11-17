# splitroute

Keep chosen IP ranges **outside your VPN**, or drop them, on macOS and Linux.

`splitroute` adds plain system routes: ranges set to `direct` go through your normal connection (your
router), ranges set to `block` are dropped, and everything else stays in the tunnel. A route for a specific
range always wins over a VPN's catch-all route, so it works next to VPN apps such as NordVPN, OpenVPN or
WireGuard. It keeps the routes correct when you switch networks, wake from sleep or the VPN reconnects.

The lists come from [routing-rules](https://github.com/aliraghebi/routing-rules), rebuilt daily. By default,
Iranian IP ranges (`ir-cidr`) go direct.

## Test that your VPN allows it

Some VPN apps force all traffic into the tunnel (often as part of a kill switch); then no route-based tool
works. With your VPN connected:

```sh
# macOS
gw=$(ipconfig getoption en0 router)
sudo route -n add -net 2.144.0.0/14 "$gw"
route -n get 2.144.0.1                    # should show your router and en0, not utun…
sudo route -n delete -net 2.144.0.0/14

# Linux
gw=$(ip -4 route show default | awk '{print $3; exit}')
sudo ip route add 2.144.0.0/14 via "$gw"
ip route get 2.144.0.1                    # should show your router, not tun0/wg0
sudo ip route del 2.144.0.0/14
```

## Install

### macOS app

```sh
brew install --cask aliraghebi/tap/splitroute
```

This installs **Splitroute.app** (a menu bar app, macOS 13 or later) and the `splitroute` command. Without
Homebrew, download `Splitroute_<version>_macos.dmg` from the
[releases page](https://github.com/aliraghebi/splitroute/releases) and drag the app to Applications.

Open the app: it asks for your administrator password once to install and start the service (and again after
an update, to restart the service on the new version).

- The menu bar icon shows a green dot when routes are active, red when turned off or the service isn't
  running, and orange when something needs attention.
- **Settings** lists your lists and their actions, adds release lists, creates custom lists of your own
  addresses, and applies presets. Changes take effect when you click **Apply**.
- The service runs on its own, so quitting the app doesn't stop routing by itself. While routes are active,
  **Quit** asks whether to turn them off first (**Settings → When quitting while routing** changes the default).
  With **Turn on routing when the app opens**, opening the app turns routing back on, so the app's lifetime
  can match routing's.
- The app opens at login after the first install, so the menu bar icon shows the routing state after a restart.
- The app is not notarized; the cask removes the quarantine flag so macOS opens it. From the DMG, macOS
  refuses to open it the first time: run `xattr -dr com.apple.quarantine /Applications/Splitroute.app`, or
  click **Open Anyway** in System Settings → Privacy & Security.

### Command line only

Prebuilt binaries for macOS and Linux (amd64, arm64) are on the [releases page](https://github.com/aliraghebi/splitroute/releases),
with `checksums.txt` and build provenance attestations:

```sh
gh attestation verify splitroute_*_linux_amd64.tar.gz --repo aliraghebi/splitroute
go install github.com/aliraghebi/splitroute@latest    # or build it; copy the binary to /usr/local/bin
```

## Use

```sh
sudo splitroute up          # download the lists and add the routes
splitroute status           # gateway, VPN, applied routes
sudo splitroute down        # remove every route it added
sudo splitroute install     # run as a service (launchd / systemd): now and at boot
sudo splitroute uninstall   # stop the service and remove the routes
splitroute lists            # lists available in the latest routing-rules release
splitroute preset           # presets; `sudo splitroute preset <id>` applies one
```

Add `--dry-run` to any command to print the route changes instead of making them.

## Config

`/etc/splitroute.conf` (or `--config <path>`), JSON. Without it, `ir-cidr` goes direct.

```json
{
  "lists": [
    {"name": "ir-cidr", "action": "direct"},
    {"name": "malware", "action": "block"},
    {"name": "telegram", "action": "vpn"},
    {"name": "Work", "custom": true, "action": "direct", "entries": ["203.0.113.7", "198.51.100.0/24"]}
  ]
}
```

- `direct`: through your normal connection, outside the VPN. `block`: dropped. `vpn`: left alone.
- A release list is named as in `splitroute lists`. A custom list has any name and your own IPv4 addresses
  and ranges (domains are not supported yet).
- `"disabled": true` removes the routes but keeps the service running.
- `"base_url"` overrides the release URL (https only).
- The 0.1 format (`ir-cidr = direct` lines) is still read; saving from the app or `preset` rewrites it as JSON.

Only a list's **IPv4 ranges** are routed; domain-only lists (`ads`, `nsfw`, …) have no effect. The service
applies config changes within 15 seconds.

### Presets

```sh
splitroute preset            # show presets
sudo splitroute preset iran  # replace the release lists with the preset's; custom lists are kept
```

| Preset | Lists |
|--------|-------|
| `iran` | direct: `ir-cidr`; block: `malware`, `phishing` |
| `security` | block: `malware`, `phishing` |

### Control socket

The service listens on `/var/run/splitroute.sock` (macOS, group `admin`) or `/run/splitroute.sock` (Linux,
root only): one JSON request per connection, one JSON response. Commands: `status`, `get_config`,
`set_config`, `set_enabled`, `apply_preset`, `lists`, `presets` (see `api.go`).

```sh
echo '{"cmd":"status"}' | nc -U /var/run/splitroute.sock
```

## Safety

- Lists are downloaded over HTTPS and checked against the release's `SHA256SUMS`; if a download fails, the
  last verified copy is used.
- A list with a range broader than `/8` is refused, so a broken or tampered list can't send most of your
  traffic outside the VPN.
- Private and special ranges (`10.0.0.0/8`, `192.168.0.0/16`, `100.64.0.0/10`, …) are never touched.
- `down` removes exactly the routes splitroute added (Linux routes carry `proto 199`).

## Limits

- VPN kill switches that force all traffic into the tunnel override these routes.
- IPv4 only; IPv6 stays in the tunnel.
- DNS is not changed.
- Matching is by IP: sites hosted abroad (for example behind a foreign CDN) still use the VPN.

## Development

```sh
gofmt -l . && go vet ./... && go test ./...
go run . status
go run . up --dry-run
macos/build-app.sh          # macos/build/Splitroute.app (needs Xcode)
```

## License

GPL-3.0.
