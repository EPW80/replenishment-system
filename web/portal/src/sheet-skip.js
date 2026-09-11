/*
 * Skipping one order.
 *
 * It is a row action rather than a screen, but it still gets a sheet, and the reason is
 * the idempotency key rather than the confirmation: the key has to be minted when the
 * customer opens this, and reused for every retry of the same intent. Minting it on
 * submit would make a double-tap into two skips, because skip resolves its target
 * implicitly and the first call moves what "next" means (ADR 0009).
 *
 * The nudge is here because repeated skipping is a cadence problem wearing a different
 * hat, and saying so once is more useful than letting someone skip every month.
 */

import { el, inset } from './components.js';
import { formatMedium } from './dates.js';
import { openSheet } from './sheets.js';
import { newIdempotencyKey, skip } from './transitions.js';

export function openSkipSheet({ schedule, target, following, onDone }) {
  const idempotencyKey = newIdempotencyKey();

  const body = following
    ? `You will not be charged for this one. Your schedule keeps its rhythm — the next order stays on ${formatMedium(following.scheduled_for)}, exactly ${schedule.interval_days} days on from the one you are skipping.`
    : 'You will not be charged for this one. Your schedule keeps its rhythm.';

  return openSheet({
    title: `Skip the order on ${formatMedium(target.scheduled_for)}?`,
    text: body,
    onDone,
    render: () => ({
      content: inset([
        el('p', {
          className: 'cad-note',
          textContent:
            'Want the gap to be longer every time instead? Change the interval rather than skipping repeatedly.',
        }),
      ]),
      submitLabel: 'Skip this order',
      cancelLabel: 'Keep it',
      onSubmit: () => skip(schedule.id, idempotencyKey),
    }),
  });
}
