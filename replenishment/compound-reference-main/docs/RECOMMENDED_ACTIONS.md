# Recommended actions — documented, not installed

This template uses exactly one GitHub Action: `actions/checkout`, published by
GitHub, and it is **pinned to a full commit SHA**:

```yaml
uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # actions/checkout v7.0.1
```

That SHA was resolved from `refs/tags/v7.0.1` in `actions/checkout` and
cross-checked three ways: the tag ref, the commit object itself (GPG-verified), and
the fact that both the `v7.0.1` and `v7` tags point at it. If you bump the version,
re-resolve the SHA the same way rather than trusting a tag.

Everything below is a **recommendation to evaluate**, not a dependency. Nothing
here is wired into any workflow.

---

## Why the restraint

An action runs inside your job with access to that job's token, its secrets, and
its filesystem. Adding one is adding a supply-chain dependency that executes with
your credentials on every run.

The realistic failure is not a malicious author — it is a maintainer's account
being compromised, or a popular action changing hands. Both have happened. A
mutable tag like `@v4` resolves to whatever that tag currently points at, so a
compromised tag reaches your pipeline on the next run, with no change to your
repository.

This is not an argument for never using actions. It is an argument for each one
being a decision someone made on purpose.

## Rules if you adopt one

1. **Pin to a full commit SHA, never a tag.**
   ```yaml
   uses: owner/action@<full-40-char-commit-sha> # v1.2.3
   ```
   A tag can be repointed; a commit SHA cannot. Keep the version in a trailing
   comment so the pin is legible.
2. **Prefer a plain command.** Most of what setup and audit actions do is one or
   two shell commands. If the command is short and the runner already has the
   tool, skip the action.
3. **Prefer a repository setting.** CodeQL, secret scanning, push protection, and
   dependency alerts are all configurable in settings with no workflow code at
   all — no pinning to maintain, no token exposure.
4. **Grant the minimum.** Give the job only the scopes that specific action needs.
5. **Never expose secrets to an action running untrusted code.** Especially in any
   workflow triggered by `pull_request_target`.
6. **Record the decision.** Note in the PR why the action was necessary and what
   command it replaces.

---

## Candidates by need

### Toolchain setup

**Need:** install a language runtime at a pinned version.

GitHub publishes `actions/setup-node`, `actions/setup-python`, `actions/setup-go`,
`actions/setup-java`, and `shivammathur/setup-php` is the de facto standard for
PHP (third-party).

**Consider first:** GitHub-hosted runners preinstall several runtimes, often at
multiple versions. If the preinstalled version is acceptable, or the version
manager your project already uses is present, no action is needed.

**Worth it when:** you need a pinned version the image does not provide, or you
want the dependency caching these actions offer — that caching is a real,
material speedup and the main honest argument for them.

### Dependency caching

**Need:** avoid re-downloading dependencies on every run.

`actions/cache` (GitHub), or the caching built into the `setup-*` actions above,
which is simpler when applicable.

**Consider first:** whether the install is slow enough to matter. A cache adds a
correctness risk — a stale or poisoned cache entry is a genuinely confusing
failure — so it should buy real time.

### Build artifacts

**Need:** pass a build output between jobs, or deploy the exact artifact that was
tested.

`actions/upload-artifact` and `actions/download-artifact` (GitHub).

**Worth it when:** you want production to run the same bytes staging verified,
rather than rebuilding. That is a meaningful integrity improvement over
rebuilding per environment. Left unwired here only because the template does not
know what your build produces — see the note in `build.yml`.

### Static analysis / SAST

**Need:** find security defects in source.

CodeQL is available through `github/codeql-action`, **but for supported languages
it can be enabled entirely in repository settings** (*Settings → Code security →
Code scanning*) with no workflow file. Do that instead where possible.

Third-party SAST tools mostly also ship as CLIs that run as a plain command in
`security-check.yml`, which avoids the action entirely.

### Dependency vulnerability audit

**Need:** flag known-vulnerable dependencies.

**No action required.** Every major package manager ships an audit command. Call
it directly in `security-check.yml`. Enable Dependabot alerts in settings for
continuous coverage between runs.

### Secret scanning

**Need:** stop a committed credential.

**No action required.** Enable GitHub secret scanning with push protection in
settings — it blocks the push, which is better than a CI job that reports the
secret after it is already in history.

Standalone scanners (`gitleaks`, `trufflehog`) are useful for scanning full
history or for patterns GitHub does not cover; both run as CLIs.

### Deploy provider actions

**Need:** deploy to a specific platform.

Most hosting providers publish an action, and most also publish a CLI.

**Prefer the CLI** in a `run:` step: you pin it through your normal dependency
mechanism, you can see exactly what it does, and you avoid a second supply-chain
surface in the job that holds production credentials. Prefer OIDC over a
long-lived deploy token wherever the provider supports it.

### PR automation and labelling

**Need:** auto-label, auto-assign, enforce PR title conventions.

Many small actions exist for this.

**Recommendation: skip these.** They need write permissions on pull requests —
the largest scope increase in this table — to save a few seconds of manual work.
Poor trade for a pipeline with a security review gate.

---

## Summary

| Need | Recommendation |
| --- | --- |
| Runtime setup | Runner-preinstalled tool; a `setup-*` action if you need caching or a pinned version |
| Dependency cache | Only if install time genuinely hurts |
| Artifact passing | Worth it — deploy what you tested |
| SAST | CodeQL via repository settings, not a workflow |
| Dependency audit | Package manager's own command; no action |
| Secret scanning | Repository setting with push protection; no action |
| Deploy | Provider CLI + OIDC, not the provider's action |
| PR automation | Skip — costs write scope for convenience |
