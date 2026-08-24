# Estate Financial Claim Service

Estate Financial Claim Service coordinates authorized inquiries for a deceased person's bank and insurance accounts and the controlled withdrawal of eligible small deposits. It exposes a JSON HTTP API backed by SQLite and durable worker jobs.

Claimants authenticate, submit a case with deceased-person and relationship details, and follow only their own cases. Case officers review evidence and dispatch inquiries to configured banking and insurance hubs. Completed inquiry responses create masked financial-account records. Claimants select eligible accounts, while supervisors approve the claim and atomically reserve the selected accounts before a payout instruction is queued.

Server-side sessions are hashed at rest, expire, and can be revoked by logout. Mutating workflows use persistent idempotency records, optimistic versions and database constraints. Transactional audit events retain the actor, request ID and outcome while redacting sensitive values.

## Run locally

Requirements: Go 1.22.5 or a compatible Go 1.22 toolchain.

```sh
go mod download
BOOTSTRAP_PASSWORD='replace-with-a-long-password' go run ./cmd/server
```

Configuration is provided by environment variables documented in `.env.example`. When `BOOTSTRAP_PASSWORD` is set, the server creates development accounts for `claimant@example.test`, `officer@example.test`, and `supervisor@example.test`. Production provisioning should leave this variable unset and use an operator-controlled identity provisioning path.

Health endpoints are public: `GET /healthz` and `GET /readyz`. Readiness executes a database ping. All `/v1` routes other than login require `Authorization: Bearer <token>`. Versioned transitions require `If-Match`, and case submission requires `Idempotency-Key`.

## Verify

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build ./...
```

The tests use temporary real SQLite databases and do not require online services. They cover migrations, transaction rollback, session expiry and revocation, role enforcement, state transitions, idempotency, optimistic concurrency, inquiry reconciliation, account reservation, durable worker recovery, HTTP errors, request IDs and restart persistence.

## Container

The root Dockerfile builds `./cmd/server` with the Go version declared by this repository and runs as a non-root user. Its default entrypoint starts the API and the durable worker.

```sh
docker buildx build --platform linux/amd64 --load -t estate-service:amd64 .
docker run --rm -p 8080:8080 estate-service:amd64
```
