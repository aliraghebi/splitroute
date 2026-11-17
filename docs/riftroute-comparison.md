# riftroute vs splitroute: domain matching

Compared against [Amirhat/riftroute](https://github.com/Amirhat/riftroute) (MIT) on 2026-11-17, with
`docs/domain-routing-plan.md` as our side. Only the domain/DNS part is covered in depth.

## Scope

| | splitroute | riftroute |
|---|---|---|
| Size | ~2.1k LOC, 15 files, stdlib only | ~380 Go files + Wails/React desktop app, `x/net`, SQLite store |
| Model | one config: lists go direct or blocked, beside your VPN | profiles, rules (cidr/ip/domain/asn/country/app), its own OpenVPN/WG/IKEv2 tunnels, kill switch, PF/nftables, Linux policy routing, per-app routing |
| Domains | not implemented (plan only) | implemented, two tiers |
| License | GPL | MIT, so their code can be lifted with attribution |

## How riftroute matches domains

Split by rule shape; `domain.DomainRuleHost` (`internal/domain/profile.go`) trims `*.` so a wildcard means
its apex everywhere.

**Exact domain (`foo.com`)** — `internal/dns/resolver.go`. Resolve A+AAAA with `net.Resolver.LookupNetIP`
through the *system* resolver, route the answers as host routes, 60s TTL cache plus a background `Refresh`
re-resolver for CDNs, 8 lookups in parallel. A failed lookup keeps the last good answer and sets a 2-minute
`failTTL`, so a name that cannot resolve costs one timeout per `failTTL`, not one per reconcile. No DNS
proxy involved.

**Wildcard (`*.foo.com`)** — `internal/dnsproxy` + `internal/splitdns`. DNS cannot enumerate subdomains, so:

- a loopback UDP+TCP forwarder binds an **ephemeral** port (both protocols on the same port, 5 retries if
  the paired TCP port is taken);
- `/etc/resolver/foo.com` on macOS, `resolvectl domain` on Linux, points the apex at it; resolver files
  match the apex and all its subdomains, so the wildcard collapses to one file;
- the proxy **forwards verbatim to the system upstreams and never fabricates an answer**; learning is a
  read-only tap on the response;
- learned `rule -> fqdn -> addrs` goes into `AnswerStore`: 6h per-address TTL, 256 names per rule, 16
  addresses per name, oldest evicted, persisted so routes survive a restart;
- each new answer debounce-kicks a reconcile that installs the routes;
- `prewarm` proactively resolves the apex plus a ~50-label wordlist (`www api cdn static login ...`)
  straight against the upstreams, so common subdomains are routed before the first connection, and even
  when the browser resolves over DoH.

**No domain blocking anywhere.** Nothing in riftroute returns NXDOMAIN; block is a route-level concept only.

## What our plan already gets right

- **Install the route before replying.** riftroute replies first and routes after learning, so the first
  connection to a new subdomain goes through the VPN. Its `prewarm` wordlist exists to paper over exactly
  that race, and it is a guess list.
- **Forward direct-list queries to the physical interface's DHCP DNS**, with a /32 to that server via the
  physical gateway. riftroute forwards to `prov.DNSConfig`, i.e. the VPN's resolver while the tunnel is up,
  which returns geo-wrong CDN addresses and then routes them direct. Biggest design difference; we are on
  the right side of it.
- **Diffed resolver files.** `macResolverManager.Apply` clears the whole directory and rewrites it on every
  apply. Invisible at 10 domains; a storm at 4.6k, with a window where the files are gone. Keep the
  `changes`-style diff.
- **Marker comment, and only ever remove our own files** — same as theirs.
- **Blocking with NXDOMAIN instead of routes.** They have no answer here, and routing a block list would
  blackhole CDN co-tenants.

## Worth taking from them

1. **The `Ready()` gate, and the macOS fact behind it.** macOS does not fall back past a scoped resolver: a
   resolver file pointing at a port nobody answers makes that domain dead, not slow. riftroute installs
   resolver files only when the proxy is serving *and* has an upstream. We need the same gate, plus removal
   of every managed resolver file on daemon exit (`SIGTERM` and panic included), because the launchd restart
   gap is a blackhole window for every listed domain. "Only listed domains stop resolving" is true but
   understates it.
2. **Ephemeral port instead of a fixed 5300**, written into the resolver file. 5300 collides with
   dnsmasq / AdGuard / Pi-hole-on-localhost setups. Cost: files are rewritten when the port changes, which
   the diff handles.
3. **Self-loop guard**: drop an upstream equal to our own loopback address and port, or a
   system-DNS-is-127.0.0.1 setup spins.
4. **Bounded in-flight handlers with load shedding** (they use 128 for the scoped proxy, 1024 for the
   full-tunnel forwarder) instead of unbounded goroutines; a local process can flood a loopback port.
5. **Per-rule and per-name address caps with oldest-eviction, on top of the address TTL**, so a rotating CDN
   does not accumulate dead /32s. Dynamic routes must count against `maxRoutes`: their 256x16 = 4096
   addresses *per rule* would blow straight through our cap.
6. **Persist learned addresses**, so a restart does not re-leak the first connection to every domain.

## What does not transfer

- Their resolver-file count is a handful of user wildcards. We need thousands (`ads-lite` is ~4.6k). Nobody
  there has tested mDNSResponder at that scale, so **Step 1 of the plan is still the deciding experiment**.
  If 5k files do not fly, block lists need a different mechanism from direct lists.
- They parse answers with `golang.org/x/net/dns/dnsmessage`. Stdlib-only means we write the parser. Pure
  forwarding plus learning needs read-only parsing; NXDOMAIN blocking also needs to *build* a reply. Still
  small, but it makes us an authoritative answer source rather than a tap, so the "never fabricate" safety
  property they lean on is one we give up deliberately for block lists. Say so in the plan.
- Their proxy returns **silence** when every upstream fails. Behind a scoped resolver that is a hang until
  the stub resolver times out. Return SERVFAIL instead so macOS fails fast.
- Shared-CDN-IP exposure and the DoH blind spot are identical in both designs; neither solves them. Their
  prewarm does partly cover DoH for *direct* domains, since the routes exist regardless of who resolved the
  name. Without something like it, a Chrome with Secure DNS on bypasses our resolver files entirely and the
  direct list silently does nothing. Consider prewarming the apexes of direct domains at startup.

## One correction to the plan's framing

"Only listed domains reach the daemon" is right, but with block lists at `ads-lite` scale the listed set is
most of the ad and tracker namespace, and every one of those lookups becomes a synchronous hop through our
process. The concurrency cap and a fast NXDOMAIN path that answers from the set without forwarding matter
more than the resolver-file count.
