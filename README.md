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

Open the app: it registers its routing service and macOS asks you to allow it once, under **Allow in the
Background** in System Settings → General → Login Items (the app opens that page). After an update the app
restarts the service on the new version by itself, without a password. If you installed the service with an
older version of the app or with `sudo splitroute install`, the app asks for your password once to move it to
Login Items. Turn the service off there, or with **Advanced → Uninstall service**.

- The menu bar icon shows a green dot when routes are active, red when turned off or the service isn't
  running, and orange when something needs attention.
- **Open App** (menu bar) opens the window on Status; it also has General (on/off, preset, startup), Lists,
  Advanced (list source, log file, the service), and About under the app name at the bottom, with a search
  field at the top. Changes are sent to the service as you make them; if it refuses one, the reason shows
  under that row.
- **Lists** shows every list you route, with its action. The **+** button adds one: a ready-made list from
  routing-rules (by name, such as "Iran IP ranges"), an import from an https URL (downloaded daily), a file
  from this Mac (plain lists, hosts files and domain sets up to 6 MB, copied into a custom list), or a
  custom list you type.
- The service runs on its own, so quitting the app doesn't stop routing by itself. While routes are active,
  **Quit** asks whether to turn them off first (**General → When quitting while routing** changes the default).
  With **Turn on routing when the app opens**, opening the app turns routing back on, so the app's lifetime
  can match routing's.
- The app opens at login after the first install, so the menu bar icon shows the routing state after a restart.
  At login only the icon appears; opening the app yourself (Spotlight, Finder, Launchpad) opens the window.
- The app is not notarized; the cask removes the quarantine flag so macOS opens it. From the DMG, macOS
  refuses to open it the first time: run `xattr -dr com.apple.quarantine /Applications/Splitroute.app`, or
  click **Open Anyway** in System Settings → Privacy & Security.

### Command line only

Prebuilt binaries for macOS and Linux (amd64, arm64) are on the [releases page](https://github.com/aliraghebi/splitroute/releases),
with `checksums.txt` and build provenance attestations:

```sh
gh attestation verify splitroute_*_linux_amd64.tar.gz --repo aliraghebi/splitroute
go install github.com/aliraghebi/splitroute/cmd/splitroute@latest   # or build it; copy the binary to /usr/local/bin
```

## Use

```sh
sudo splitroute up          # download the lists and add the routes
splitroute status           # gateway, VPN, applied routes
sudo splitroute down        # remove every route it added
sudo splitroute install     # run as a service (launchd / systemd): now and at boot; not while the app manages it
sudo splitroute uninstall   # stop the service and remove the routes
splitroute lists            # lists in the latest routing-rules release, with their titles
splitroute preset           # presets; `sudo splitroute preset <id>` applies one
```

Add `--dry-run` to any command to print the route changes instead of making them.

## Config

Settings live in a SQLite database: `/var/db/splitroute/splitroute.db` on macOS,
`/var/lib/splitroute/splitroute.db` on Linux. Change them with the menu bar app, `splitroute preset`, or as JSON:

```sh
splitroute config export > settings.json        # anyone can read
sudo splitroute config import settings.json     # validated, then applied within 15 seconds
```

The first run as root imports `/etc/splitroute.conf` (or `--config <path>`) if it exists; without it, `ir-cidr`
goes direct. After that the file is **no longer read** (the service logs a warning if it changes); it is left
in place so an older splitroute still finds it. The JSON format:

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
- A release list is named as in `splitroute lists`. A custom list has any name and your own entries:
  addresses or ranges (`203.0.113.7`, `198.51.100.0/24`, `2400:cb00::/32`), domains with their subdomains
  (`example.com`), exact names (`full:www.example.com`) or AS numbers (`AS13335`). Only IPv4 is routed for
  now; the rest is validated and kept for domain and ASN routing.
- `"disabled": true` removes the routes but keeps the service running.
- `"base_url"` overrides the release URL (https only).
- `config import` also reads the 0.1 format (`ir-cidr = direct` lines).
- In custom lists, `#` lines are notes: kept with the entry that follows them. Entries are stored in a
  canonical form (`Example.COM` → `example.com`, `198.51.100.9/24` → `198.51.100.0/24`), and duplicates and
  blank lines are dropped.

Only a list's **IPv4 ranges** are routed; domain-only lists (`ads`, `nsfw`, …) have no effect. The service
applies settings changes within 15 seconds.

### Lists from the command line

```sh
splitroute list                                  # configured lists, in order, with entry counts
splitroute list show Work                        # a custom list's entries
sudo splitroute list create Work block           # new custom list (direct, block or vpn; default direct)
sudo splitroute list enable telegram vpn         # add a release list (names: splitroute lists)
sudo splitroute list add Work 203.0.113.7 example.com AS13335
sudo splitroute list add Work - < hosts.txt      # one entry per line from stdin; # lines are notes
sudo splitroute list delete Work example.com
sudo splitroute list action Work direct
sudo splitroute list rename Work Office          # custom lists only
sudo splitroute list move Office 1               # 1 is first
sudo splitroute list remove Office
```

Entries already in a list are skipped; one invalid entry rejects the whole command. A custom list holds at
most 100,000 entries.

### Imports

An import is a list downloaded from an https URL and refreshed by the service (daily by default):

```sh
sudo splitroute list import Ads https://example.com/hosts.txt block
sudo splitroute list refresh Ads     # download again now
splitroute list show Ads             # what the last good download contained
splitroute status                    # last download time and error per import
```

- Formats: one entry per line (same syntax as custom lists), hosts files (`0.0.0.0 ads.example.com`: each
  name is that exact domain), and Surge/Clash domain sets (`.example.com` is the domain and its
  subdomains, a plain name is exact). `#` and `!` lines are comments. AdGuard/uBlock rules are skipped for
  now.
- Invalid lines are skipped and counted. A range broader than /8 (IPv4) or /16 (IPv6) refuses the whole
  download, and so does a download with no entries or more invalid lines than entries (a login or error
  page). Limits: 64 MB and 500,000 entries per import.
- A failed download keeps the last good copy and is retried after 15 minutes.
- https only, also for redirects. URLs with a user or password are refused: **every local user can read the
  settings database**, including the URL, so don't put secrets (tokens in the query string either) in it.
- An import's entries come from its URL: `list add`/`delete` refuse imports. Changing the URL drops the old
  entries until the new URL is downloaded.

### Presets

```sh
splitroute preset            # show presets
sudo splitroute preset iran  # replace the release lists with the preset's; custom lists and imports are kept
```

| Preset | Lists |
|--------|-------|
| `iran` | direct: `ir-cidr`; block: `malware`, `phishing` |
| `security` | block: `malware`, `phishing` |

### Control socket

The service listens on `/var/run/splitroute.sock` (macOS, group `admin`) or `/run/splitroute.sock` (Linux,
root only): one JSON request per connection, one JSON response. Commands: `status`, `get_config`,
`set_config`, `set_enabled`, `apply_preset`, `lists`, `presets`, and list edits `list_create`,
`list_enable`, `list_import`, `list_refresh`, `list_remove`, `list_rename`, `list_set_action`, `list_move`,
`entries_add`, `entries_remove`, `entries_set`
(see `internal/api/api.go`).

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
go run ./cmd/splitroute status
go run ./cmd/splitroute up --dry-run
macos/build-app.sh          # macos/build/Splitroute.app (needs Xcode)
# Debug builds: SPLITROUTE_SOCKET=<path> talks to a fake service; --snapshot <dir> renders every
# settings page (light and dark) to PNGs and quits; --settings opens the settings window.
```

## License

GPL-3.0.
