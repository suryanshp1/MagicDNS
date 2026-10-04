# DevMesh DNS user guide

This guide covers the currently implemented M0 DNS core. It runs as a local authoritative DNS server for configured private zones and forwards all other names to upstream resolvers.

Docker discovery, system DNS installation, the web dashboard, Kubernetes, and Tailscale integration are planned but are not available yet.

## Requirements

- Go 1.25 or newer when building from source.
- An available UDP and TCP port.
- Access to at least one upstream DNS server when public-name forwarding is needed.

The example uses port `5354`, so root or administrator privileges are not required. Port `53` normally requires elevated privileges or an OS-specific capability.

## Build from source

```bash
git clone <repository-url>
cd devmesh
make test
make build
```

The binary is written to `bin/devmesh`.

Check it with:

```bash
./bin/devmesh version
./bin/devmesh --help
```

## First run

Validate the included example:

```bash
./bin/devmesh config validate --config devmesh.example.yaml
```

Start the server:

```bash
./bin/devmesh serve --config devmesh.example.yaml
```

The example listens on `127.0.0.1:5354` over UDP and TCP. Leave it running and open another terminal:

```bash
./bin/devmesh query --server 127.0.0.1:5354 api.project.internal
```

Expected answer:

```text
status=NOERROR authoritative=true
api.project.internal. 30 IN A 127.0.0.1
```

Stop the server with `Ctrl+C`. DevMesh waits for the listeners to shut down before exiting.

## Configuration

DevMesh uses one YAML configuration file. Unknown fields, invalid records, duplicate zones, unsafe namespaces, and malformed durations cause validation to fail before the server starts.

```yaml
version: 1

server:
  listen:
    dns:
      - "127.0.0.1:5354"
  upstreams:
    - system
  timeout: 2s
  cache_ttl: 30s
  cache_size: 4096

zones:
  - name: project.internal
    default_ttl: 30s
    records:
      - name: api
        type: A
        values:
          - 127.0.0.1
```

### Server options

| Field | Default | Meaning |
| --- | --- | --- |
| `server.listen.dns` | `127.0.0.1:5354` | UDP and TCP addresses on which DevMesh listens. |
| `server.upstreams` | `system` | DNS servers used for names outside managed zones. |
| `server.timeout` | `2s` | Deadline for each upstream attempt. |
| `server.cache_ttl` | `30s` | Maximum time a forwarded response stays cached; `0s` disables caching. |
| `server.cache_size` | `4096` | Maximum number of forwarded responses retained in memory. |

`system` reads the nameservers from `/etc/resolv.conf`. Explicit upstream IP addresses can be supplied with or without a port:

```yaml
server:
  upstreams:
    - "192.0.2.53"
    - "[2001:db8::53]:53"
```

DevMesh tries upstreams in order. A truncated UDP response is retried over TCP.

### Zones

Each zone is authoritative. A name inside the zone is never forwarded to an upstream resolver.

```yaml
zones:
  - name: lab.internal
    default_ttl: 1m
    records: []
```

If `missing.lab.internal` does not exist, DevMesh returns authoritative `NXDOMAIN`. This prevents private names from leaking to public DNS.

Recommended namespaces:

- Use `.internal` for private development and internal infrastructure.
- Use `home.arpa` for residential homelabs.
- Use a subdomain of a domain you own when publicly trusted certificates are required later.

DevMesh rejects `.dev`, `.home`, `.local`, and `.localhost` zones by default. To deliberately override that protection:

```yaml
version: 1
allow_unsafe_zones: true
```

This override affects the entire configuration and should be used only when the naming collision is understood.

## Record types

DevMesh currently accepts `A`, `AAAA`, `CNAME`, `TXT`, and `SRV` records.

Record names without a trailing dot are relative to their zone. Use `@` for the zone apex. Fully qualified record names must remain inside the zone.

### A and AAAA

```yaml
- name: api
  type: A
  values:
    - 127.0.0.1
    - 192.168.1.20

- name: api-v6
  type: AAAA
  values:
    - "2001:db8::20"
```

Multiple values form one DNS record set.

### CNAME

```yaml
- name: backend
  type: CNAME
  values:
    - api.lab.internal.
```

Use a fully qualified target ending in a dot. A CNAME owner cannot also contain another record type.

### TXT

TXT data follows DNS presentation syntax. Quote values containing spaces:

```yaml
- name: metadata
  type: TXT
  values:
    - '"environment=development owner=platform"'
```

### SRV

An SRV value contains priority, weight, port, and a fully qualified target:

