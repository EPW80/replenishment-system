/*
 * Screen 1 (Active schedule) and screen 6 (Phone) -- one screen, two widths. The narrow
 * layout is a container query in portal.css, not a second implementation.
 *
 * Every string here comes from copy.md. Two deliberate departures, both because the
 * literal string would be false on a real schedule:
 *
 *   - The queue caption is "{n} scheduled ahead" rather than "Three scheduled ahead".
 *     The horizon is configurable (MATERIALIZE_HORIZON), so the word "Three" is only
 *     true at the default.
 *   - The countdown phrases spell out their one-day and today cases instead of
 *     rendering "1 days". See dates.js.
 *
 * Controls that lead to screens 3, 4 and 5 render only when the host passes a handler
 * for them. This step of the build order is "two GETs, no writes", and a button that
 * goes nowhere is worse than an absent one -- the action column still reserves its
 * width, so the transitions PR adds handlers without moving anything.
 */

import { getOccurrences, getSchedule, ApiError } from './api.js';
import {
  band,
  button,
  card,
  el,
  field,
  itemRow,
  linkButton,
  occurrenceRow,
  pill,
  section,
  skeleton,
} from './components.js';
import {
  chargesPhrase,
  countdownPhrase,
  countdownPhraseShort,
  formatCompact,
  formatLong,
  formatMedium,
  formatShort,
} from './dates.js';
import { occurrenceStatus, scheduleStatus, splitOccurrences } from './status.js';

export function mountActiveSchedule(root, config) {
  const { scheduleID } = config;

  // The two calls resolve independently so the header can be live while the queue is
  // still skeletal (components.md). Each half owns its own state.
  const state = {
    schedule: { phase: 'loading' },
    occurrences: { phase: 'loading' },
  };

  // Both halves are in flight at once, so an expired token 401s both. They share one
  // exchange, single-flighted here, and each retries at most once.
  //
  // The guard has to be per-half, not shared: a shared "already tried" flag would send
  // whichever half lost the race straight to an error state, and the exchange that just
  // succeeded would only ever retry the half that started it -- half a portal, from a
  // refresh that worked.
  let reauth = null;

  function reauthenticate() {
    reauth ??= Promise.resolve(config.onReauthenticate());
    return reauth;
  }

  function paint() {
    root.replaceChildren(view(state, config, { onRetry: load }));
  }

  async function fetchHalf(key, fn, retried = false) {
    state[key] = { phase: 'loading' };
    try {
      state[key] = { phase: 'ready', data: await fn() };
    } catch (err) {
      // A 401 means the portal's JWT expired. Re-run the host's token exchange rather
      // than showing an error, and only surface one if that also fails (components.md).
      if (err instanceof ApiError && err.status === 401 && config.onReauthenticate && !retried) {
        try {
          await reauthenticate();
          return fetchHalf(key, fn, true);
        } catch {
          /* fall through to the error state */
        }
      }
      state[key] = { phase: 'error', error: err };
    }
    paint();
  }

  function load() {
    reauth = null;
    paint();
    fetchHalf('schedule', () => getSchedule(scheduleID));
    fetchHalf('occurrences', () => getOccurrences(scheduleID));
  }

  load();
  return { reload: load };
}

/* ---------------------------------------------------------------------------
 * View
 * ------------------------------------------------------------------------ */

function view(state, config, handlers) {
  return el('div', { className: 'cad-page' }, [
    masthead(config),
    scheduleCard(state.schedule, config, handlers),
    queueCard(state, config, handlers),
    footer(config),
  ]);
}

/** "Cancel recurring orders" is screen 5's entry point and renders only when the host
 *  wires it, for the same reason the row actions do. */
function footer({ supportEmail = '[support email]', onCancel }) {
  return el('div', { className: 'cad-footer' }, [
    el('span', { textContent: `Questions? Contact ${supportEmail}.` }),
    onCancel &&
      el('a', {
        className: 'cad-link',
        textContent: 'Cancel recurring orders',
        href: '#',
        onclick: (event) => {
          event.preventDefault();
          onCancel();
        },
      }),
  ]);
}

