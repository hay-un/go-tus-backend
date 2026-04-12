# CLAUDE.md — go-tus-backend

> Project-wide conventions and architecture: see `../CLAUDE.md`
> Deployment config and env vars: see `../meta/CLAUDE.md`

## Service purpose

TUS upload server, file download/streaming, bucket CRUD, and share proxy to go-shares. Port 8080.
Module: `codirs/backend`

## Key patterns

- JWT bypass in dev: empty `KEYCLOAK_ISSUER` → injects wildcard admin Claims (skips auth)
- Stream endpoint accepts `?token=` query param (for `<video src>` tags in browser)
- All handlers check `ClaimsFromContext`; if nil → skip access check (dev/test mode)
- `AuditProducer` interface: `KafkaAuditProducer` (prod), `NoopAuditProducer` (dev/test)
- All test App literals must include `Audit: &NoopAuditProducer{}`
- `OwnsBucket(bucket)` in Claims: explicit allowedBuckets entry (non-wildcard) or admin role
- `canAccessBucket(ctx, claims, bucket)`: checks JWT allowedBuckets first, then calls go-shares HTTP
- CORS via `uploader.CORS()` middleware — `internal/uploader/cors.go`
- Follow existing patterns in `internal/uploader/` for new handlers

## Commands

```bash
make test                # unit tests (~346)
make test-coverage       # HTML coverage report
make test-integration    # needs MinIO + Keycloak running
go run cmd/server/main.go
```

## What NOT to do

- Never add auth logic beyond JWT validation — Keycloak owns auth
- Never block on audit writes — always async Kafka; use NoopAuditProducer in tests
- Never write to `audit_log` directly — only via Kafka
- Never call go-shares directly from frontend — always proxy through this service
