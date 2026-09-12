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
import { openCadenceSheet, openDeferSheet } from './sheet-cadence.js';
import { openPauseSheet, resumeSchedule } from './sheet-pause.js';
import { openCancelSheet } from './sheet-cancel.js';
import { openSkipSheet } from './sheet-skip.js';
import { changeCadence, setReauthenticator } from './transitions.js';

export function mountActiveSchedule(root, config) {
  const { scheduleID } = config;

  // The two calls resolve independently so the header can be live while the queue is
  // still skeletal (components.md). Each half owns its own state.
  const state = {
    schedule: { phase: 'loading' },
    occurrences: { phase: 'loading' },
    // Set by a sheet-less write that failed; see runDirect.
    actionError: null,
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

  // Reads `screenConfig`, which is built below; nothing paints until load() runs at the
  // end of this function, by which point it exists.
  function paint() {
    root.replaceChildren(view(state, screenConfig, { onRetry: load }));
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

  /*
   * After a transition lands, the returned schedule is authoritative and goes straight
   * on screen -- the header updates with no round trip, and nothing is optimistically
   * patched.
   *
   * The occurrence list is a different matter: it is not in that response, and most of
   * these transitions rewrite it. A pause cancels every unexecuted occurrence, a cadence
   * change re-anchors and rewrites them, skip and defer move them. So the queue is
   * re-fetched rather than reasoned about, which also picks up whatever the service did
   * that the client did not predict.
   */
  function applyTransition(schedule) {
    state.schedule = { phase: 'ready', data: schedule };
    // A transition that succeeded settles whatever an earlier one failed at. Without
    // this the band from a failed resume outlives it -- still on screen after the
    // customer has since paused successfully, describing a problem that is over.
    state.actionError = null;
    paint();
    fetchHalf('occurrences', () => getOccurrences(scheduleID));
  }

  // The portal implements the transitions itself now, so the host no longer supplies
  // handlers for them -- only the URLs it alone knows (payment, orders) and the token
  /* Resume, and the deflection's "Go to N days", are writes with no sheet to report
   * into -- one is a bare button on the card, the other runs after the cancel sheet has
   * closed itself. Left as bare promises they failed silently: an unhandled rejection,
   * nothing on screen, and no way to try again. They report into a band on the page
   * instead, carrying the domain's message verbatim and a retry that re-runs the same
   * action. */
  async function runDirect(action, retry) {
    state.actionError = null;
    paint();
    try {
      applyTransition(await action());
    } catch (err) {
      state.actionError = { message: err.message, retry };
      paint();
      // Same reasoning as a sheet's onError: a rejection usually means this view is
      // stale, so re-read underneath the message.
      load();
    }
  }

  // exchange. `screenConfig` is what the view renders from.
  const screenConfig = {
    ...config,
    ...transitionActions({
      schedule: () => (state.schedule.phase === 'ready' ? state.schedule.data : null),
      occurrences: () => (state.occurrences.phase === 'ready' ? state.occurrences.data : []),
      applyTransition,
      refresh: () => {
        fetchHalf('schedule', () => getSchedule(scheduleID));
        fetchHalf('occurrences', () => getOccurrences(scheduleID));
      },
      runDirect,
    }),
  };

  // Writes share the read path's single-flighted token exchange; without this a 401 is
  // terminal for every transition while reads quietly recover.
  if (config.onReauthenticate) setReauthenticator(reauthenticate);

  load();
  return { reload: load };
}

/** Builds the five handlers the screen's controls call, each opening its sheet against
 *  the data as it stands at the moment of the click.
 *
 *  `refresh` is handed to every sheet as `onError`: a rejected transition almost always
 *  means the view is stale, so the data underneath is re-read while the message stands.
 *  `runDirect` is for the two writes that have no sheet to report into. */
function transitionActions({ schedule, occurrences, applyTransition, refresh, runDirect }) {
  const open = (fn) => () => {
    const current = schedule();
    if (current) fn(current);
  };

  const changeInterval = (preset) => {
    const current = schedule();
    if (!current) return;
    // A number comes from the cancel sheet's deflection, which has already closed
    // itself -- so this write has no sheet to fail into and goes through runDirect.
    if (typeof preset === 'number') {
      runDirect(() => changeCadence(current.id, preset), () => changeInterval(preset));
      return;
    }
    openCadenceSheet({
      schedule: current,
      occurrences: occurrences(),
      onDone: applyTransition,
      onError: refresh,
    });
  };

  const pauseNow = open((current) =>
    openPauseSheet({ schedule: current, onDone: applyTransition, onError: refresh }),
  );

  const resumeNow = open((current) =>
    runDirect(() => resumeSchedule(current), resumeNow),
  );

  return {
    onChangeInterval: changeInterval,
    onPause: pauseNow,
    onResume: resumeNow,
    onDefer: open((current) =>
      openDeferSheet({
        schedule: current,
        occurrences: occurrences(),
        onDone: applyTransition,
        onError: refresh,
      }),
    ),
    // Skip takes no occurrence: the service acts on whichever is soonest
    // (NextActionableOccurrence). So the sheet names that one and the row action is
    // offered on that row alone -- see upcomingRow.
    onSkip: () => {
      const current = schedule();
      if (!current) return;
      const { upcoming } = splitOccurrences(occurrences());
      if (upcoming.length === 0) return;
      openSkipSheet({
        schedule: current,
        target: upcoming[0],
        following: upcoming[1],
        onDone: applyTransition,
        onError: refresh,
      });
    },
    onCancel: open((current) =>
      openCancelSheet({
        schedule: current,
        onDone: applyTransition,
        onError: refresh,
        onChangeInterval: changeInterval,
        onPause: pauseNow,
      }),
    ),
  };
}

/* ---------------------------------------------------------------------------
 * View
 * ------------------------------------------------------------------------ */

function view(state, config, handlers) {
  return el('div', { className: 'cad-page' }, [
    masthead(config),
    // A write that had no sheet to fail into reports here, above the card it acted on.
    state.actionError &&
      band({
        variant: 'crit',
        heading: 'That did not go through',
        text: state.actionError.message,
        action: button({ label: 'Try again', onClick: state.actionError.retry }),
      }),
    scheduleCard(state.schedule, config, handlers),
    queueCard(state, config, handlers),
    footer(config, state.schedule.phase === 'ready' ? state.schedule.data : null),
  ]);
}

/** "Cancel recurring orders" is screen 5's entry point. It is withheld once the
 *  schedule is already canceled -- cancel is the one transition a canceled schedule
 *  still exposes a route to, and taking it can only produce "this schedule has already
 *  been canceled". Same rule as the manage column: do not offer a control whose only
 *  possible outcome is a rejection. */
function footer({ supportEmail = '[support email]', onCancel }, schedule) {
  const cancellable = onCancel && schedule?.status !== 'canceled';
  return el('div', { className: 'cad-footer' }, [
    el('span', { textContent: `Questions? Contact ${supportEmail}.` }),
    cancellable &&
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
        manageColumn(schedule, config),
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

/* Which controls make sense depends on the status, and the domain's preconditions are
 * the authority on that (internal/domain/transitions.go): pause needs an active
 * schedule, resume needs a paused one, cadence accepts active or paused, and a canceled
 * schedule accepts nothing at all. Offering a control the service is certain to reject
 * with a 409 is not a safe default -- it is a button that exists only to fail. */
function manageColumn(schedule, config) {
  if (schedule.status === 'canceled') return null;

  const controls = [];
  if (schedule.status === 'active' || schedule.status === 'paused') {
    controls.push(
      button({ label: 'Change interval', icon: 'rotate', onClick: config.onChangeInterval }),
    );
  }
  if (schedule.status === 'active') {
    controls.push(button({ label: 'Pause', icon: 'pause', onClick: config.onPause }));
  }
  if (schedule.status === 'paused') {
    controls.push(button({ label: 'Resume now', variant: 'primary', onClick: config.onResume }));
  }

  return controls.length > 0 ? el('div', { className: 'cad-manage' }, controls) : null;
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
        el(
          'div',
          { className: 'cad-queue' },
          // Only the first row carries the actions; see upcomingRow.
          upcoming.map((occ, index) => upcomingRow(occ, schedule, config, index === 0)),
        ),
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

/* `actionable` marks the one row skip and defer will actually affect.
 *
 * Neither endpoint takes an occurrence: each resolves its own target as the soonest
 * planned or pending one (NextActionableOccurrence, ordered by scheduled_for), which is
 * exactly this list's first row. Rendering the actions on every row implied a choice
 * the API does not offer -- pressing Skip on the third row skipped the first, while the
 * confirmation named the third. A customer would have been told one date and had
 * another one skipped.
 *
 * So the actions live only where they are truthful. The action column keeps its width
 * on the rest, so the status pills stay on one vertical line down the list. */
function upcomingRow(occ, schedule, config, actionable) {
  // Skip and defer are accepted on an active schedule only. A paused or failed one
  // still has planned occurrences on screen, but both actions could only answer 409 --
  // the same rule as the manage column and the cancel link.
  const actions = actionable && schedule.status === 'active'
    ? [
        config.onSkip && button({ label: 'Skip', variant: 'row', onClick: config.onSkip }),
        config.onDefer && button({ label: 'Push back', variant: 'row', onClick: config.onDefer }),
      ].filter(Boolean)
    : [];

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
