/*
 * Screen 3 — two sheets with different semantics, deliberately not merged into one
 * control (screens.md): changing the interval rewrites every future order, while
 * pushing back moves exactly one and leaves the rhythm alone.
 */

import { chip, el, inset, stepper } from './components.js';
import { addDays, formatCompact, formatLong, formatMedium, todayIn } from './dates.js';
import { chipRow, openSheet } from './sheets.js';
import { splitOccurrences } from './status.js';
import { changeCadence, defer, newIdempotencyKey } from './transitions.js';

const INTERVAL_PRESETS = [14, 21, 28, 35, 42, 56];
const DEFER_PRESETS = [3, 7, 14];

export const MIN_INTERVAL = 7;
export const MAX_INTERVAL = 180;
export const MAX_DEFER = 180;

/*
 * A cadence change re-anchors to the last placed order and recomputes every unexecuted
 * occurrence from there, so the preview has to do the same arithmetic the service does
 * (internal/schedule/service.go): each date is `anchor + (n × interval)`, counted from
 * the anchor every time, never stepped forward from the previously rendered date.
 *
 * When nothing has been placed yet the service anchors to today, in the schedule's
 * timezone -- so the preview does too, rather than guessing from the occurrence list.
 */
export function previewAnchor(occurrences, timezone, now = new Date()) {
  const placed = occurrences.filter((o) => o.status === 'placed');
  if (placed.length === 0) return { anchor: todayIn(timezone, now), fromPlacedOrder: false };
  const latest = placed.reduce((a, b) => (a.scheduled_for > b.scheduled_for ? a : b));
  return { anchor: latest.scheduled_for, fromPlacedOrder: true };
}

/** The next `count` dates a given interval would produce from an anchor. */
export function previewDates(anchor, intervalDays, count = 3) {
  return Array.from({ length: count }, (_, i) => addDays(anchor, intervalDays * (i + 1)));
}

export function openCadenceSheet({ schedule, occurrences, onDone }) {
  const { anchor, fromPlacedOrder } = previewAnchor(occurrences, schedule.timezone);
  let interval = schedule.interval_days;

  const preview = el('div', { className: 'cad-preview' });
  const chips = new Map();
  let control;

  function paint() {
    for (const [days, node] of chips) {
      node.setAttribute('aria-pressed', String(days === interval));
    }
    preview.replaceChildren(
      el('div', { className: 'cad-field__label', textContent: 'What this changes' }),
      el('div', { className: 'cad-preview__grid' }, [
        previewColumn(`Now — every ${schedule.interval_days} days`, previewDates(anchor, schedule.interval_days), false),
        previewColumn(`After — every ${interval} days`, previewDates(anchor, interval), true),
      ]),
      el('div', {
        className: 'cad-preview__note',
        textContent: fromPlacedOrder
          ? `Dates count forward from your last order placed on ${formatMedium(anchor)}, so the spacing stays exact instead of drifting a little each time.`
          : 'Dates count forward from today, so the spacing stays exact instead of drifting a little each time.',
      }),
    );
  }

  const setInterval = (days) => {
    interval = days;
    control.set(days);
    paint();
  };

  for (const days of INTERVAL_PRESETS) {
    chips.set(days, chip({ label: `${days} days`, onClick: () => setInterval(days) }));
  }

  control = stepper({
    value: interval,
    min: MIN_INTERVAL,
    max: MAX_INTERVAL,
    unit: 'days',
    label: 'Interval in days',
    onChange: (days) => {
      interval = days;
      paint();
    },
  });

  paint();

  return openSheet({
    title: 'How often should this repeat?',
    text: 'Pick the gap between orders. Anything from 7 to 180 days. This changes every future order, not just the next one.',
    onDone,
    render: () => ({
      content: el('div', { className: 'cad-stack' }, [
        chipRow([...chips.values()]),
        el('div', { className: 'cad-custom' }, [
          el('span', { className: 'cad-custom__label', textContent: 'or' }),
          control.node,
        ]),
        inset([preview]),
      ]),
      submitLabel: 'Save interval',
      onSubmit: () => changeCadence(schedule.id, interval),
    }),
  });
}

function previewColumn(label, dates, after) {
  return el('div', { className: `cad-preview__col${after ? ' cad-preview__col--after' : ''}` }, [
    el('div', { className: 'cad-preview__label', textContent: label }),
    el('div', {
      className: 'cad-preview__dates',
      textContent: dates.map((d) => formatCompact(d).replace(/ \d{4}$/, '')).join(' · '),
    }),
  ]);
}

/*
 * Pushing back moves one occurrence and does not touch the anchor, so the preview shows
 * one date moving and says plainly that the order after it does not. A preview that slid
 * the following dates would be showing the drift spec §6 exists to prevent.
 */
export function openDeferSheet({ schedule, occurrences, onDone }) {
  const { upcoming } = splitOccurrences(occurrences);
  const target = upcoming[0];
  if (!target) return null;
  const following = upcoming[1];

  // Minted once, when the sheet opens, and reused by every retry of this one intent.
  const idempotencyKey = newIdempotencyKey();

  let days = 7;
  const chips = new Map();
  const newDate = el('span', { className: 'cad-inline__value' });

  function paint() {
    for (const [preset, node] of chips) node.setAttribute('aria-pressed', String(preset === days));
    newDate.textContent = formatLong(addDays(target.scheduled_for, days));
  }

  for (const preset of DEFER_PRESETS) {
    chips.set(preset, chip({ label: `+${preset} days`, onClick: () => { days = preset; paint(); } }));
  }
  paint();

  return openSheet({
    title: 'Push back just this order',
    text: `Move the ${formatMedium(target.scheduled_for)} order later without changing your interval. Afterwards you go straight back to your normal rhythm — this does not slide every order forward.`,
    onDone,
    render: () => ({
      content: el('div', { className: 'cad-stack' }, [
        el('div', { className: 'cad-inline' }, [
          chipRow([...chips.values()]),
          el('span', { className: 'cad-inline__label', textContent: 'New date:' }),
          newDate,
        ]),
        following &&
          el('div', {
            className: 'cad-note cad-note--hairline',
            textContent: `Order after it stays on ${formatMedium(following.scheduled_for)}, unchanged.`,
          }),
      ]),
      submitLabel: 'Push back',
      onSubmit: () => defer(schedule.id, days, idempotencyKey),
    }),
  });
}
