/*
 * Screen 5 — cancelling.
 *
 * Two rules govern this screen and both are easy to violate by being helpful.
 *
 * The request carries the reason *code*, never the label. The labels exist only to be
 * read; a label reaching `reason_code` is rejected outright, and if it were not it would
 * silently corrupt the churn aggregation in spec §8.
 *
 * The deflection panel appears for `too_frequent` alone. It is an answer to a specific
 * complaint -- orders arriving closer together than wanted, which a longer interval
 * actually fixes -- and screens.md is explicit that generalising it into a retention
 * interstitial for every reason would be a dark pattern.
 *
 * The primary button is the danger button, for the same reason: the destructive action
 * is what the customer came to do, and dressing it down to a secondary treatment while
 * "Keep my schedule" takes the primary slot is not politeness.
 */

import { button, el, inset, radioGroup } from './components.js';
import { openSheet } from './sheets.js';
import { cancel } from './transitions.js';
import { MAX_INTERVAL } from './sheet-cadence.js';

/** The closed set, in the artboard's order. Codes match domain.CancellationReasons. */
const REASONS = [
  { value: 'too_expensive', label: 'It costs more than I want to spend' },
  { value: 'too_frequent', label: 'Orders arrive closer together than I want' },
  { value: 'switched_brand', label: 'I am buying this somewhere else' },
  { value: 'delivery_issue', label: 'There was a problem with delivery' },
  { value: 'payment_issue', label: 'There was a problem with payment' },
  { value: 'no_longer_wanted', label: 'I do not want this product any more' },
  { value: 'other', label: 'Something else', className: 'cad-radio--wide' },
];

/** The two longer intervals the deflection offers: half again and double, which is what
 *  the artboard shows for a 28-day schedule (42 and 56), clamped to the maximum. */
export function deflectionIntervals(intervalDays) {
  const candidates = [Math.round(intervalDays * 1.5), intervalDays * 2]
    .map((n) => Math.min(n, MAX_INTERVAL))
    .filter((n) => n > intervalDays);
  return [...new Set(candidates)];
}

export function openCancelSheet({ schedule, onDone, onError, onChangeInterval, onPause }) {
  let reason = null;
  const body = el('div', { className: 'cad-stack' });
  // Set once the shell has built the dialog; the deflection's two ways out have to
  // close this sheet before opening anything else, or a second dialog stacks on top of
  // a cancellation the customer has decided against.
  let dismiss = () => {};

  function paint() {
    const [longer, longest] = deflectionIntervals(schedule.interval_days);
    /* Cancel accepts a failed schedule; the two ways out of this panel do not. Cadence
     * needs active or paused, pause needs active. Offering an escape route that can only
     * answer 409 is worse than offering none, so each is gated on the status that can
     * actually take it. */
    const canChangeCadence = schedule.status === 'active' || schedule.status === 'paused';
    const canPause = schedule.status === 'active';
    const showDeflection = reason === 'too_frequent' && longer && (canChangeCadence || canPause);

    body.replaceChildren(
      el('div', { className: 'cad-stack' }, [
        el('div', { className: 'cad-section-head' }, [
          el('div', { className: 'cad-field__label', textContent: 'Why are you cancelling?' }),
          el('div', { className: 'cad-caption', textContent: 'Optional, but it genuinely helps us' }),
        ]),
        radioGroup({
          name: 'cad-cancel-reason',
          value: reason,
          className: 'cad-radios--grid',
          options: REASONS,
          onChange: (next) => {
            reason = next;
            paint();
          },
        }),
      ]),
      showDeflection &&
        inset([
          el('div', { className: 'cad-stack' }, [
            el('div', { className: 'cad-deflect__heading', textContent: 'A longer gap might fix that' }),
            el('p', {
              className: 'cad-note',
              textContent: `You are on ${schedule.interval_days} days. Moving to ${longer}${longest ? ` or ${longest}` : ''} keeps the recurring discount and stretches out the deliveries.`,
            }),
            el('div', { className: 'cad-chiprow' }, [
              canChangeCadence &&
                button({
                  label: `Go to ${longer} days`,
                  onClick: () => {
                    dismiss();
                    onChangeInterval(longer);
                  },
                }),
              canPause &&
                button({
                  label: 'Pause instead',
                  onClick: () => {
                    dismiss();
                    onPause();
                  },
                }),
            ]),
          ]),
        ]),
    );
  }
  paint();

  return openSheet({
    title: 'Cancel your recurring orders?',
    text: 'Nothing further will be charged and your scheduled orders are cleared. You can order again any time from the store — this only ends the schedule.',
    onDone,
    onError,
    render: ({ close }) => {
      dismiss = close;
      return {
        content: body,
        submitLabel: 'Cancel recurring orders',
        submitVariant: 'danger',
        cancelLabel: 'Keep my schedule',
        // `other` is the code for "Something else", so an unanswered prompt still sends
        // a member of the closed set rather than an empty string the server rejects.
        onSubmit: () => cancel(schedule.id, reason ?? 'other'),
      };
    },
  });
}
