# DevMesh DNS product and implementation plan

## 1. Product promise

DevMesh DNS turns changing infrastructure into stable names. A user should be able to install one binary, declare names beside the services they describe, and resolve those names from the appropriate network within seconds.

The product must remain useful when the internet, DevMesh Cloud, Kubernetes, or Docker is unavailable. DNS is foundational infrastructure, so the local data path may never require a SaaS round trip.

### Primary users

1. A developer running several Docker Compose projects on one workstation.
2. A homelab operator with services on a LAN and a tailnet.
3. A small team sharing development and staging environments.
4. A platform engineer exposing selected Kubernetes services outside a cluster.

### Jobs to be done

- Replace fragile `/etc/hosts` entries with names that follow service lifecycles.
- Resolve the same name differently on a laptop, LAN, tailnet, or cluster network.
- See where a record came from, why it resolved, and whether its target is healthy.
- Add DNS to an existing Compose or Kubernetes definition without adopting a new orchestrator.
- Uninstall cleanly and restore the machine's previous DNS configuration.

## 2. Product boundaries

### First public release (`v0.1`)

- Static records loaded from a human-readable YAML file.
- Authoritative local zones with exact and wildcard records.
- `A`, `AAAA`, `CNAME`, `TXT`, and `SRV` records.
- UDP and TCP DNS listeners.
- Forwarding with cache, timeout, retry, and loop detection.
- Views selected by source CIDR, listener, and explicitly configured priority.
- Docker discovery from events, container inspection, Compose metadata, and DevMesh labels.
- A reconciliation loop that removes stale discovered records.
- Local HTTP API, CLI, embedded read-mostly dashboard, metrics, and structured logs.
- A `doctor` command and reversible resolver installation.
- Linux, macOS, and Windows release artifacts.

### Explicitly not in `v0.1`

- A full recursive resolver. DevMesh forwards non-local questions to configured or system upstreams.
- A general reverse proxy or service mesh.
- Automatic public certificates.
- Automatic discovery of every process or VM. Processes register through the CLI/API; VM providers require adapters.
- Transparent port translation. DNS returns names and addresses, not ports. `api.internal:8080` still needs a port unless an HTTP/TLS gateway owns 80/443.
- Multi-machine consensus or a cloud control plane.
- DNSSEC signing.
- mDNS capture of `.local`.

### Later releases

- `v0.2`: local HTTP/TLS gateway, private CA workflow, health-aware answers, richer dashboard.
- `v0.3`: Kubernetes Services and EndpointSlices, Tailscale device import and tailnet views.
- `v0.4`: mDNS bridge, VM adapters, Git-managed records, documented extension SDK.
- `v1.0`: signed releases, migration guarantees, backup/restore, stable API, hardened upgrades.
- Hosted: encrypted multi-machine sync, team namespaces, RBAC, audit logs, SSO, and longer analytics retention.

## 3. Naming and safety policy

The safe default zone is `internal`. For a homelab, the setup wizard recommends `home.arpa`. A user who controls a real domain may delegate a subdomain such as `dev.example.com`.

DevMesh must warn or refuse by default when asked to manage:

- a public suffix such as `dev`;
- `local`, which is reserved for mDNS behavior;
- `localhost`, reverse zones, or a zone already owned by another local resolver;
- an extremely broad zone that would shadow public DNS.

An unsafe zone can only be enabled with an explicit configuration flag and a clear diagnostic. Managed-zone misses return authoritative `NXDOMAIN`; they are never forwarded, which prevents private names from leaking upstream.

Canonical names are lower-case fully qualified names internally. The API may accept names without a trailing dot. Internationalized names are converted to IDNA ASCII form before validation.

### Wildcard rules

Use DNS wildcard semantics, not shell globs:

- exact records win;
- the closest matching wildcard wins;
- `*.api.project.internal` matches one or more otherwise-unmatched names beneath that owner according to DNS wildcard rules;
- wildcard behavior is covered by conformance tests because intuitive glob behavior differs from DNS behavior.

## 4. User experience