```yaml
- name: _http._tcp
  type: SRV
  values:
    - "0 0 8080 api.lab.internal."
```

Query it with:

```bash
./bin/devmesh query \
  --server 127.0.0.1:5354 \
  --type SRV \
  _http._tcp.lab.internal
```

### Per-record TTL

Records inherit `default_ttl` from their zone. Override it when needed:

```yaml
- name: temporary
  type: A
  ttl: 5s
  values:
    - 127.0.0.1
```

TTLs must be at least one second.

## Wildcards

Only a complete left-most label can be a wildcard:

```yaml
- name: "*.api"
  type: CNAME
  values:
    - api.lab.internal.
```

This can answer names such as `customer.api.lab.internal`. Exact records take precedence. DevMesh follows DNS closest-encloser behavior, which differs from filesystem glob matching; a more general wildcard does not cross an existing, more specific DNS node.

Patterns such as `api-*`, `foo.*`, and multiple wildcard labels are invalid.

## CLI reference

### Start the server

```bash
devmesh serve --config /path/to/devmesh.yaml
```

The default configuration path is `devmesh.yaml` in the current directory.

### Validate configuration

```bash
devmesh config validate --config /path/to/devmesh.yaml
```

Successful output includes the number of zones, listeners, and upstream servers. Validation reports all detected configuration problems together when possible.

### Query DevMesh

```bash
devmesh query [flags] <name>
```

Flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--server` | `127.0.0.1:5354` | DevMesh DNS server to query. |
| `--type` | `A` | DNS record type. |
| `--timeout` | `2s` | Client query deadline. |

Examples:

```bash
devmesh query api.lab.internal
devmesh query --type AAAA api-v6.lab.internal
devmesh query --type TXT metadata.lab.internal
devmesh query --type SRV _http._tcp.lab.internal
```

The status line reports the DNS response code, whether the response was authoritative, and elapsed time.

### Display the version

```bash
devmesh version
```

Release builds can inject a version through the Makefile:

```bash
make build VERSION=0.1.0
```

## Forwarding and caching

Queries outside configured zones are forwarded to the configured upstreams. DevMesh caches successful and negative upstream responses when they contain cacheable TTL data.

The effective cache lifetime is the smaller of:

- the smallest TTL in the response; and
- `server.cache_ttl`.

Returned TTLs decrease while an answer remains cached. The cache is bounded by `server.cache_size`; when full, it removes an expired entry or the oldest retained entry.

DevMesh does not perform full recursive resolution itself.

## Testing changes

```bash
make test
make test-race
make vet
```

Run the complete local check:

```bash
make check
```

The test suite is hermetic and does not require a live DNS server, Docker daemon, external account, or internet connection after Go modules have been downloaded.

## Troubleshooting

### `bind: address already in use`

Another process owns the configured port. Check the address in `server.listen.dns` or choose another port for development:

```yaml
server:
  listen:
    dns: ["127.0.0.1:5454"]
```

Then query the same port:

```bash
devmesh query --server 127.0.0.1:5454 api.lab.internal
```

### `permission denied` when binding port 53

Use port `5354` during development. Running the entire program as root is not recommended. A later milestone will install the narrow OS capability and resolver routing required for port 53.

### Public names return `SERVFAIL`

Check that the configured upstream is reachable and accepts DNS over UDP and TCP. Validate whether `system` found a usable nameserver:

```bash
devmesh config validate --config devmesh.yaml
```

An authoritative local name continues to work even when upstream DNS is unavailable.

### A private name returns `NXDOMAIN`

If the response says `authoritative=true`, DevMesh owns the matching zone but could not find an exact or applicable wildcard record. Check:

- the zone suffix;
- relative versus fully qualified record names;
- the queried record type;
- wildcard closest-encloser behavior.

### Configuration fails after adding a field

The decoder intentionally rejects unknown fields. Compare the field with this guide and `devmesh.example.yaml`; this catches spelling mistakes that would otherwise silently disable configuration.

## Current limitations

- DevMesh does not yet configure the operating system to use it automatically.
- Configuration is loaded only at startup; live reload is not implemented.
- Records come from YAML only; Docker and process discovery are not implemented.
- Split-horizon views are planned but not implemented in M0.
- There is no API, dashboard, persistent database, DoH, or DoT listener yet.
- Health checks and health-aware answers are not implemented.
- Query metrics and structured request logs are not exposed yet.

See [docs/PLAN.md](docs/PLAN.md) for the architecture, security model, and delivery milestones.

