# Plan: domain lists (direct and block by domain name)

Status: design agreed, not started. First step is the prototype below.

## Problem

splitroute only routes IPv4 ranges. Domain-only lists in routing-rules (`ads-lite`, `nsfw`, ...) have no
effect today (the daemon warns "no routable IPv4 ranges"). Sites on CDNs can't be routed by static IP lists
because their IPs rotate.

## Design: per-domain resolvers pointing at the daemon

macOS reads `/etc/resolver/<domain>` files. Each one says "for this domain and all its subdomains, ask this
DNS server". We write one per listed domain, pointing at the daemon:

```
# /etc/resolver/digikala.com
nameserver 127.0.0.1
port 5300
```

Only listed domains reach the daemon. Every other lookup uses the normal DNS (VPN's or ISP's) and never
touches splitroute. Check with `scutil --dns`.

### Direct list (example: digikala.com, VPN on)

1. Browser asks for `www.digikala.com`; macOS sends it to `127.0.0.1:5300` because of the resolver file.
2. Daemon forwards the query to the **physical network's DNS** (DHCP DNS of the gateway interface), with a
   host route to that server via the physical gateway, so the lookup itself skips the VPN and gets local
   (geo-correct) answers.
3. For each A record: `route add <ip>/32 <physical gateway>`.
4. Only after the routes exist, reply to macOS. (Reply first and the first connection goes through the VPN.)
5. The /32 is more specific than the VPN's default route, so traffic goes direct.
6. Each route expires after `max(TTL, ~1h)` (apps cache longer than TTL). Refresh the expiry on re-lookup.

### Block list (example: doubleclick.net)

Reply `NXDOMAIN`. No route. Blocking by route would also block unrelated sites sharing the CDN IP.

### VPN-action lists

No resolver file; nothing to do.

## Why not a system-wide DNS proxy

Considered: set the system DNS to the daemon, forward everything to the previous servers.

- Single point of failure: daemon crash, restart during upgrade, or app deleted without uninstall leaves DNS
  pointing at a dead 127.0.0.1, which looks like the whole internet is down. Scoped resolvers fail safe:
  only listed domains stop resolving.
- Fights the VPN: VPN clients set their own DNS on connect, and macOS prefers it while the tunnel is up, so
  the proxy is bypassed exactly when it matters. Re-applying means polling `scutil --dns` (no stdlib API for
  change notifications) and two programs overwriting each other.
- "Previous DNS servers" are a moving target: once overridden, the system shows ours; originals change on
  every network switch; direct domains need physical DNS and VPN domains need VPN DNS (two upstreams).
  Restoring wrong leaves DNS broken after uninstall.
- Doesn't fix the DoH blind spot either.

Keep it only as an opt-in fallback for block lists if the prototype shows resolver files don't scale. If
built: fail open (restore DNS on exit, `down`, `uninstall`), health check that restores the saved servers if
the proxy stops answering, re-apply on network events, keep `/etc/resolver` for direct lists.

Also rejected: Network Extension DNS/app proxy (needs Apple Developer account and entitlements; the app is
ad-hoc signed). Checking DNS answers against IP lists adds nothing: IP lists already route by IP.

## Step 1: prototype (decides whether block lists fit this design)

Measure on macOS 13 and 26+:

- Write N resolver files (N = 100, 1k, 5k, 20k) pointing at a tiny UDP responder on 127.0.0.1:5300.
- Time for mDNSResponder to pick them up (`scutil --dns` shows them; first query reaches the responder).
- Lookup latency for listed and unlisted names; mDNSResponder CPU and memory.
- Does a connected VPN (WireGuard, OpenVPN, Tunnelblick, an IKEv2 app) still let the resolver files win for
  their domains?
- Does `port` work for all clients, or should we bind 127.0.0.1:53 on a loopback alias (e.g. 127.0.0.53)?

`ads-lite` is ~4.6k domains; if 5k files is fine, block lists go through resolver files too.

## Step 2: implementation

Keep the CLAUDE.md rules: pure logic tested on any OS, only `app.query`/`app.change` (passed into packages) change the
system, `down` removes exactly what `up` added, route cap, never write an empty state over a real one.

- **DNS parsing (`internal/dns`, pure):** parse and build with `golang.org/x/net/dns/dnsmessage` (header,
  question, A/AAAA/CNAME answers, NXDOMAIN and SERVFAIL replies, TTLs). Tests use fixtures captured from
  real `dig` traffic.
- **Lists (`internal/lists`, `internal/rules`):** keep domain entries instead of dropping them; validate (no wildcards beyond the
  domain-suffix meaning, length limits, lowercase, punycode only). Cap domains per list like routes.
- **Plan (`internal/rules`, pure):** `planDomains(cfg, lists) -> map[domain]Action`, block beats direct like IPs.
  Diff of resolver files to write/remove, like `changes`.
- **Resolver files (`internal/engine/route/resolver.go`):** write `/etc/resolver/<domain>` with a marker comment
  (`# managed by splitroute`); only ever remove files carrying the marker. Record written files in the state.
- **DNS server (`internal/dns/server.go`):** UDP (and TCP for truncated answers) on 127.0.0.1:5300 in the `watch`
  loop. Upstream = physical interface DNS (from `scutil --dns` / `ipconfig getpacket`), with a /32 route
  to it via the gateway. Timeouts; never block the answer longer than ~2s.
- **Dynamic routes:** add before replying; track `ip -> {list, expiry}` in memory and in the state file so
  `down` and a restart clean them. Count them against `maxRoutes`. Same reserved-range and /8 checks.
  Expiry sweep on the existing ticker. On gateway change, move them like static direct routes.
- **Status and socket:** per-list domain counts, active dynamic routes; `Daemon.swift` wire types change
  together with the Go JSON.
- **App:** drop the "domain-only lists have no effect" text; show domain counts.
- **Linux (later, separate):** no `/etc/resolver`. Use systemd-resolved routing domains
  (`resolvectl domain <dummy link> ~example.com`) on a dummy interface pointing at the daemon.

## Tests

- Table tests: DNS parse/build from fixtures, domain validation, `planDomains` (block beats direct),
  resolver-file diff, dynamic route expiry and cap.
- `up --dry-run` prints resolver files it would write.
- Manual on macOS: VPN on, `dig www.digikala.com` (via system resolver: `dscacheutil -q host -a name ...`),
  `route -n get <ip>` shows the physical gateway; blocked domain returns NXDOMAIN; kill the daemon, unlisted
  names still resolve; `down` leaves no managed files in `/etc/resolver` and no dynamic routes.

## Limits to document for users

- Apps that bypass the system resolver ignore domain lists: browser Secure DNS (DoH) in Chrome/Firefox,
  apps with built-in DoH, VPNs that capture all port-53 traffic. Users must turn DoH off in browsers.
- Shared CDN IPs: a direct domain's IP also sends other sites on that IP direct.
- First lookup after a route expires pays one extra `route add` (milliseconds).