### Configuration

The top-level configuration belongs in a single documented file. Discovery state does not get written back into it.

```yaml
version: 1

server:
  listen:
    dns: ["127.0.0.1:53", "[::1]:53"]
    http: "127.0.0.1:9053"
  upstreams:
    - "system"

zones:
  - name: "project.internal"
    default_ttl: 30s
    records:
      - name: "api"
        type: A
        values: ["127.0.0.1"]
      - name: "*.api"
        type: CNAME
        values: ["api.project.internal."]

views:
  - name: tailnet
    source_cidrs: ["100.64.0.0/10"]
    priority: 100
  - name: lan
    source_cidrs: ["192.168.0.0/16", "10.0.0.0/8"]
    priority: 50
  - name: local
    source_cidrs: ["127.0.0.0/8", "::1/128"]
    priority: 10

sources:
  docker:
    enabled: true
    socket: auto
    default_zone: "container.internal"
    implicit_names: false
```

`implicit_names` is false by default. This avoids publishing every container unexpectedly and makes labels the stable contract.

### Docker and Compose contract

Compose has no portable custom `dns:` service field, so DevMesh uses standard labels:

```yaml
services:
  api:
    image: ghcr.io/example/api:latest
    labels:
      devmesh.dns.enabled: "true"
      devmesh.dns.names: "api.shop.internal,*.api.shop.internal"
      devmesh.dns.view: "local"
      devmesh.dns.health: "docker"
```

DevMesh also reads Compose's canonical project and service labels to create optional predictable names such as `api.shop.container.internal`.

Target selection must be platform-aware:

- On native Linux, a reachable container address may be published.
- On Docker Desktop, bridge addresses are normally not host-routable. DevMesh should use a configured host address for published ports and report when a container has no usable host route.
- DNS cannot preserve a container port mapping. The CLI and dashboard must show the usable endpoint, including its port, and avoid implying that an address alone solves port routing.

### CLI surface

```text
devmesh serve                         run in the foreground
devmesh install                       install service and resolver route
devmesh uninstall                     restore resolver state and remove service
devmesh doctor                        diagnose port, resolver, socket, and zone issues
devmesh status                        show daemon and source status
devmesh query <name> [--view name]    resolve and explain the winning rule
devmesh records list
devmesh records add <name> <type> <value>
devmesh records remove <id>
devmesh sources list
devmesh sources resync <source>
devmesh config validate
devmesh run --name api.project.internal -- <command>
```

Mutating commands talk to the local API. `devmesh run` starts a process, registers its chosen address for the process lifetime, and reliably unregisters it on exit.

### Dashboard

The embedded UI is served only on loopback by default and contains:

- records with source, view, target, TTL, and health;
- live discovery/source state and last successful reconciliation;
- query explorer with an explanation trace;
- conflict, loop, and unreachable-target warnings;
- configuration display with secrets redacted;
- basic query counts, latency, cache hit rate, and top failed names.

The dashboard should initially be read-mostly. High-risk operations such as installing a CA or changing system DNS remain explicit CLI actions.

## 5. Architecture

Start as a modular monolith and one binary. DNS needs a small failure surface; distributed services would add more ways for local name resolution to fail.

```text
                         +-----------------------+
 Docker events -------->|                       |
 Static YAML ---------->|  source reconcilers   |
 Process registrations >|                       |
                         +-----------+-----------+
                                     |
                              normalized records
                                     |
                         +-----------v-----------+
                         | desired-state engine  |
                         | conflicts + ownership |
                         +-----------+-----------+
                                     |
                          immutable answer snapshot
                                     |
               +---------------------+---------------------+
               |                                           |
        +------v-------+                            +------v-------+
        | DNS UDP/TCP  |                            | HTTP API/UI  |
        | views/cache  |                            | CLI/metrics  |
        +------+-------+                            +--------------+
               |
          upstream DNS
```

### Core packages

