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

```sh
brew install --cask aliraghebi/tap/splitroute         # macOS
go install github.com/aliraghebi/splitroute@latest    # any OS with Go; copy the binary to /usr/local/bin
```

Prebuilt binaries for macOS and Linux (amd64, arm64) are on the [releases page](https://github.com/aliraghebi/splitroute/releases),
with `checksums.txt` and build provenance attestations:

```sh
gh attestation verify splitroute_*_linux_amd64.tar.gz --repo aliraghebi/splitroute
```

## Use

```sh
sudo splitroute up          # download the lists and add the routes
splitroute status           # gateway, VPN, applied routes
sudo splitroute down        # remove every route it added
sudo splitroute install     # run as a service (launchd / systemd): now and at boot
sudo splitroute uninstall   # stop the service and remove the routes
splitroute lists            # lists available in the latest routing-rules release
```

Add `--dry-run` to any command to print the route changes instead of making them.

## Config

`/etc/splitroute.conf` (or `--config <path>`). Without it, `ir-cidr = direct`.

```ini
ir-cidr  = direct   # through your normal connection, outside the VPN
malware  = block    # dropped
telegram = vpn      # left alone: follows the VPN
# base_url = https://github.com/aliraghebi/routing-rules/releases/latest/download
```

Only a list's **IPv4 ranges** are routed; domain-only lists (`ads`, `nsfw`, …) have no effect. The service
picks up config changes with its daily refresh; run `sudo splitroute up` (or restart the service) to apply
them now.

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
```

## License

GPL-3.0.
