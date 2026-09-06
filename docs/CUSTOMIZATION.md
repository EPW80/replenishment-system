# Customization guide

What to change when starting a project from this template.

**How to read the per-stack sections.** They list the *decisions* each stack
forces and the questions you must answer. Where a command is named, it is a
common candidate from that ecosystem — verify it against your actual project
before pasting it into a workflow. This template does not know your linter, your
test runner, your host, or your deploy mechanism, and nothing below should be
treated as configuration you can adopt unread.

---

## Part 1 — Applies to every stack

Do these regardless of what you are building.

### 1. Repository settings

Work the configuration checklist in [`LIFECYCLE.md`](LIFECYCLE.md#configuration-checklist).
Most of the lifecycle's enforcement lives in settings, not in files. Until it is
done, the gates are advisory.

### 2. Code owners

**Deferred — leave `CODEOWNERS.example` where it is for now.** Do not rename it to
`.github/CODEOWNERS` yet.

A `CODEOWNERS` file naming teams that do not exist, or whose members have not yet
accepted their invitations, is worse than no file: GitHub **silently ignores** rules
referencing a team without write access. The peer-approval gate would appear
configured and enforce nothing.

Do this once organization members have accepted invitations and repository access
is assigned:

1. Copy `CODEOWNERS.example` → `.github/CODEOWNERS`.
2. Replace every `@ORG/REPLACE-ME-*` placeholder with a real user or team.
3. Confirm each team exists **and** has write access to the repository.
4. Enable "Require review from Code Owners" in branch protection.
5. Verify with a throwaway PR that it actually requests review from the expected
   owner.

Until then, treat peer approval as a convention enforced by people, not by GitHub.

### 3. Documents

| File | What to change |
| --- | --- |
| `README.md` | Replace with a description of the actual project |
| `CLAUDE.md` | Every `CUSTOMIZE` block: stack, commands, layout |
| `SECURITY.md` | Reporting contact, response targets, supported versions, scope |
| `CONTRIBUTING.md` | The "Local setup" section |
| `.github/ISSUE_TEMPLATE/config.yml` | `ORG/REPO` in the contact links |

### 4. Branch name

`pr-checks.yml` hardcodes `main` in two places. Change both if your default
branch has a different name.

### 5. The four check workflows

Replace the `CUSTOMIZE` step in each of `lint.yml`, `test.yml`, `build.yml`, and
`security-check.yml`. Each currently exits 1 with a message naming itself.

If your stack has no build step, delete `build.yml` and its job in
`pr-checks.yml` rather than leaving a workflow that always fails.

**Toolchain setup.** Most stacks need a runtime installed before these commands
run. GitHub-hosted runners preinstall many; prefer what is already in the image,
or the version manager your stack already uses. Adding a `setup-*` action is a
supply-chain decision — see [`RECOMMENDED_ACTIONS.md`](RECOMMENDED_ACTIONS.md).

### 6. Deploy workflows

Replace the `CUSTOMIZE` step in `staging-deploy.yml` and `production-deploy.yml`.
Both also set a `url` output so the health checks have a target:

```sh
echo "url=https://staging.example.com" >> "$GITHUB_OUTPUT"
```

Do not remove the SHA validation or the approval-match check. Those are the parts
that make an approval mean something.

### 7. Health checks

These work as shipped. You need only set `path:` in `deploy.yml` to a real health
endpoint. Adjust `attempts` and `delay-seconds` to your deploy's warm-up time.

### 8. Secrets

Create the deploy credential as a secret on **each Environment** — same secret
name on `staging` and `production`, different values — and read it in the deploy
step as `${{ secrets.YOUR_SECRET_NAME }}`. The environment binding is what keeps a
staging run from reading production credentials.

Read it inside the deploy workflow, not in `deploy.yml`. A job that calls a
reusable workflow cannot declare `environment:`, so it cannot see
environment-scoped secrets, and forwarding one from there passes an empty string
with no error. The deploy workflows carry a comment explaining this at the point
it matters.

**This design is not yet verified.** It is sound in principle, but it has not been
exercised in a real GitHub repository, and the failure mode is silent: an unset or
unreachable secret resolves to an empty string rather than erroring, so a misplaced
secret looks like working config until the deploy command fails for an
unrelated-looking reason.

Verify it with a **dummy, non-production value** before wiring a real credential:
add a throwaway secret to the `staging` Environment, have the deploy job echo its
*length* (never its value), and confirm the length is non-zero. Only then replace
it with the real credential.

Prefer OIDC over a stored token where the provider supports it; the commented
`id-token: write` lines mark where.

### 9. Stack-specific files this template omits

A minimal root `.gitignore` already excludes `.claude/settings.local.json`. Add
as your first commit: your stack's own `.gitignore` entries, dependency manifest
and lockfile, linter and formatter config, editor config, runtime version pin.

### 10. AI review

Fill in the `CUSTOMIZE` block in `.claude/agents/security-reviewer.md` with your
trust boundaries and sensitive areas. A generic security review finds generic
issues; the useful findings come from knowing what this system protects.

---

## Part 2 — Per stack

### React / Next.js

**Decisions:** Which is it? A React SPA and a Next.js app differ in the parts
that matter here — build output, whether a server runtime is needed, and
therefore how it deploys and rolls back.

- Package manager (npm / yarn / pnpm / bun) — determines every command, and the
  lockfile you must commit. Use the CI-oriented install (`npm ci` and its
  equivalents), not the interactive one.
- Node version, pinned somewhere the workflow and local dev both read.
- **Static export vs. server rendering.** A static build is a directory of files
  — deploys are a file sync and rollback is trivial. Anything with SSR, route
  handlers, middleware, or ISR needs a Node runtime in production, and rollback
  depends on the platform.
- Build-time vs. runtime environment variables. Values inlined at build time are
  baked into the bundle and are **public** — never a secret. This also means a
  build is environment-specific: a staging build cannot be promoted to
  production if it inlined staging values.
- Test layers: unit, component, end-to-end. E2E against staging is a separate
  concern from `test.yml` and usually a separate workflow.

**`build.yml`** is required. Confirm what directory the build emits and whether
the deploy consumes that directory or a server bundle.

**`security-check.yml`:** the package manager's own audit command needs no
third-party action.

**Health check:** `path` should be a lightweight route, not the app shell. If the
app is behind a CDN, ensure the probe is not served a cached response — that would
report healthy from cache while the origin is down.

---

### Node (service / API / CLI)

**Decisions:**

- Package manager and Node version, as above.
- Does it build? TypeScript compiles; plain JavaScript may not. If it does not,
  delete `build.yml`.
- **Long-running process vs. serverless.** This is the decision that shapes the
  deploy and rollback workflows more than anything else. A persistent process
  needs a restart or replace strategy and graceful shutdown; a serverless
  function is versioned by the platform, which often makes rollback a matter of
  repointing an alias.
- Database and migrations. If there are migrations, decide where they run —
  inside the deploy, or as a separate gated step — and be strict about setting
  `database_migration` and `rollback_safe`.
- Test services: does the suite need a real database or cache? Use the job's
  `services:` block.

**Health check:** a service should expose a real readiness endpoint that checks
its dependencies (database reachable, migrations applied) and reports the
deployed commit SHA. Verifying that SHA in the health check catches a deploy that
silently kept serving the previous version — the failure a plain `200` cannot see.

---

### Python

**Decisions:**

- Python version, and how it is installed on the runner.
- **Dependency and environment tooling.** This is the least standardized part of
  the ecosystem — pip with requirements files, Poetry, uv, PDM, Conda, all in
  active use with different lockfiles and different install commands. Pick one
  and pin it; the commands in every workflow follow from this choice.
- Lint and format: linter, formatter, and whether type checking is a separate
  gate. Decide whether type errors fail `lint.yml` or `test.yml`.
- What kind of Python is it? A web service, a CLI, a library, and a scheduled job
  deploy in entirely different ways. A library may have no deploy at all — in
  which case delete the deploy workflows and use a publish workflow instead.
- If it is a web application: which server runs it in production, and how the
  process is managed and reloaded.
- Migrations, if the framework has them. Same `rollback_safe` discipline.

**`build.yml`:** often unnecessary for a service — delete it. Needed if you build
a distributable package or a container image.

**`security-check.yml`:** a dependency vulnerability scanner plus a static
analysis pass is the usual pairing; both exist as installable tools that run as
plain commands.

---

### PHP

**Decisions:**

- PHP version, and required extensions — a mismatch between the runner and
  production is a common and confusing failure.
- Composer: commit the lockfile, and use the production install flags
  (no dev dependencies, optimized autoloader) for the artifact you actually
  deploy — while keeping dev dependencies in the test job, which needs them.
- Framework or not. A framework brings its own CLI for migrations, caching, and
  maintenance mode, and its own opinion about what a deploy consists of. Plain
  PHP means you define the deploy yourself.
- **Deploy model.** Options range from file upload, to a pull-and-switch on the
  server, to an atomic symlink swap between release directories, to a container
  image. This is the single largest fork in the workflows: symlink-style releases
  make rollback nearly free, while in-place file sync makes it difficult.
- Does the build step do anything? If assets are compiled, that is usually a
  Node toolchain alongside PHP — two runtimes in one workflow.
- Writable paths, caches, and generated config that must not be overwritten or
  must be cleared on deploy.

**`security-check.yml`:** Composer's own audit command, plus a static analyzer if
one is adopted.

---

### WordPress

**Decisions.** Establish first **what this repository actually contains** —
nothing else can be decided until it is settled, and it is the most common source
of confusion:

- a single theme, or
- a single plugin, or
- the full site (`wp-content`, or the whole document root), or
- a composer-managed WordPress install.

Then:

- WordPress and PHP version targets.
- **What is under version control and what is not.** WordPress core, uploaded
  media, and the database are usually excluded. Be explicit — an accidentally
  committed `wp-config.php` leaks database credentials and salts, and committed
  uploads bloat the repository permanently.
- Coding standards, if the project follows WordPress's published ones.
- **The database is the hard problem.** WordPress keeps configuration, content,
  and serialized option data in the database. Staging and production diverge
  continuously because content changes in production. Decide deliberately: what
  is deployed as code, what is configured per environment, and what is never
  synced. Plugin updates and settings changes made through the admin UI are
  changes that bypass this entire lifecycle — decide whether admin write access
  is restricted in production, or accept that production drifts from the
  repository.
- Migration and rollback: URL rewriting between environments and serialized data
  make database rollback genuinely risky. For most WordPress projects, code is
  rollback-safe and database changes are not. Set `rollback_safe` accordingly and
  do not let it default to `true`.

**`test.yml`:** many WordPress projects have no automated tests. If so, delete
`test.yml` and its job rather than leaving a permanently failing gate — and note
in the README that the test gate is intentionally absent, so nobody assumes
coverage that does not exist.

**Health check:** probe a real page and confirm it renders, not just that PHP
responds. A WordPress site with a fatal error in a plugin can still return `200`
with a broken page, so consider asserting on body content.

---

### Static site

The simplest case: no server runtime, so deploys are a file sync and rollback is
usually just redeploying the previous output.

**Decisions:**

- Generator, or hand-written HTML. If there is no generator, delete `build.yml`.
- If there is a generator: its toolchain and version, and the output directory.
- Where it is hosted. Static hosts differ in whether they keep previous
  deployments — if yours does, rollback is promoting the previous deployment and a
  rollback workflow becomes a single API call. Check before writing anything more
  complicated.
- Cache invalidation. Often the only genuinely tricky part: a deploy that
  succeeds while the CDN keeps serving old files looks healthy and is not.
  Invalidation belongs in the deploy step, before the health check runs.
- Link checking and HTML validation are good `lint.yml` candidates for a static
  site.

**`test.yml`:** frequently nothing to test. Delete it rather than leaving it red.

**`database_migration`** is always `false`; **`rollback_safe`** is normally `true`.

---

## Part 3 — Deleting what you do not need

The template is deliberately larger than most projects need. Removing a workflow
is a legitimate customization, and better than leaving one that always fails.

| If... | Then |
| --- | --- |
| No build step | Delete `build.yml` + its job in `pr-checks.yml` |
| No automated tests | Delete `test.yml` + its job; note the gap in the README |
| No staging environment | Delete `staging-*.yml`; document that releases go straight to production |
| Platform cannot roll back | Delete the rollback workflow; state it in the README *(done here — see [`LIFECYCLE.md` §12](LIFECYCLE.md#12-rollback))* |
| No business approval needed | Keep the Environment gate, set `business_approval_required: false` |

Deleting a gate is a decision worth recording. Say why in the README, so the next
person can tell an intentional omission from an oversight.
