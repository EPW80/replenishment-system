# Deployment configuration

What has to exist outside this repository for `deploy.yml` to work. The gate sequence
itself is [`LIFECYCLE.md`](LIFECYCLE.md); this is the configuration those gates assume.

Deploys are triggered by a human via `workflow_dispatch`. Production additionally
pauses for the `production` Environment's required reviewer before any step runs.

## GitHub Environment configuration

Create `staging` and `production` GitHub Environments with the applicable values:

| Kind | Name | Value |
| --- | --- | --- |
| Secret | `COOLIFY_DEPLOY_WEBHOOK_URL` | Staging application's Coolify deploy webhook |
| Secret | `COOLIFY_API_TOKEN` | Coolify API token authorized for that application |
| Variable | `APP_URL` | Public URL the health check probes, e.g. `https://staging.example.com` |
| Variable | `COOLIFY_API_URL` | Production only: Coolify API base URL ending in `/api/v1` |
| Variable | `COOLIFY_APPLICATION_UUID` | Production only: application UUID to pin and deploy |

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
| `POSTMARK_API_KEY`, `NOTIFICATION_FROM_ADDRESS`, `NOTIFICATION_SUPPORT_CONTACT` | Required — the notification dispatch task runs in this application. See [`SCHEDULED_JOBS.md`](SCHEDULED_JOBS.md). |

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

Staging calls the Coolify deploy webhook and therefore builds the tracked branch tip.
Production uses the authenticated Coolify API in two steps: it first updates the
application's `git_commit_sha` to the approved 40-character SHA, then queues a forced
deployment of that application UUID. This prevents a branch update between approval
and deployment from changing the production target.

Both paths return as soon as deployment is *queued*. The health check polls until the
endpoint reports the expected SHA, which is the real completion signal. A SHA mismatch
is a failed deploy, not a transient probe failure.

Production rebuilds the exact approved source revision rather than promoting the image
staging verified. The commit and Dockerfile are fixed, but this remains a deviation
from "promote the artifact you tested"; a per-commit registry image is still the
stronger future shape.

## First deploy checklist

1. Create both GitHub Environments with the values above. Production requires
   `COOLIFY_API_URL` and `COOLIFY_APPLICATION_UUID`; staging requires the webhook URL.
2. Configure the Coolify application per environment, including `BUILD_SHA` and the
   `migrate` pre-deployment command.
3. Add the required reviewer on the `production` Environment — this is the
   business-approval gate and nothing else enforces it.
4. Create **both** scheduled tasks — the nightly pair and notification dispatch
   ([`SCHEDULED_JOBS.md`](SCHEDULED_JOBS.md)). They have different cadences and
   different environments; creating only the first leaves every confirmation email
   unsent, with nothing erroring.
5. Run `deploy.yml` against staging and confirm the health check passes on the SHA you
   deployed, not merely that it returned 200.
6. Write the first `releases/` record ([`RELEASE_METADATA.md`](RELEASE_METADATA.md)).
