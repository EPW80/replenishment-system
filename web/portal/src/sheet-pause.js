/*
 * Screen 4 — pausing.
 *
 * Two modes, and the difference between them is expressed in what the request carries:
 * a dated pause sends `paused_until`, an open-ended one sends no body at all. The
 * handler's decoder treats a zero-length body as "no value supplied", so sending `null`
 * or an empty string instead would be a 400 rather than an open-ended pause.
 *
 * Pausing also cancels every unexecuted occurrence (spec §6), which is why the copy says
 * the scheduled orders are cleared and why the caller re-fetches the queue afterwards
 * rather than leaving a stale list on screen.
 */

import { el, radioGroup } from './components.js';
import { addDays, todayIn } from './dates.js';
import { openSheet } from './sheets.js';
import { pause, resume } from './transitions.js';

export function openPauseSheet({ schedule, onDone }) {
  const today = todayIn(schedule.timezone);
  let mode = 'until';
  // A default that is unambiguously in the future in the schedule's timezone: the
  // service rejects a resume date that is not.
  let untilDate = addDays(today, schedule.interval_days);

  const dateField = el('input', {
    className: 'cad-datefield',
    type: 'date',
    value: untilDate,
    min: addDays(today, 1),
    attrs: { 'aria-label': 'Date to resume on' },
    onchange: (event) => {
      untilDate = event.target.value;
    },
  });

  const group = el('div');

  function paint() {
    group.replaceChildren(
      radioGroup({
        name: 'cad-pause-mode',
        value: mode,
        onChange: (next) => {
          mode = next;
          paint();
        },
        options: [
          {
            value: 'until',
            label: 'Resume automatically on a date',
            helper: 'We start your schedule again that morning — no action needed from you.',
            trailing: mode === 'until' ? dateField : null,
          },
          {
            value: 'open',
            label: 'Pause until I say otherwise',
            helper: 'Resume from this page whenever you want to reorder.',
          },
        ],
      }),
    );
  }
  paint();

  return openSheet({
    title: 'Pause your recurring orders',
    text: 'Nothing is charged while paused, and the orders already scheduled are cleared. Set a date to start again automatically, or leave it open and resume whenever you like.',
    onDone,
    render: () => ({
      content: group,
      submitLabel: 'Pause orders',
      cancelLabel: 'Never mind',
      onSubmit: () =>
        pause(schedule.id, mode === 'until' ? { pausedUntil: untilDate } : {}),
    }),
  });
}

/** Resume takes no body and needs no confirmation -- it is the recovery action, and
 *  putting a sheet in front of it would be friction on the way back to ordering. */
export function resumeSchedule(schedule) {
  return resume(schedule.id);
}
