# DevMesh DNS

MagicDNS for developers, homelabs, and local infrastructure.

DevMesh DNS is planned as a self-hosted Go service that discovers local services and gives them stable, readable names without hand-editing `/etc/hosts` or assembling several DNS tools.

```text
Docker / static config / local processes / Kubernetes / Tailscale
                              |
                              v
                         DevMesh DNS
                              |
                 +------------+------------+
                 |                         |
          managed local names        upstream DNS
          api.shop.internal          example.com
          nas.home.arpa
```

## Project status

Milestone M0 is implemented. DevMesh can load and strictly validate YAML configuration, serve authoritative local zones over UDP and TCP, resolve exact and wildcard records, return authoritative negative answers, forward unmanaged names to upstream DNS, retry truncated UDP answers over TCP, and cache forwarded responses with correct TTL aging.

Docker discovery, host installation, the API, and the dashboard are not implemented yet. The implementation sequence, architecture, security model, and release criteria are in [docs/PLAN.md](docs/PLAN.md).

For installation, configuration, record examples, CLI usage, and troubleshooting, see the [user guide](GUIDE.md).

The first usable release remains intentionally smaller than the full product vision:

- one self-contained Go binary;
- static `A`, `AAAA`, `CNAME`, `TXT`, and `SRV` records;
- wildcard records;
- forwarding for names DevMesh does not own;
- Docker and Docker Compose discovery through labels;
- source-network-based split DNS;
- local API, CLI, health endpoint, and embedded dashboard;
- safe installation and removal on Linux, macOS, and Windows;
- YAML configuration with an embedded local state database;
- no account and no cloud dependency.

Kubernetes, Tailscale, HTTPS automation, mDNS bridging, Git sync, and teams follow after the local core is reliable.

## Build and run the current DNS core

Requirements: Go 1.25 or newer.

```bash
git clone <repository-url>
cd devmesh

make test
make build

# The example listens on unprivileged port 5354.
./bin/devmesh config validate --config devmesh.example.yaml
./bin/devmesh serve --config devmesh.example.yaml
```

From another terminal:

```bash
./bin/devmesh query --server 127.0.0.1:5354 api.project.internal
./bin/devmesh query --server 127.0.0.1:5354 --type SRV _http._tcp.project.internal
```

Expected `A` response:

```text
status=NOERROR authoritative=true
api.project.internal. 30 IN A 127.0.0.1
```

The current server does not change system DNS settings. Query port `5354` explicitly during M0; reversible port-53 and split-resolver installation belongs to the host-installation milestone.

## Current commands

```text
devmesh serve [--config path]
devmesh config validate [--config path]
devmesh query [--server host:port] [--type A] <name>
devmesh version
```

## Namespace guidance

DevMesh will use `internal` as its default private-use suffix and offer `home.arpa` for residential networks. It will not claim `.dev` or `.home` by default:

- `.dev` exists in public DNS, so shadowing it can produce confusing failures and leaks.
- `home.arpa` is the IETF-designated home-network namespace; bare `.home` is deprecated for this use.
- `.internal` was permanently reserved by ICANN for private-use applications.
- `.local` belongs to mDNS and must not be captured by ordinary unicast DNS.

Users who own a public domain can also configure a delegated subdomain such as `dev.example.com`; that is the preferred route for publicly trusted certificates.

## License and distribution direction

The core is licensed under Apache-2.0 and remains useful with no hosted account. Optional hosted sync, team policy, audit retention, RBAC, and SSO can fund the project without making local DNS depend on the cloud.
# MagicDNS
