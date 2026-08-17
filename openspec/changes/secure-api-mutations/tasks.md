## 1. Persistence

- [ ] 1.1 Inventory webhook and administration mutation routes and ownership rules.
- [ ] 1.2 Add reversible hashed-token migration and indexes.

## 2. Enforcement

- [ ] 2.1 Implement bearer parsing, constant-time hash verification, expiry, and revocation.
- [ ] 2.2 Require matching authenticated user and token owner for mutations.
- [ ] 2.3 Implement owner-only create/list/revoke/rotate token operations.

## 3. Verification

- [ ] 3.1 Test public TRMNL reads and all authorization failure modes.
- [ ] 3.2 Test ownership, rotation, revocation, expiry, and secret non-disclosure.
- [ ] 3.3 Update client documentation and run the full test suite.