function masthead({ brand = '[Brand]' }) {
  return el('div', { className: 'cad-masthead' }, [
    el('div', { className: 'cad-eyebrow', textContent: `${brand} · Account` }),
    el('h1', { className: 'cad-h1', textContent: 'Recurring orders' }),
    el('p', {
      className: 'cad-intro',
      textContent:
        'Reorder on a schedule you set. Change how often it repeats, skip an order, ' +
        'or push one back — any time up to 72 hours before it is charged.',
    }),
  ]);
}

/* ---------------------------------------------------------------------------
 * Schedule card
 * ------------------------------------------------------------------------ */

function scheduleCard(half, config, handlers) {
  if (half.phase === 'loading') return card([headerSkeleton(), fieldsSkeleton()]);
  if (half.phase === 'error') return card([errorBand(half.error, handlers.onRetry)]);

  const schedule = half.data;
  const status = scheduleStatus(schedule.status);

  return card([
    // Two bands never stack; a failure outranks anything else the card could say.
    //
    // Only copy.md's band heading is rendered. Its body ("We tried three times over
    // the past week for the order due {date}") needs the attempt count and failure
    // date that Phase 2's dunning ladder produces, and neither exists yet -- so the
    // band states the fact it can stand behind and leaves the rest to screen 4, which
    // owns this state properly.
    schedule.status === 'failed' &&
      band({
        variant: 'crit',
        heading: 'We could not charge your card',
        action: config.paymentURL
          ? linkButton({ label: 'Update card and restart', href: config.paymentURL })
          : null,
      }),
    section(
      [
        el('div', { className: 'cad-headline cad-headline--main' }, [
          el('div', { className: 'cad-status-line' }, [
            pill(status),
            scheduleRef(schedule.id),
          ]),
          nextOrderBlock(schedule),
        ]),
        manageColumn(config),
      ],
      { className: 'cad-section--header' },
    ),
    section([factsGrid(schedule, config)]),
    section([itemsBlock(schedule)]),
  ]);
}

/** The artboard shows a short reference (SCH·8F2A41C9), not the full UUID. The whole id
 *  stays on the element's title, because a shortened id is no use to somebody reading it
 *  back to support. */
function scheduleRef(id) {
  const short = id.replace(/-/g, '').slice(0, 8).toUpperCase();
  return el('span', {
    className: 'cad-mono',
    textContent: `SCH·${short}`,
    attrs: { title: id },
  });
}

function nextOrderBlock(schedule) {
  // next_run_date is null on a paused or canceled schedule; there is no date to lead
  // with, so the block says what the state is instead of rendering an empty headline.
  if (!schedule.next_run_date) {
    return el('div', { className: 'cad-next' }, [
      el('div', { className: 'cad-field__label', textContent: 'Next order' }),
      el('div', {
        className: 'cad-headline__sub',
        textContent:
          schedule.status === 'paused'
            ? pausedSubtitle(schedule)
            : 'Nothing is scheduled.',
      }),
    ]);
  }

  const repeats = `Repeats every ${schedule.interval_days} days`;
  const sub = (phrase) => (phrase ? `${repeats} · ${phrase}` : repeats);

  return el('div', { className: 'cad-next' }, [
    el('div', { className: 'cad-field__label', textContent: 'Next order' }),
    // Both spellings are rendered; the container query shows one. Keeping them in the
    // DOM avoids a resize listener for what is purely a presentation choice. The
    // narrow wording is the phone artboard's, not an abbreviation of the wide one.
    el('div', {
      className: 'cad-headline__date cad-wide-only',
      textContent: formatLong(schedule.next_run_date),
    }),
    el('div', {
      className: 'cad-headline__date cad-narrow-only',
      textContent: formatShort(schedule.next_run_date),
    }),
    el('div', {
      className: 'cad-headline__sub cad-wide-only',
      textContent: sub(countdownPhrase(schedule.next_run_date, schedule.timezone)),
    }),
    el('div', {
      className: 'cad-headline__sub cad-narrow-only',
      textContent: sub(countdownPhraseShort(schedule.next_run_date, schedule.timezone)),
    }),
  ]);
}

/** copy.md's paused card says "Resuming {date}". It has no wording for an open-ended
 *  pause -- the pause sheet that sets one is screen 4 -- so that case is spelled here
 *  in the same voice, saying what resumes it rather than when. */
function pausedSubtitle(schedule) {
  if (!schedule.paused_until) return 'Paused until you resume it.';
  return `Resuming ${formatMedium(schedule.paused_until)}`;
}

