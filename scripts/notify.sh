#!/bin/sh
#
# Runs the outstanding Phase 4 transactional email pass (spec §7): schedule
# created/paused/resumed/canceled confirmations.
#
# This is its own scheduled task, separate from scripts/nightly.sh, because it needs
# a different credential set: POSTMARK_API_KEY, NOTIFICATION_FROM_ADDRESS, and
# NOTIFICATION_SUPPORT_CONTACT, which sweep and materialize have no use for. ADR
# 0011's reasoning for scoping nightly.sh to DATABASE_URL alone -- a scheduled task
# should not hold a credential it never presents -- is exactly why this is a second
# task instead of a third job appended to that script. See docs/SCHEDULED_JOBS.md.
#
# cmd/notify validates its own Postmark configuration (config.RequireNotifications)
# and exits non-zero if it is missing, so this script only guards DATABASE_URL, the
# one thing it would otherwise fail on well before reaching that check.
#
# POSIX sh, not bash: the runtime image is not defined in this repository, and a
# slim base may ship dash as /bin/sh.

set -u

if [ -z "${DATABASE_URL:-}" ]; then
	echo "notify: DATABASE_URL is required" >&2
	exit 1
fi

log() {
	echo "notify: $(date -u '+%Y-%m-%dT%H:%M:%SZ') $*"
}

# Prefers a compiled binary and falls back to `go run`, matching nightly.sh: the
# deployed image may carry either.
run_job() {
	job=$1
	if [ -x "./bin/$job" ]; then
		"./bin/$job"
	elif command -v "$job" >/dev/null 2>&1; then
		"$job"
	else
		go run "./cmd/$job"
	fi
}

log "notify starting"
if run_job notify; then
	log "notify ok"
	exit 0
else
	status=$?
	log "notify FAILED (exit $status)"
	exit "$status"
fi
