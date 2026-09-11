/*
 * The five transitions the portal can perform, and the idempotency-key lifecycle two of
 * them need.
 *
 * Every one returns the updated schedule, and the portal re-renders from that rather
 * than patching local state -- the response is authoritative about status, next_run_date,
 * paused_until, interval_days and anchor_date, several of which move in ways the client
 * should not try to predict. Resume re-anchors to today; a cadence change re-anchors to
 * the last placed order.
 *
 * What is NOT in that response is the occurrence list, and most of these rewrite it: a
 * pause cancels every unexecuted occurrence, a cadence change rewrites them, skip and
 * defer move them. Callers re-fetch occurrences after a success; see screen-active.js.
 */

import { ApiError } from './api.js';

async function post(scheduleID, action, body) {
  const path = `/api/schedules/${encodeURIComponent(scheduleID)}/${action}`;

  let res;
  try {
    res = await fetch(path, {
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      // An absent body is meaningful: `decode` treats ContentLength == 0 as "no value
      // supplied", which is how an open-ended pause is expressed. Sending `null` or an
      // empty string instead would be a 400.
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (cause) {
    throw new ApiError(0, 'Could not reach the server.', { cause });
  }

  let payload = null;
  try {
    payload = await res.json();
  } catch {
    /* handled below */
  }

  if (!res.ok) {
    // 409 carries a message from internal/domain/transitions.go. Those strings are the
    // most carefully audited copy in the repo; they render verbatim and are never
    // paraphrased, and nothing here branches on their text -- only on the status.
    const message =
      typeof payload?.error === 'string' ? payload.error : `Request failed (${res.status}).`;
    throw new ApiError(res.status, message);
  }

  return payload;
}

export const pause = (scheduleID, { pausedUntil } = {}) =>
  // Open-ended pause sends no body at all.
  post(scheduleID, 'pause', pausedUntil ? { paused_until: pausedUntil } : undefined);

export const resume = (scheduleID) => post(scheduleID, 'resume', undefined);

export const changeCadence = (scheduleID, intervalDays) =>
  post(scheduleID, 'cadence', { interval_days: intervalDays });

export const cancel = (scheduleID, reasonCode) =>
  post(scheduleID, 'cancel', { reason_code: reasonCode });

export const skip = (scheduleID, idempotencyKey) =>
  post(scheduleID, 'skip', { idempotency_key: idempotencyKey });

export const defer = (scheduleID, days, idempotencyKey) =>
  post(scheduleID, 'defer', { days, idempotency_key: idempotencyKey });

/*
 * Skip and defer resolve their target occurrence implicitly -- "whichever is next" --
 * and the first call's own mutation changes which one that is. So a blind retry does not
 * repeat the action, it performs a second one on a different occurrence (ADR 0009).
 *
 * The key is therefore minted when the customer opens the confirmation, and reused for
 * every retry of that one intent. A key generated per click turns a double-tap into two
 * deferrals. Replaying an identical key returns 200 and the current schedule with no
 * second mutation, so a retry after a dropped connection is safe.
 */
export function newIdempotencyKey() {
  // Well under the server's 200-character ceiling.
  return crypto.randomUUID();
}
