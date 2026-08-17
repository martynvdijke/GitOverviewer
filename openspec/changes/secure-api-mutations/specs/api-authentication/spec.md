## ADDED Requirements

### Requirement: Public reads remain public

The service SHALL allow unauthenticated GET requests to `GET /api/trmnl/summary` and other explicitly public read endpoints.

#### Scenario: TRMNL summary read

- WHEN TRMNL requests `/api/trmnl/summary` without credentials
- THEN the service returns the summary payload

### Requirement: Mutations require two factors

Every create, update, and delete API route MUST require an authenticated application user and a valid bearer API token owned by that user.

#### Scenario: Token-only write

- WHEN a request has a valid token but no authenticated user session
- THEN the service rejects it without writing data

### Requirement: Tokens are owner-scoped and one-time visible

Authenticated users SHALL be able to create, list metadata for, revoke, and rotate their own tokens. Secrets MUST be stored hashed, shown only at creation, and never logged or returned later.

#### Scenario: Cross-user token use

- WHEN a user presents another user's valid token
- THEN the mutation is rejected and no data changes

### Requirement: Invalid credentials fail safely

Malformed, expired, and revoked tokens MUST be rejected without revealing token existence.

#### Scenario: Revoked token

- WHEN a revoked token is submitted to a mutation
- THEN the service returns an authorization error and performs no write
