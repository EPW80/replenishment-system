/*
 * Status vocabulary: API value -> portal label, pill variant, and which section of the
 * queue a row belongs in.
 *
 * The labels are copy.md's status table verbatim. Two of them are deliberately not the
 * API's word: schedule `failed` reads to a customer as something they broke, and spec
 * §7 treats a failed schedule as a recoverable asset, so the portal says "Needs payment
 * update"; and the portal spells `canceled` the British way while the enum stays
 * American, because the enum is what the database holds. Do not "fix" either.
 *
 * The section split is screens.md's occurrence table. ListOccurrences returns every
 * occurrence for the schedule and does not paginate, so splitting them is the portal's
 * job.
 */

const SCHEDULE_STATUS = {
  active:   { label: 'Active', variant: 'ok' },
  paused:   { label: 'Paused', variant: 'idle' },
  canceled: { label: 'Cancelled', variant: 'idle' },
  failed:   { label: 'Needs payment update', variant: 'crit' },
};

const OCCURRENCE_STATUS = {
  planned:  { label: 'Scheduled', variant: 'idle', section: 'upcoming' },
  pending:  { label: 'Charging soon', variant: 'warn', section: 'upcoming' },
  placed:   { label: 'Placed', variant: 'ok', section: 'sent', icon: 'check' },
  skipped:  { label: 'Skipped by you', variant: 'idle', section: 'sent', icon: 'cross' },
  failed:   { label: 'Could not be charged', variant: 'crit', section: 'sent' },
  // Debris of a pause or a cancellation (spec §6). Showing a customer orders that were
  // removed because they asked for them to be removed is noise.
  canceled: { label: 'Canceled', variant: 'idle', section: 'hidden' },
};

/** An unknown status renders its raw value rather than disappearing: a new enum member
 *  reaching an old portal should look unfamiliar, not invisible. */
function lookup(table, value) {
  return table[value] ?? { label: value, variant: 'idle', section: 'upcoming' };
}

export function scheduleStatus(value) {
  return lookup(SCHEDULE_STATUS, value);
}

export function occurrenceStatus(value) {
  return lookup(OCCURRENCE_STATUS, value);
}

/** Splits the full occurrence list into the two rendered sections, dropping the
 *  hidden ones. Upcoming runs earliest-first; already-sent runs most-recent-first.
 *
 *  Ordered by date, with the sequence number only as a tie-breaker -- the same order
 *  the service uses in NextActionableOccurrence (`ORDER BY scheduled_for, sequence_no`,
 *  internal/store/store.go). The two can disagree: deferring an occurrence moves its
 *  date without renumbering it, so sorting by sequence alone would show a later order
 *  above an earlier one, and the row the customer reads as "next" would not be the one
 *  the service acts on next. */
export function splitOccurrences(occurrences) {
  const upcoming = [];
  const sent = [];
  for (const occ of occurrences) {
    const { section } = occurrenceStatus(occ.status);
    if (section === 'upcoming') upcoming.push(occ);
    else if (section === 'sent') sent.push(occ);
  }
  const byDate = (a, b) =>
    a.scheduled_for.localeCompare(b.scheduled_for) || a.sequence_no - b.sequence_no;
  upcoming.sort(byDate);
  sent.sort((a, b) => byDate(b, a));
  return { upcoming, sent };
}
