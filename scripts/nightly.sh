#!/bin/sh
#
# Runs the nightly passes that move schedules forward with no customer action:
# sweep (end timed pauses that have come due, spec §6 resume) and materialize
# (top up the planned-occurrence horizon, spec §5 step 1).
#
# Invoked by the Coolify scheduled task in staging and production, and by
# `make nightly` locally. See docs/SCHEDULED_JOBS.md for the deployment config
# and for why this is a scheduled task rather than a workflow or a goroutine.
#
# POSIX sh, not bash: the runtime image is not defined in this repository, and a
# slim base may ship dash as /bin/sh.

set -u

# DATABASE_URL is the only variable these jobs need. Neither authenticates a
# caller, so neither takes PORTAL_JWT_SECRET or SERVICE_API_KEY -- do not hand a
# scheduled job credentials it never presents (see internal/config.RequireAuth).
if [ -z "${DATABASE_URL:-}" ]; then
	echo "nightly: DATABASE_URL is required" >&2
	exit 1
fi

log() {
	echo "nightly: $(date -u '+%Y-%m-%dT%H:%M:%SZ') $*"
}

# run_job runs one job by name, preferring a compiled binary and falling back to
# `go run`. The deployed image may carry either, so this works in both without
# the schedule needing to know which.
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

# Both jobs run even if the first fails, and the script exits non-zero if either
# did. They are independent: sweep's resume re-materializes inside its own
# transaction (internal/schedule.Service.Resume), so a failed sweep cannot leave
# a resumed schedule without occurrences, and skipping materialize because sweep
# failed would turn a recoverable error into a draining horizon.
status=0

log "sweep starting"
if run_job sweep; then
	log "sweep ok"
else
	log "sweep FAILED (exit $?)"
	status=1
fi

# Second, so its schedules_considered count includes anything the sweep resumed
# tonight and the log reads as a true snapshot of the active set.
log "materialize starting"
if run_job materialize; then
	log "materialize ok"
else
	log "materialize FAILED (exit $?)"
	status=1
fi

if [ "$status" -ne 0 ]; then
	log "one or more jobs failed"
fi
exit "$status"
