/*
 * The sheet shell every transition confirmation is built on.
 *
 * A native <dialog> rather than a hand-rolled overlay: showModal() gives focus trapping,
 * Escape-to-dismiss, inert background content and a top-layer that the host theme's
 * stacking contexts and overflow cannot clip. components.md asks for the first two
 * explicitly; the third is the failure an embedded widget cannot test for, because it
 * depends on a stylesheet we do not own.
 *
 * The shell also owns the two behaviours every transition shares:
 *
 *   - In flight, the submitting control is disabled and keeps its label, with a spinner
 *     in place of its icon. Swapping the label to "Saving…" changes the control's width
 *     and moves everything beside it.
 *   - A rejected transition renders its message verbatim, inline with the controls that
 *     caused it, and the sheet stays open and usable. A 409 is not a failure to
 *     apologise for -- it usually means the view was stale -- so the schedule is
 *     re-fetched underneath while the message stands.
 */

import { button, el } from './components.js';

/**
 * Opens a modal sheet.
 *
 * `render` builds the body and returns `{ content, submitLabel, submitVariant, onSubmit,
 * cancelLabel }`. `onSubmit` returns the updated schedule; the sheet closes on success
 * and hands it to `onDone`.
 */
export function openSheet({ title, text, render, onDone, onError }) {
  const dialog = el('dialog', { className: 'cad-sheet cad-portal' });
  const error = el('div', { className: 'cad-sheet__error', attrs: { role: 'alert' } });

  /* Closing and detaching are done together rather than detaching in a `close` handler.
   * The close event is dispatched as a queued task, and leaning on it to clean up was
   * observed leaving a closed-but-attached dialog behind after a submit that awaited the
   * network -- an invisible element intercepting nothing but still in the document, and
   * a second open would stack another beside it. Doing both here is deterministic; the
   * listener below still covers the paths the browser closes for us, Escape above all,
   * and remove() on an already-detached node is a no-op. */
  const dismiss = () => {
    dialog.close();
    dialog.remove();
  };

  const spec = render({ close: dismiss });

  const submit = button({
    label: spec.submitLabel,
    variant: spec.submitVariant ?? 'primary',
    onClick: async () => {
      error.textContent = '';
      setPending(submit, true);
      try {
        const schedule = await spec.onSubmit();
        dismiss();
        onDone(schedule);
      } catch (err) {
        // Verbatim, inline, sheet still usable.
        error.textContent = err.message;
        setPending(submit, false);
        onError?.(err);
      }
    },
  });

  dialog.append(
    el('div', { className: 'cad-sheet__body' }, [
      el('div', { className: 'cad-sheet__head' }, [
        el('h2', { className: 'cad-sheet__title', textContent: title }),
        text && el('p', { className: 'cad-sheet__text', textContent: text }),
      ]),
      spec.content,
      el('div', { className: 'cad-sheet__actions' }, [
        submit,
        button({ label: spec.cancelLabel ?? 'Cancel', onClick: dismiss }),
        error,
      ]),
    ]),
  );

  // Escape is the browser's own dismissal: it fires `cancel` and then closes. Handling
  // `cancel` puts that path through the same deterministic teardown as the buttons,
  // rather than through the `close` event this file already declines to depend on.
  dialog.addEventListener('cancel', (event) => {
    event.preventDefault();
    dismiss();
  });
  dialog.addEventListener('close', () => dialog.remove());

  document.body.append(dialog);
  dialog.showModal();
  return dialog;
}

/** Keeps the label, swaps the icon slot for a spinner, and blocks a second submit. */
function setPending(control, pending) {
  control.disabled = pending;
  const existing = control.querySelector('.cad-btn__spinner');
  if (pending && !existing) {
    control.prepend(el('span', { className: 'cad-btn__spinner', attrs: { 'aria-hidden': 'true' } }));
  } else if (!pending && existing) {
    existing.remove();
  }
}

/** A row of preset chips with a live selection, used by the cadence and defer sheets. */
export function chipRow(chips) {
  return el('div', { className: 'cad-chiprow' }, chips);
}