- `internal/dnsserver`: wire protocol, UDP/TCP listeners, response construction.
- `internal/resolver`: exact/wildcard lookup, view selection, forwarding, cache, loop protection.
- `internal/registry`: normalized records, ownership, conflicts, immutable snapshots.
- `internal/source`: common adapter interface and reconciliation lifecycle.
- `internal/source/static`: YAML-defined records.
- `internal/source/docker`: Docker events, inspection, Compose metadata, labels.
- `internal/source/process`: lease-based API and `devmesh run` registrations.
- `internal/api`: local REST API and event stream.
- `internal/store`: schema migrations and durable local state.
- `internal/install`: OS-specific service and resolver integration.
- `internal/health`: bounded probes and health state.
- `internal/telemetry`: logs, Prometheus metrics, and privacy controls.
- `web`: dashboard source, embedded into the binary at release time.

Use `github.com/miekg/dns` behind the `dnsserver` boundary for the initial implementation, pinned to a reviewed version. Its v1 repository has entered fix-oriented maintenance, so the wrapper prevents protocol-library details from spreading and leaves room to evaluate its v2 line later.

Use the official Docker Go client with API version negotiation. On startup, list and inspect current containers; then consume Docker's event stream. Events are hints, not the source of truth: reconnect with backoff and periodically reconcile the full list so missed events cannot leave stale records.

### Internal record model

```go
type Record struct {
    ID        string
    Name      string
    Type      uint16
    Values    []string
    TTL       time.Duration
    Zone      string
    View      string
    Source    SourceRef
    Health    HealthState
    Revision  uint64
    ExpiresAt *time.Time
}
```

The source supplies desired records. The registry validates and normalizes them, resolves ownership conflicts deterministically, and atomically swaps a read-only snapshot. DNS request handlers never wait on Docker, disk, HTTP, health checks, or a cloud service.

### Conflict precedence

Default order, configurable only at source boundaries:

1. Explicit static/user record.
2. Leased process/API registration.
3. Docker label.
4. Optional implicit Docker/Compose name.
5. Later discovery adapters.

Equal-precedence conflicts fail closed for that name and produce a prominent diagnostic; DevMesh should not silently choose a target based on event ordering.

### Query path

1. Parse and validate one DNS question; apply rate and size limits.
2. Determine the view from the receiving listener and source IP. Never trust forwarded client headers from an untrusted proxy.
3. Find the longest managed zone.
4. Resolve exact name, then DNS wildcard, then applicable rewrite.
5. Filter unhealthy targets only when a healthy alternative exists; otherwise follow the record's configured fail-open/fail-closed behavior.
6. For a managed-zone miss, return authoritative `NXDOMAIN` with the zone SOA.
7. For an unmanaged name, select a conditional upstream, consult cache, and forward with strict deadlines.
8. Emit bounded metrics and a sampled/redacted query event.

### Storage

- YAML is the portable, reviewable server configuration.
- A CGO-free embedded SQLite database stores user-created API records, leases, source checkpoints, install metadata, migrations, and bounded analytics.
- Discovered records remain derived state and are rebuilt after restart.
- The daemon uses atomic config reload: parse, validate all errors, construct a new snapshot, then swap. An invalid reload leaves the last good configuration active.

The state schema must include a monotonically increasing migration version and support export/import before `v1.0`.

## 6. Split DNS design

A view is an ordered policy containing source CIDRs, optional listener identity, records, and upstream routes. The most specific source prefix wins; configured priority breaks ties. Ambiguous ties are configuration errors.

Source-address views work for direct UDP/TCP traffic. They are not reliable through NAT or a generic DoH reverse proxy, so later encrypted transports need one of:

- a dedicated listener per view;
- an authenticated client/device identity;
- an explicitly trusted proxy that conveys identity.

DoH/DoT are transports, not the first milestone. Local stub-to-loopback traffic already stays on the host, while premature encrypted-listener support would expand certificate, authentication, and abuse concerns.

## 7. Installation and portability

`devmesh install` is a privileged, explicit step; normal daemon operation is unprivileged wherever the OS permits it.

### macOS

