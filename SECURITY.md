# Security Policy

## Reporting a vulnerability

Do **not** open a public issue for a security vulnerability.

Use GitHub's private vulnerability reporting: **Security → Advisories → Report a
vulnerability** on this repository. Reports stay private until a fix is available.

Include, as far as you can:

- what the issue is and the impact you believe it has,
- the affected version or commit SHA,
- steps to reproduce,
- any logs or proof of concept.

### What to expect

| Stage | Target |
| --- | --- |
| Acknowledgement | within 3 business days |
| Initial assessment | within 10 business days |
| Fix or mitigation plan | communicated after assessment |

We will keep you informed while the issue is open and credit you in the advisory
unless you ask us not to.

## Supported versions

CadenceOS is a continuously deployed service, not a versioned distribution. There are
no release branches and no backports.

| Version | Supported |
| --- | --- |
| The currently deployed production release | Yes |
| Any earlier commit | No — report against the current deployment |

## Scope

**In scope:**

- This repository and the CadenceOS service it deploys.
- The CadenceOS staging and production deployments.
- The customer-facing replenishment portal widget and the WordPress mu-plugin that
  proxies authenticated calls to it.

**Out of scope:**

- WordPress core, WooCommerce, and third-party plugins — report those upstream.
- Postmark, the payment gateway, and other third-party services — report those to the
  vendor.
- Findings that require a compromised customer account or physical device access.

Do not test against production. Do not run denial-of-service tests, and do not access
data belonging to anyone other than yourself.

### Areas worth a reporter's attention

These are the trust boundaries this service actually has:

- **Payment token handling.** `payment_token_ref` is an opaque gateway vault
  reference. Card data never enters this system; a report showing otherwise is a
  serious finding.
- **Order idempotency.** `occurrences.idempotency_key` is what prevents a retry, a
  duplicate queue delivery, or a mid-run redeploy from producing a second charge. A
  path to a duplicate charge is a serious finding.
- **The nonce-to-JWT exchange** in the WordPress mu-plugin — the only authentication
  boundary between the storefront and this service.
- **Cross-customer access** to schedules, occurrences, or order references.

---

## Practices this repository enforces

Summarized here for reporters. The rules and the reasoning behind them live in
one place each, so they cannot drift out of sync:

- **Pipeline hardening** — least-privilege workflow permissions, no third-party
  GitHub Actions, `persist-credentials: false` on checkout, no `secrets: inherit`,
  and environment-scoped deploy credentials.
  → [`docs/WORKFLOWS.md`](docs/WORKFLOWS.md)
- **Repository settings** — branch protection, required status checks, required
  code-owner review, and the `staging` / `production` Environments that carry the
  approval gate.
  → [`docs/LIFECYCLE.md`](docs/LIFECYCLE.md#configuration-checklist)
- **Review gates** every change passes before production.
  → [`docs/LIFECYCLE.md`](docs/LIFECYCLE.md)
- **Third-party action recommendations**, documented rather than installed.
  → [`docs/RECOMMENDED_ACTIONS.md`](docs/RECOMMENDED_ACTIONS.md)

Two rules worth stating outright, because a reporter may be checking for them:
no secret is ever committed to this repository, and widening a workflow's token
scope is treated as a security change requiring its own reviewed pull request.