function manageColumn(config) {
  const controls = [
    config.onChangeInterval &&
      button({ label: 'Change interval', icon: 'rotate', onClick: config.onChangeInterval }),
    config.onPause && button({ label: 'Pause', icon: 'pause', onClick: config.onPause }),
  ].filter(Boolean);
  if (controls.length === 0) return null;
  return el('div', { className: 'cad-manage' }, controls);
}

function factsGrid(schedule, config) {
  const fields = [
    field({ label: 'Interval', value: `Every ${schedule.interval_days} days` }),
    field({ label: 'Started', value: formatMedium(schedule.anchor_date) }),
    field({ label: 'Time zone', value: schedule.timezone }),
    // The address lives in WooCommerce; the API carries only its id, and not in the
    // response. Until the theme resolves it this is a placeholder, and reads as one.
    field({ label: 'Ships to', value: '[Default shipping address]', placeholder: true }),
    field({
      label: 'Payment',
      value: 'Card on file',
      placeholder: true,
      extra: config.paymentURL
        ? el('a', {
            className: 'cad-link cad-link--sm',
            textContent: 'Update',
            href: config.paymentURL,
          })
        : null,
    }),
  ];

  // "0% off every order" is noise, not a fact.
  if (schedule.discount_pct > 0) {
    fields.push(
      field({
        label: 'Recurring discount',
        value: `${formatPercent(schedule.discount_pct)}% off every order`,
        accent: true,
      }),
    );
  }

  return el('div', { className: 'cad-fields' }, fields);
}

function formatPercent(value) {
  return Number.isInteger(value) ? String(value) : String(Number(value.toFixed(2)));
}

function itemsBlock(schedule) {
  return el('div', { className: 'cad-stack' }, [
    el('div', { className: 'cad-section-head' }, [
      el('div', { className: 'cad-field__label', textContent: 'What ships each time' }),
      el('div', { className: 'cad-caption', textContent: 'Names and prices from the store catalog' }),
    ]),
    el(
      'div',
      { className: 'cad-items' },
      (schedule.items ?? []).map((item) => itemRow(item)),
    ),
  ]);
}

/* ---------------------------------------------------------------------------
 * Queue card
 * ------------------------------------------------------------------------ */

function queueCard(state, config, handlers) {
  const scheduleHalf = state.schedule;
  const half = state.occurrences;

  const head = section(
    [
      el('div', { className: 'cad-section-head' }, [
        // The phone artboard heads this list "Coming up"; the wide one spells it out.
        el('h2', { className: 'cad-h2' }, [
          el('span', { className: 'cad-wide-only', textContent: 'Upcoming orders' }),
          el('span', { className: 'cad-narrow-only', textContent: 'Coming up' }),
        ]),
        // The count is suppressed at zero: the empty state below already says there is
        // nothing scheduled, and says why, which "0 scheduled ahead" does not.
        half.phase === 'ready' &&
          scheduleHalf.phase === 'ready' &&
          splitOccurrences(half.data).upcoming.length > 0 &&
          el('span', {
            className: 'cad-caption',
            textContent: `${splitOccurrences(half.data).upcoming.length} scheduled ahead`,
          }),
      ]),
      el('p', {
        className: 'cad-note',
        textContent:
          'Skip or push back any order up to 72 hours before it is charged. ' +
          'We email you before each one.',
      }),
    ],
    { className: 'cad-section--head' },
  );

  if (half.phase === 'loading' || scheduleHalf.phase === 'loading') {
    return card([head, section([queueSkeleton()])]);
  }
  // When the schedule itself failed, its card is already carrying the message and the
  // retry. Repeating it here reads as two separate faults.
  if (scheduleHalf.phase === 'error') {
    return card([head]);
  }
  // The queue can fail on its own while the schedule renders fine. Without its own
  // retry the customer's only way out is a full reload, so it gets the same band and
  // affordance the schedule error gets rather than a line of grey text.
  if (half.phase === 'error') {
    return card([errorBand(half.error, handlers.onRetry, 'We could not load your orders'), head]);
  }

  const schedule = scheduleHalf.data;
  const { upcoming, sent } = splitOccurrences(half.data);

  const body = [head];

  if (upcoming.length === 0) {
    body.push(section([el('div', { className: 'cad-empty', textContent: emptyReason(schedule) })]));
  } else {
    body.push(
      section([
        el('div', { className: 'cad-queue' }, upcoming.map((occ) => upcomingRow(occ, schedule, config))),
      ]),
    );
  }

  if (sent.length > 0) {
    body.push(
      section(
        [
          el('div', { className: 'cad-field__label', textContent: 'Already sent' }),
          el('div', { className: 'cad-queue' }, sent.map((occ) => sentRow(occ, config))),
        ],
        { className: 'cad-section--settled' },
      ),
    );
  }

  return card(body);
}

