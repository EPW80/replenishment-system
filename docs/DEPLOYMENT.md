# Deployment configuration

What has to exist outside this repository for `deploy.yml` to work. The gate sequence
itself is [`LIFECYCLE.md`](LIFECYCLE.md); this is the configuration those gates assume.

Deploys are triggered by a human via `workflow_dispatch`. Production additionally
pauses for the `production` Environment's required reviewer before any step runs.

## GitHub Environment configuration

Two GitHub Environments, `staging` and `production`, each with:

| Kind | Name | Value |
| --- | --- | --- |
| Secret | `COOLIFY_DEPLOY_WEBHOOK_URL` | That application's Coolify deploy webhook |
| Secret | `COOLIFY_API_TOKEN` | Coolify API token authorized for that application |
| Variable | `APP_URL` | Public URL the health check probes, e.g. `https://staging.example.com` |

The deploy job binds its Environment, which is what lets it read these. A reusable
workflow cannot be handed them by its caller — `deploy.yml` cannot forward Environment
secrets, and an unset secret resolves to an empty string with no error, so each deploy
step checks all three are non-empty and fails loudly rather than deploying nothing.

Scope the API token to the one application if Coolify allows it. The production token
is the credential that can redeploy production.

## Coolify application configuration

Per environment, on the CadenceOS application:

**Build:** from the repository's `Dockerfile`.

**Environment variables:**

| Variable | Notes |
| --- | --- |
| `DATABASE_URL` | Postgres connection string. Never leaves Coolify's network. |
| `PORT` | `8080`, matching the Dockerfile's `EXPOSE`. |
| `BUILD_SHA` | **Must equal the deployed commit.** See below. |
| `PORTAL_JWT_SECRET` | ≥32 chars. Generate with `openssl rand -base64 48`. |
| `SERVICE_API_KEY` | ≥32 chars, generated separately — not a copy of the JWT secret. |
| `PORTAL_JWT_ISSUER` / `PORTAL_JWT_AUDIENCE` | Must match what the WP mu-plugin mints. |
| `MATERIALIZE_HORIZON` | Optional; defaults to 3. |
| `POSTMARK_API_KEY`, `NOTIFICATION_FROM_ADDRESS`, `NOTIFICATION_SUPPORT_CONTACT` | Only if `cmd/notify` runs here. |

### BUILD_SHA is a gate, not a label

`staging-health-check` and `production-health-check` assert that `/healthz` reports the
**commit being deployed**. That assertion is the only thing standing between "the
webhook returned 200" and "the new code is actually serving" — a deploy that silently
kept the previous version returns 200 from every other check.

`config.Load` reads `BUILD_SHA` from the environment at runtime, so it has to be set to
the deployed commit on each deploy. If Coolify exposes the checked-out commit as a
build/runtime variable, wire `BUILD_SHA` to it; the variable's name differs between
Coolify versions, so confirm it against the running instance rather than assuming.
**If it cannot be wired automatically, the health gate will fail on every deploy** —
that is the gate working, not a bug to route around. Do not set `BUILD_SHA` to a
constant to make it pass; that disables the only check that the right code shipped.

### Migrations run here, not in CI

Set `migrate` as the application's **pre-deployment command**, so it runs inside the
deployment's own network before the new container serves.

Migrations are deliberately *not* applied from the GitHub runner. Doing so would mean
the production database accepts connections from GitHub's published IP ranges and a
long-lived `DATABASE_URL` lives as a GitHub secret — the same exposure
[ADR 0011](adr/0011-coolify-scheduled-tasks-for-nightly-jobs.md) rejected for the
nightly jobs. Keeping it a pre-deployment command also keeps migrations an explicit,
ordered step rather than a side effect of a process starting, which is why
`cmd/migrate` is a separate binary at all.

### Scheduled task

The nightly jobs run in this same image. See
[`SCHEDULED_JOBS.md`](SCHEDULED_JOBS.md).

## What the deploy step actually does

It calls the Coolify deploy webhook and returns as soon as the deployment is *queued*.
It does not wait for the rollout — the health check polls until the endpoint reports
the expected SHA, which is the real completion signal.

**The webhook does not take a commit SHA.** Coolify builds whatever the branch it
tracks currently points at. So the workflow's `commit-sha` input does not control what
gets built; it controls what the health check demands to see afterwards. Two
consequences:

- Point Coolify at the branch being deployed, and deploy from that branch's tip.
- If the tip moved between approval and deploy, the health check fails because the
  served commit will not match. That is the intended behaviour and the reason
  `production-deploy` verifies `commit-sha` against the approved SHA first.

Production rebuilds from source rather than promoting the image staging verified. The
inputs are identical — same commit, same `Dockerfile`, pinned base images — but this is
a real deviation from "promote the artifact you tested", accepted because Coolify's
webhook builds from git. Moving to a registry image is the better shape if that becomes
available.

## First deploy checklist

1. Create both GitHub Environments with the three values above.
2. Configure the Coolify application per environment, including `BUILD_SHA` and the
   `migrate` pre-deployment command.
3. Add the required reviewer on the `production` Environment — this is the
   business-approval gate and nothing else enforces it.
4. Create the nightly scheduled task ([`SCHEDULED_JOBS.md`](SCHEDULED_JOBS.md)).
5. Run `deploy.yml` against staging and confirm the health check passes on the SHA you
   deployed, not merely that it returned 200.
6. Write the first `releases/` record ([`RELEASE_METADATA.md`](RELEASE_METADATA.md)).
