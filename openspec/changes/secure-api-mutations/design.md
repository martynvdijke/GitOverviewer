## Context

GitLens has a public TRMNL summary endpoint and application routes that may receive webhook or administrative writes. The new machine credential must complement, not replace, user authentication.

## Goals / Non-Goals

### Goals

- Preserve public GET polling.
- Add hashed, revocable tokens scoped to users.
- Compose bearer-token checks with existing session and ownership checks.

### Non-Goals

- Protecting the public summary read.
- Committing or generating production secrets.
- Replacing browser sessions or CSRF protection.

## Decisions

- Add a token persistence model with hash, owner, timestamps, expiry, and revocation state.
- Add middleware that validates `Authorization: Bearer` and requires the session user to match the token owner.
- Apply it to every mutation route discovered during implementation; keep read-only webhook/status endpoints public only where intentionally documented.
- Provide owner-only create/list/revoke/rotate operations and one-time secret display.

## Risks / Trade-offs

Existing automation must add a user session and token. Hash-only storage limits recovery, so rotation and clear migration errors are required.

## Migration Plan

1. Add the reversible token migration and lifecycle API.
2. Add middleware and protect mutation routes.
3. Add authorization and public-read tests.
4. Issue tokens to users and migrate clients; revoke exposed credentials.

## Open Questions

- What default token expiry should GitLens use?
- Which webhook clients can establish the required user session?
