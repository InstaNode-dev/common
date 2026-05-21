# common

Shared Go packages for the [InstaNode](https://instanode.dev) platform — the
zero-friction developer infrastructure that lets agents provision databases,
caches, queues, object storage, and full application deployments with a single
HTTP call.

This repo is the **shared substrate** consumed by the three backend services:

- **api** — agent-facing HTTP surface (port 8080)
- **worker** — River-backed background jobs (expiry, billing, propagation, email)
- **provisioner** — gRPC service that creates and destroys real databases

Module path: `instant.dev/common` (Go 1.25+).

## Why a separate module?

Every backend service depends on the same tier definitions, the same encryption
keyring, the same readiness check shape, and the same provider interfaces. We
factored those out so a change to a tier limit, a new provisioner backend, or a
new readiness check lands in one place and propagates to every service via a
single `go mod` bump — instead of three drifting copies.

## Packages

| Package | Purpose |
|---|---|
| [`buildinfo`](./buildinfo) | ldflag-stamped `GitSHA` + `BuildTime` + `Version` exposed on every service's `/healthz`. Lets ops verify "is the running pod actually the commit I just pushed?" — see [InstaNode CLAUDE.md rule 14](https://instanode.dev) (Build-SHA gate). |
| [`crypto`](./crypto) | AES-256-GCM keyring (multi-key with rotation), JWT signing helpers, IP fingerprint hashing (SHA256 over `/24` subnet + ASN), and base32 token generation. Fails open on decrypt errors so a key rotation doesn't break reads. |
| [`logctx`](./logctx) | `slog.Handler` wrapper that injects `commit_id`, `request_id`, `team_id`, `resource_token` from `context.Context` into every structured log line. Keys are defined once here so api, worker, and provisioner emit consistent JSON. |
| [`plans`](./plans) | Tier registry loaded from `plans.yaml` — the single source of truth for per-tier limits (storage MB, connection caps, deployment slots, webhook retention, etc.). Iterated by `/api/v1/capabilities` so adding a tier in `plans.yaml` + `rank.go` automatically surfaces to clients. |
| [`queueprovider`](./queueprovider) | Pluggable factory + `QueueCredentialProvider` interface for NATS / RabbitMQ / Kafka / `legacyopen` backends. NATS impl mints per-tenant account JWTs + user NKeys; RabbitMQ + Kafka are portability skeletons (return `ErrNotImplemented`); `legacyopen` is the cutover shim for pre-isolation tenants. |
| [`readiness`](./readiness) | Shared `Registry` + `Check` interface for deep `/readyz` endpoints. Reusable check constructors for HTTP/GET, gRPC dial, Postgres ping, Redis ping, Mongo ping, NATS ping. Per-check criticality (critical / degraded) and 10-15s cache TTL. Emits Prometheus + slog. |
| [`resourcestatus`](./resourcestatus) | Shared enum for resource lifecycle states (`active`, `expired`, `deleting`, `deleted`, …) plus the expiry-warning ladder stages (6h / 2h / 1h imminent). |
| [`resourcetype`](./resourcetype) | Shared enum for provisioned resource kinds (`postgres`, `redis`, `mongodb`, `queue`, `storage`, `webhook`, `deploy`, …). |
| [`storageprovider`](./storageprovider) | Pluggable factory + `StorageCredentialProvider` interface for DigitalOcean Spaces / Cloudflare R2 / AWS S3 / MinIO backends. Capability-aware: each backend declares whether it supports prefix-scoped keys, bucket-scoped keys, STS temp credentials, and bucket-per-tenant — the api uses this to decide between `prefix-scoped`, `prefix-scoped-temporary`, `shared-master-key`, and `broker` modes. Live prod uses DO Spaces in `prefix-scoped` mode. |

## Using in a downstream service

```go
import (
    "instant.dev/common/buildinfo"
    "instant.dev/common/logctx"
    "instant.dev/common/plans"
    "instant.dev/common/readiness"
)
```

This module is consumed via a `replace` directive in the api / worker /
provisioner `go.mod` files when developing across the repo bundle locally. In
prod CI, tagged releases are pulled directly.

## Versioning

`common` follows semantic versioning. **Contract changes** (a new tier in
`plans.yaml`, a renamed `resourcetype`, a changed `readiness.Status` shape)
must land in synchronised PRs across `common` + `api` + `worker` +
`provisioner` — see the [InstaNode handoff doc](https://instanode.dev)
rule 22 ("Contract changes touch all surfaces in one PR").

## Testing

```bash
go test ./...
```

Each package ships unit tests; contract tests live alongside the factory in
`queueprovider/contract_test.go` and `storageprovider/contract_test.go` to
verify every backend implements the interface consistently.

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). Issues + PRs on this repo are
welcome for shared-package bugs; platform-wide bugs (API behaviour, dashboard,
billing, deploy pipeline) belong on the [api repo](https://github.com/InstaNode-dev).

## Security

See [SECURITY.md](./SECURITY.md). Report vulnerabilities privately to
**security@instanode.dev** — do not open a public issue.

## License

[MIT](./LICENSE) © 2026 InstaNode.
