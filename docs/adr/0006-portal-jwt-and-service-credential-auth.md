# 0006 — Portal JWT and a separate service credential

**Status:** Accepted

## Context

The service had no authentication or authorization. Every route was wired without
middleware, the actor written to the audit log was a constant asserting "customer"
whoever called, and the schedule and customer IDs came straight from the URL. Anyone
who could reach the service and guess a UUID could read or mutate any customer's
schedule.

Spec §4 describes a thin WP mu-plugin that "holds a nonce-to-JWT exchange and nothing
else" and proxies authenticated calls. That fixes the shape of the customer half:
CadenceOS is the verifier, not the issuer. It leaves two things open — which signature
algorithm, and how schedule creation authenticates, since creation happens server-side
at WooCommerce checkout where there is no browser session to mint a token from.

## Decision

**Two credentials, separated by route group.**

- Customer routes (reads and the six spec §6 transitions) require an HS256 JWT signed
  by the mu-plugin with a shared secret. The verifier checks `iss`, `aud`, `exp` and
  `nbf`, requires an expiry, and takes the customer ID from `sub`.
- `POST /schedules` requires a static service credential, compared in constant time.
- `/healthz` requires nothing.

Splitting by route rather than sniffing the credential is deliberate: no request is
ever ambiguous about which credential it presents, so a customer token cannot be
replayed against the creation endpoint merely because the server was willing to try it
both ways.

**HS256 rather than an asymmetric algorithm.** The mu-plugin signs; CadenceOS verifies
with the same secret, which means CadenceOS can also mint customer tokens. That is a
real cost, accepted because the blast radius barely changes: an attacker who can run
code in CadenceOS already reaches the database holding every schedule. Simpler key
distribution is worth more than a property that does not hold in practice.

**The accepted algorithm is pinned.** The verifier accepts HS256 and nothing else. A
parser that trusts the token's own header to say how it should be checked accepts
`alg: none` (no verification at all) and RS256-against-an-HMAC-key (the public key
becomes a signing key). The token does not get a vote on how it is verified.

**Ownership is enforced in the query layer.** A `store.Scope` parameter on
`GetSchedule`, `ListOccurrences` and `ListScheduleItems` adds a `customer_id`
predicate. A check written once per handler is a check that will eventually be omitted
from one handler, and that one omission is the whole vulnerability. Making the scope a
required argument forces each call site to name the scope it means, so the unrestricted
path cannot be reached by forgetting something.

**Cross-customer access answers 404, never 403.** A 403 confirms that the ID names a
real schedule, which is exactly what an attacker enumerating UUIDs is trying to learn.
"Not yours" and "no such thing" are the same answer, with the same body.

**The actor is what the service can verify.** A service credential records
`ActorSystem`, not `ActorCustomer`, even though a customer's checkout is what set it
off: CadenceOS knows a trusted backend called, not that a person clicked. Same
reasoning as `test_status` in the release record — record the fact, not the claim.

## Consequences

- Two new secrets are required, `PORTAL_JWT_SECRET` and `SERVICE_API_KEY`, both at
  least 32 characters. Config refuses to start without them, following the
  `DATABASE_URL` precedent: a service that boots without them serves every schedule to
  anyone who asks, and there is no safe degraded mode to fall back into.
- The service credential is static and shared, so rotating it is a coordinated change
  with the WP host. Acceptable while there is exactly one caller; a second one is the
  signal to move to per-caller credentials.
- **Mutations are not themselves customer-scoped in SQL.** They are gated by the scoped
  load in `internal/schedule`, which happens *outside* the transaction that follows —
  a check, not a lock. Two concurrent transitions can each pass it against state the
  other is changing. Closing that is the concurrency work, which moves the load inside
  the transaction with `SELECT ... FOR UPDATE`; until then this layer stops the wrong
  customer, not a race.
- The upgrade path if the trust assumption changes is EdDSA: the mu-plugin holds a
  private key, CadenceOS only a public one, and a CadenceOS compromise can no longer
  mint tokens. It is an adapter change inside `internal/auth` — the verifier's callers
  see a `Principal` either way.
- Phase 6's admin surface gets its own route group and its own credential rather than
  reusing the customer one. It is deliberately absent here: a route group with no
  routes is clearer than an admin path nobody has designed yet.
