/*
 * Date arithmetic for the portal.
 *
 * Two rules from screens.md and spec §3 drive everything here.
 *
 * First, "today" is resolved in the schedule's timezone, never the browser's. A
 * customer in Los Angeles loading the portal from a hotel in Berlin must see the same
 * date the service will act on.
 *
 * Second, the API's dates are calendar dates (YYYY-MM-DD), not instants. Formatting one
 * through a timezone would shift it a day -- new Date('2026-09-18') is UTC midnight,
 * which is 17 September in Los Angeles. So a calendar date is formatted as itself, in
 * UTC, and the schedule's timezone is used only to decide what today is. The two
 * concerns look similar and reversing them is the bug this file exists to prevent.
 *
 * Nothing here ever computes a date by adding an interval to another date: the service
 * owns that arithmetic as anchor_date + (n × interval_days), and re-deriving it on the
 * client reintroduces exactly the drift that formula avoids.
 */

const MS_PER_DAY = 86400000;

/** Parses YYYY-MM-DD into a Date at UTC noon. Noon, not midnight, so that a
 *  formatter running in any timezone still reports the same calendar day. */
function parseCalendarDate(iso) {
  const [y, m, d] = iso.split('-').map(Number);
  return new Date(Date.UTC(y, m - 1, d, 12));
}

/*
 * The three date spellings are assembled from parts rather than handed to a locale.
 * Every one of them is a reviewed string on a compliance-governed surface, and a
 * locale's punctuation is not stable enough to carry that: en-GB renders "Friday 18
 * September 2026" with no comma, en-US reorders the day and month outright. Taking the
 * parts and joining them ourselves is what makes the output the artboards' output on
 * every browser and every ICU version.
 */
function parts(iso, options, locale = 'en-GB') {
  const formatted = new Intl.DateTimeFormat(locale, {
    timeZone: 'UTC',
    ...options,
  }).formatToParts(parseCalendarDate(iso));
  const out = {};
  for (const { type, value } of formatted) out[type] = value;
  return out;
}

/** "Friday, 18 September 2026" */
export function formatLong(iso) {
  const p = parts(iso, { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' });
  return `${p.weekday}, ${p.day} ${p.month} ${p.year}`;
}

/** "Fri, 18 September" -- the narrow layout's headline. */
export function formatShort(iso) {
  const p = parts(iso, { weekday: 'short', day: 'numeric', month: 'long' });
  return `${p.weekday}, ${p.day} ${p.month}`;
}

/** "29 May 2026" */
export function formatMedium(iso) {
  const p = parts(iso, { day: 'numeric', month: 'long', year: 'numeric' });
  return `${p.day} ${p.month} ${p.year}`;
}

/** "18 Sep 2026" -- the narrow layout's occurrence rows.
 *
 *  The abbreviated month comes from en-US, where every month is three letters. en-GB
 *  abbreviates September to "Sept", which is four characters wide in a column sized for
 *  three and is not what the artboard shows. Only the month name is taken from the
 *  locale; the order is assembled here either way. */
export function formatCompact(iso) {
  const p = parts(iso, { day: 'numeric', month: 'short', year: 'numeric' }, 'en-US');
  return `${p.day} ${p.month} ${p.year}`;
}

/** Today's calendar date in the given IANA timezone, as YYYY-MM-DD. */
export function todayIn(timeZone, now = new Date()) {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(now);
  const get = (type) => parts.find((p) => p.type === type).value;
  return `${get('year')}-${get('month')}-${get('day')}`;
}

/** Whole days from one calendar date to another. Negative when `to` is earlier. */
export function daysBetween(fromISO, toISO) {
  return Math.round((parseCalendarDate(toISO) - parseCalendarDate(fromISO)) / MS_PER_DAY);
}

/** A calendar date `days` after `iso`, as YYYY-MM-DD.
 *
 *  This is for previewing what the service will do, never for deriving a date the
 *  portal then treats as fact -- the response is what settles that. In particular a
 *  cadence preview counts every date from the anchor (`anchor + n × interval`) rather
 *  than stepping forward from the previous rendered date, which is the drift spec §3
 *  exists to avoid. */
export function addDays(iso, days) {
  const d = parseCalendarDate(iso);
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
}

/** Whole days from today, in the schedule's timezone, to a calendar date. */
export function daysFromToday(iso, timeZone, now = new Date()) {
  return daysBetween(todayIn(timeZone, now), iso);
}

/*
 * The two countdown strings. copy.md gives them as "{d} days from now" and "Charges in
 * N days", which read wrong at one day and at zero, so those two cases are spelled out
 * rather than rendered with a plural that does not agree. A date already in the past
 * gets no countdown at all: the alternative is inventing copy the handoff does not
 * have, and a stale "Charges in -2 days" is worse than silence.
 */

/** "12 days from now", "1 day from now", "today", or null when already past. */
export function countdownPhrase(iso, timeZone, now = new Date()) {
  return countdown(iso, timeZone, now, 'from now');
}

/** The narrow layout says "12 days away" where the wide one says "from now"
 *  (Mobile.dc.html). Same arithmetic, shorter tail. */
export function countdownPhraseShort(iso, timeZone, now = new Date()) {
  return countdown(iso, timeZone, now, 'away');
}

function countdown(iso, timeZone, now, tail) {
  const days = daysFromToday(iso, timeZone, now);
  if (days < 0) return null;
  if (days === 0) return 'today';
  if (days === 1) return `1 day ${tail}`;
  return `${days} days ${tail}`;
}

/** "Charges in 12 days", "Charges in 1 day", "Charges today", or null when past. */
export function chargesPhrase(iso, timeZone, now = new Date()) {
  const days = daysFromToday(iso, timeZone, now);
  if (days < 0) return null;
  if (days === 0) return 'Charges today';
  if (days === 1) return 'Charges in 1 day';
  return `Charges in ${days} days`;
}
