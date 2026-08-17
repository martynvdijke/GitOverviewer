## Why

GitLens exposes a public summary used by TRMNL, but future webhook and administration writes need a consistent authentication boundary.

## What Changes

- Keep `GET /api/trmnl/summary` and other public GET reads unauthenticated.
- Require an authenticated user and bearer API token for every create, update, and delete API operation.
- Add one-time-visible, hashed, revocable user tokens.

## Capabilities

### New Capabilities

- `api-authentication`

### Modified Capabilities

- `public-api-reads`

## Impact

Route middleware, token persistence and management, migrations, documentation, and tests change. Existing public TRMNL polling and browser authentication remain compatible.