/** An empty queue is not an error -- say which kind of empty it is. "No upcoming
 *  orders" reads like something broke; naming the cause does not (components.md). */
function emptyReason(schedule) {
  if (schedule.status === 'paused') return 'Paused, so nothing is scheduled.';
  if (schedule.status === 'canceled') return 'Cancelled, so nothing is scheduled.';
  return 'Nothing is scheduled yet. Your next order will appear here shortly.';
}

function upcomingRow(occ, schedule, config) {
  const actions = [
    config.onSkip && button({ label: 'Skip', variant: 'row', onClick: () => config.onSkip(occ) }),
    config.onDefer &&
      button({ label: 'Push back', variant: 'row', onClick: () => config.onDefer(occ) }),
  ].filter(Boolean);

  return occurrenceRow({
    sequenceNo: occ.sequence_no,
    date: formatLong(occ.scheduled_for),
    dateNarrow: formatCompact(occ.scheduled_for),
    meta: chargesPhrase(occ.scheduled_for, schedule.timezone),
    status: occurrenceStatus(occ.status),
    actions,
  });
}

/* screens.md's status table gives already-sent rows two actions: a placed row links to
 * its order_id in WooCommerce, and a failed row offers payment recovery. Both need
 * something only the host knows -- the store's order URL, and where payment details are
 * managed -- so both render when it supplies them and degrade to plain text when it
 * does not. A failed occurrence under a still-active schedule is the case that needs
 * this: the schedule-level band never appears, so without the row action the customer
 * has no way back. */
function sentRow(occ, config) {
  const status = occurrenceStatus(occ.status);
  const actions = [];
  if (occ.status === 'failed' && config.paymentURL) {
    actions.push(
      linkButton({ label: 'Update payment', href: config.paymentURL }),
    );
  }

  return occurrenceRow({
    sequenceNo: occ.sequence_no,
    date: formatLong(occ.scheduled_for),
    dateNarrow: formatCompact(occ.scheduled_for),
    meta: orderMeta(occ, config),
    status,
    actions,
  });
}

/** The order reference, linked when the host gave a URL template. */
function orderMeta(occ, config) {
  if (!occ.order_id) return null;
  const label = `Order #${occ.order_id}`;
  if (!config.orderURLTemplate) return label;
  return el('a', {
    className: 'cad-link',
    textContent: label,
    href: config.orderURLTemplate.replace('{id}', encodeURIComponent(occ.order_id)),
  });
}

/* ---------------------------------------------------------------------------
 * Loading and error
 * ------------------------------------------------------------------------ */

function headerSkeleton() {
  return section(
    [
      el('div', { className: 'cad-headline cad-headline--main' }, [
        skeleton('pill'),
        skeleton('title'),
        skeleton('line', { width: '220px' }),
      ]),
    ],
    { className: 'cad-section--header' },
  );
}

function fieldsSkeleton() {
  return section([
    el(
      'div',
      { className: 'cad-fields' },
      Array.from({ length: 3 }, () =>
        el('div', { className: 'cad-field' }, [
          skeleton('line', { width: '70px' }),
          skeleton('line', { width: '120px' }),
        ]),
      ),
    ),
  ]);
}

function queueSkeleton() {
  return el(
    'div',
    { className: 'cad-queue' },
    Array.from({ length: 3 }, () =>
      el('div', { className: 'cad-occ' }, [
        el('div', { className: 'cad-occ__when cad-occ__when--skel' }, [
          skeleton('line', { width: '210px' }),
          skeleton('line', { width: '110px' }),
        ]),
        skeleton('pill'),
      ]),
    ),
  );
}

function errorBand(error, onRetry, heading = 'We could not load your schedule') {
  return band({
    variant: 'crit',
    heading,
    // The API's message verbatim (screens.md); the portal never paraphrases one.
    text: error.message,
    action: button({ label: 'Try again', onClick: onRetry }),
  });
}
