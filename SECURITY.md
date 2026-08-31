# Security Policy

> **CUSTOMIZE:** replace the reporting contact and the supported-versions table
> before this repository is used.

## Reporting a vulnerability

Do **not** open a public issue for a security vulnerability.

<!-- CUSTOMIZE — pick one and delete the other -->
- Use GitHub's private vulnerability reporting: **Security → Advisories → Report
  a vulnerability** on this repository. (Requires enabling private reporting in
  repository settings.)
- Or email: _security-contact@example.com_

Include, as far as you can:

- what the issue is and the impact you believe it has,
- the affected version or commit SHA,
- steps to reproduce,
- any logs or proof of concept.

### What to expect

<!-- CUSTOMIZE — commit to timelines you can actually meet -->
| Stage | Target |
| --- | --- |
| Acknowledgement | _within N business days_ |
| Initial assessment | _within N business days_ |
| Fix or mitigation plan | _communicated after assessment_ |

We will keep you informed while the issue is open and credit you in the advisory
unless you ask us not to.

## Supported versions

<!-- CUSTOMIZE -->
| Version | Supported |
| --- | --- |
| _fill in_ | _yes / no_ |

## Scope

<!-- CUSTOMIZE -->
_Name the deployed hosts, domains, and repositories in scope, and anything
explicitly out of scope (third-party services, staging data, etc.)._

Do not test against production. Do not run denial-of-service tests, and do not
access data belonging to anyone other than yourself.

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
