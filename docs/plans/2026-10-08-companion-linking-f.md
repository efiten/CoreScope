# Companion Linking (Sub-project F) Implementation Plan Index

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement the parts below, in order, task-by-task.

**Goal:** Let a logged-in user own the CoreDrive RX companions they drive with: RX logs in with a scoped device token, the companion proves ownership by signing a server challenge with its own key, the link adds it to the user's My nodes, the account page lists device tokens and companions, the coverage page gets "My coverage", and an operator can optionally make the ingestor accept client data from linked companions only.

**Spec:** `docs/specs/2026-10-08-companion-linking-design.md`.

## Parts, in order

| Order | Part | Plan | Tasks |
|---|---|---|---|
| 1 | F1: users store (`internal/users`): schema v6, device sessions, link challenges, companion links, janitor prune | [`2026-10-08-companion-linking-f1-store.md`](2026-10-08-companion-linking-f1-store.md) | 5 |
| 2 | F2: server (`cmd/server`, `internal/sigvalidate`): device-token login, bearer auth and scope, companion routes, transfer audit and mail, My-nodes merge, `rx-coverage?mine=1`, CORS, client-config flag, OpenAPI | [`2026-10-08-companion-linking-f2-server.md`](2026-10-08-companion-linking-f2-server.md) | 12 |
| 3 | F3: ingestor linked-only filter, account page, coverage toggle, e2e, docs and release note | [`2026-10-08-companion-linking-f3-ingestor-frontend-docs.md`](2026-10-08-companion-linking-f3-ingestor-frontend-docs.md) | 10 |

F1 → F2 → F3 strictly: F2 calls the F1 store API, and F3's ingestor reads the F1 table while its frontend calls the F2 routes. Each part is mergeable on its own: F1 alone exposes nothing (no route creates a device session), F2 alone is complete server-side, F3 only adds the consumers.

## Cross-part contracts

- **F1 → F2:** the "Exported API (F2 relies on these names)" table at the top of the F1 plan (`SessionKindDevice`, `DeviceSessionTTL`, `CreateDeviceSession`, `HasScope`, `CreateLinkChallenge`, `ConsumeLinkChallenge`, `UpsertCompanionLink`, `ListCompanionLinks`, `DeleteCompanionLink`, `NormalizePubkey`, the sentinel errors). Changing a name there means changing F2.
- **F1 → F3 (ingestor):** the `companion_links` table itself (`pubkey` 64 lowercase hex), read with raw SQL `SELECT pubkey FROM companion_links`. The ingestor never imports `internal/users`; `TestLinkedCompanionsTableIsInUsersSchema` (F3 Task 2) fails when the table leaves `internal/users/schema.go`.
- **F2 → F3 and CoreDrive RX:** the "API for F3" table at the top of the F2 plan: request/response shapes, status codes, the signed message `"corescope-link:" + <host of publicBaseUrl> + ":" + challenge`, `myNodes` ∈ `added`/`present`/`full`/`failed`, sessions `kind`/`label`, `?mine=1`, `clientRxRequireLinkedCompanion`, and the CORS headers. The RX side is specified in `efiten/coredrive-rx`, `docs/superpowers/specs/2026-10-08-corescope-login-design.md`.
- **Unchanged:** the RX MQTT payload contract (`docs/client-rx-coverage.md`) and the ingestor's write path. Attribution is a read-time join, so no stored row changes when a companion is linked, transferred or unlinked.

## Done when

- `make test` passes for every Go module (`internal/users`, `internal/sigvalidate`, `cmd/server`, `cmd/ingestor` and the rest), and `go test -race` passes for the F3 ingestor filter tests.
- `sh test-all.sh` passes, including `tests/unit/test-user-management-ui.js` and `tests/unit/test-rx-coverage-mine.js`.
- `tests/e2e/test-user-management-e2e.js` passes against the `-tags e2etest` server with user management on, including the companions step.
- `cd cmd/server && go test -run TestOpenAPI .` passes: every new route has an OpenAPI entry (F2 Task 7/12), and `/api/rx-coverage` stays in `openapi_known_gaps.json`.
- Every self-review list at the end of the three plans is ticked against the spec.

## Rollout

Opt-in, like A–E: nothing changes until `userManagement.enabled`. Deployments where RX runs on another origin add that origin to `corsAllowedOrigins`. An RX build without account support keeps working unchanged, **except** under `clientRxCoverage.requireLinkedCompanion`, where all of its data is dropped. That setting is a filter, not a security boundary (all RX clients share one broker account): turn it on only after the RX clients in use support linking, and after the server and ingestor both run F2/F3 (the ingestor reads the v6 table that F1's migration creates when the server starts).