- Install a `launchd` service.
- Create `/etc/resolver/<zone>` files for per-zone routing.
- Save a manifest and backups of files DevMesh owns.

### Linux

- Prefer a `systemd` service and `systemd-resolved` routing domains when detected.
- Grant only the capability needed to bind port 53 rather than running the whole daemon as root.
- Provide documented NetworkManager and manual modes when `systemd-resolved` is absent.

### Windows

- Install a Windows service.
- Use supported DNS policy/adapter configuration with an exact rollback record.
- Validate behavior on current Windows 11 and Windows Server CI images before claiming support.

### Containers

A container image is useful for servers and homelabs, but it is not the primary desktop installation because port 53, host resolver changes, Docker socket access, and Docker Desktop networking are clearer from a host agent. Docker socket mounting must be opt-in and documented as host-equivalent privilege.

### Reversibility requirements

- Never replace an existing resolver configuration without detecting and explaining it.
- Record every changed path, service, capability, and policy entry.
- Make uninstall idempotent.
- Restore the exact prior state when it is still safe to do so.
- If the prior state changed externally, stop and present a manual recovery plan instead of overwriting it.

## 8. HTTPS plan

“Automatic HTTPS” consists of two separate capabilities:

1. A gateway that routes HTTP by `Host` and TLS by SNI.
2. A certificate authority and trust-distribution workflow.

For private names such as `*.internal`, publicly trusted CAs cannot issue certificates. DevMesh `v0.2` should create a local CA only after explicit approval, keep its private key in the OS credential/key store where possible, issue short-lived leaf certificates, and install trust separately on each client. Root trust installation must never happen as a side effect of starting DNS.

For a real user-owned domain, a later ACME DNS-01 adapter can obtain publicly trusted certificates. Credentials must be scoped to the delegated development subdomain.

The gateway and CA remain separate modules so users can pair DevMesh DNS with Caddy, Traefik, or their existing PKI.

## 9. Security and privacy model

### Threats to address before `v0.1`

- DNS rebinding and unsafe answers for private/public boundaries.
- Open-recursion exposure on LAN or internet interfaces.
- Amplification through UDP responses.
- Malicious or compromised Docker containers claiming arbitrary names.
- Docker socket privilege and untrusted label content.
- API/dashboard cross-site request forgery and unauthenticated non-loopback access.
- Symlink and ownership attacks during privileged installation.
- Query-log leakage of private service names.
- Forwarding loops involving the system resolver and DevMesh.
- Cache poisoning and malformed DNS packets.

### Defaults

- Bind DNS and HTTP to loopback only.
- Require explicit allowlists for non-loopback listeners and recursion clients.
- Permit Docker labels to claim only configured zones.
- Disable query-name logging by default; aggregate metrics locally with bounded retention.
- Set strict UDP response limits and support TCP fallback.
- Drop privileges after binding, or use socket activation/capabilities.
- Authenticate the API before any non-loopback bind.
- Never expose the Docker socket through the API.
- Reject upstream addresses that resolve back to DevMesh itself.

## 10. Kubernetes, Tailscale, and mDNS follow-on adapters

### Kubernetes

Watch Services and `discovery.k8s.io/v1` EndpointSlices through `client-go` informers. EndpointSlices, rather than the deprecated legacy Endpoints API, provide ready endpoint addresses and scale. The adapter should:

- use namespace/label allowlists;
- publish ClusterIP, ExternalName, load balancer, or ready endpoint addresses according to an explicit policy;
- respect IPv4/IPv6 families and readiness;
- relist after watch expiration and rebuild desired state;
- ship least-privilege read-only RBAC examples;
- never assume pod or ClusterIP addresses are reachable from a laptop.

### Tailscale

Support two distinct use cases:

- import tailnet devices and their Tailscale addresses as records;
- make DevMesh a restricted nameserver for selected private zones.

Prefer documented Tailscale APIs and explicit auth. The adapter must not overwrite MagicDNS names, must avoid forwarding loops, and must explain that control-plane APIs and local reachability are separate. Hosted tailnet sync is optional; local DNS continues with the last valid snapshot.

### mDNS

Do not serve `.local` through ordinary unicast DNS. A future bridge discovers selected mDNS services and republishes them into a configured DevMesh zone such as `printer.home.arpa`. Bridging is allowlist-based to prevent noisy or sensitive device publication.

## 11. Repository layout

```text
.
|-- cmd/devmesh/              CLI and daemon entry point
|-- internal/
|   |-- api/
|   |-- config/
|   |-- dnsserver/
|   |-- health/
|   |-- install/
|   |-- registry/
|   |-- resolver/
|   |-- source/
|   |   |-- docker/
|   |   |-- process/
|   |   `-- static/
|   |-- store/
|   `-- telemetry/
|-- web/                      dashboard application
|-- docs/
|   |-- design/
|   |-- guides/
|   `-- reference/
|-- packaging/
|   |-- docker/
|   |-- launchd/
|   |-- systemd/
|   `-- windows/
|-- integration/              hermetic integration tests
|-- go.mod
|-- Makefile
`-- README.md
```

Do not create a plugin system in the first release. Define a small internal source interface, prove it with static/Docker/process sources, then stabilize an external extension contract only when real third-party use cases exist.

## 12. Delivery milestones

### M0 — executable DNS core

Deliverables:

- Go module, CI, linting, version package, reproducible build metadata.
- YAML config and validation.
- Static zones, exact/wildcard lookup, UDP/TCP, forwarding, cache.
- Unit tests using in-process DNS clients; no live internet requirement.

Exit criteria:

- `dig @127.0.0.1 -p 5354 api.project.internal` returns the configured answer.
- Managed misses return authoritative `NXDOMAIN`; unmanaged names forward.
- Race detector and fuzz tests pass for config and DNS message handling.

### M1 — discovery and explainability

Deliverables:

- Docker initial list, event stream, reconnect, and periodic reconciliation.
- Labels and Compose-derived optional names.
- Immutable registry snapshots and deterministic conflicts.
- CLI `query`, `status`, `sources`, and `config validate`.
- Local API and minimal dashboard.

Exit criteria:

- A labeled container appears within two seconds and disappears after stop within the configured grace period.
- Restarting Docker or dropping the event stream does not leave a permanent stale record.
- Every answer can be traced to a source, view, and rule.

### M2 — safe host installation (`v0.1`)

Deliverables:

- Installer, uninstaller, service definitions, resolver routes, and `doctor` for all claimed OSes.
- Signed release archives, checksums, SBOM, container image, upgrade guide.
- Security review of listeners, API, installer, Docker permissions, forwarding, and logs.

Exit criteria:

- Fresh-machine install, reboot, upgrade, and uninstall tests pass on macOS, Ubuntu/Fedora, and Windows.
- An interrupted install is recoverable.
- Uninstall returns the resolver to its prior configuration.
- The daemon serves the last valid local snapshot when Docker and upstream DNS are unavailable.

### M3 — local HTTPS (`v0.2`)

Deliverables:

- Optional HTTP/TLS gateway.
- Explicit local-CA creation/trust workflow and short-lived certificate issuance.
- Health probes with bounded concurrency and clear fail policy.

### M4 — infrastructure adapters (`v0.3`)

Deliverables:

- Kubernetes Service/EndpointSlice adapter and RBAC examples.
- Tailscale import and restricted-nameserver guide.
- Cross-network views and reachability diagnostics.

## 13. Test strategy

- Table-driven unit tests for normalization, precedence, wildcards, views, TTLs, and negative answers.
- Go fuzz targets for DNS parsing boundaries, config decoding, label parsing, and API inputs.
- Race-detector tests around snapshot swaps and source reconciliation.
- Hermetic DNS integration tests with fake upstreams for timeouts, truncation, TCP fallback, loops, and cache behavior.
- Docker integration tests using throwaway networks and containers.
- Network-namespace tests on Linux for source-based views and unreachable targets.
- Golden tests for install plans; VM tests perform real install/reboot/uninstall cycles.
- Browser tests for the dashboard's query explorer and diagnostics.
- Benchmarks for hot-cache and authoritative responses, with a latency budget rather than vanity throughput.

Initial service objectives on a normal developer laptop:

- authoritative/cache-hit p99 below 5 ms at 1,000 queries/second;
- no DNS handler disk or discovery I/O;
- bounded memory cache and analytics storage;
- graceful shutdown without dropping the durable state transaction.

## 14. Observability

Expose Prometheus-format metrics locally:

- query count by result class and transport;
- duration histogram;
- cache hit/miss/eviction counts;
- upstream health and timeout counts;
- records by source/view/health;
- source reconciliation success, failure, and age;
- conflict and rejected-record counts.

Avoid high-cardinality labels such as full query names or container IDs. A local, opt-in query ring buffer may retain redacted recent events for debugging. Cloud analytics are opt-in and must never include raw private names by default.

## 15. Open-source and business boundary

Keep the complete single-machine data plane open source:

- DNS server and forwarder;
- all local discovery adapters;
- local API, CLI, dashboard, metrics, and HTTPS gateway;
- configuration, export/import, and local analytics;
- documented local multi-node configuration where practical.

Charge for operational coordination rather than basic resolution:

- encrypted hosted sync and device enrollment;
- team namespace ownership and approvals;
- RBAC, SSO/SCIM, audit retention, policy distribution;
- hosted analytics, fleet health, and support.

Proposed packaging:

- Community: unlimited local records on one machine, no account.
- Pro: multiple enrolled machines, hosted sync, tailnet convenience, longer analytics.
- Team: shared namespaces, RBAC, audit logs, SSO, policy controls.

Avoid a hard 100-record cap in the local open-source server. It is easy to bypass, creates surprising DNS outages, and weakens trust in infrastructure software. Limits can apply to hosted sync/storage instead.

## 16. Decisions to make before implementation

These choices should be captured as short architecture decision records during M0:

1. Final project/module name and repository URL.
2. Exact embedded SQLite driver after build-size and cross-compilation tests.
3. Dashboard stack, favoring a small static bundle with no runtime Node.js dependency.
4. Supported OS/version matrix for `v0.1`.
5. Whether `internal` is the literal default zone or setup always asks for a subzone such as `<machine>.internal`.
6. Default Docker target behavior per platform and how unusable targets are represented.
7. Analytics retention default and redaction behavior.

## 17. Definition of “clone, download, and use”

The project is not ready merely when it compiles. A release satisfies the goal when:

- `go build ./cmd/devmesh` works with the documented current Go toolchain;
- `make test` runs without external accounts or live internet;
- release archives contain one binary plus license/checksum metadata;
- a user can install, resolve a labeled container, diagnose a failure, and uninstall using only the CLI and local docs;
- Docker, Kubernetes, Tailscale, cloud sync, and telemetry are all optional;
- startup with an invalid config preserves the last working service or fails with actionable errors;
- upgrades are backward compatible within the documented support window;
- the repository contains contributing, security, release, and architecture documentation before `v1.0`.

## 18. Reference choices

- Go DNS protocol/server library: <https://github.com/miekg/dns>
- Docker Engine event API: <https://docs.docker.com/reference/api/engine/>
- Docker Compose labels: <https://docs.docker.com/reference/compose-file/services/#labels>
- Kubernetes Services: <https://kubernetes.io/docs/concepts/services-networking/service/>
- Kubernetes EndpointSlices: <https://kubernetes.io/docs/concepts/services-networking/endpoint-slices/>
- Tailscale MagicDNS behavior: <https://tailscale.com/docs/features/magicdns>
- IETF `home.arpa`: <https://datatracker.ietf.org/doc/html/rfc8375>
- ICANN reservation of `.internal`: <https://www.icann.org/en/board-activities-and-meetings/materials/approved-resolutions-special-meeting-of-the-icann-board-29-07-2024-en>
- CA/Browser Forum certificate requirements: <https://cabforum.org/working-groups/server/baseline-requirements/requirements/>
