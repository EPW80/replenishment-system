/*
 * Entry point. Reads its configuration from the mount element's data attributes so the
 * host page configures the widget in markup rather than by editing this file:
 *
 *   <div class="cad-portal" data-schedule-id="..." data-brand="House of Helix"></div>
 *
 * In production that element is printed by the WordPress mu-plugin, which already knows
 * the logged-in customer's schedule. Locally it is printed by index.html.
 */

import { mountActiveSchedule } from './screen-active.js';

function boot() {
  const root = document.querySelector('[data-cad-portal]');
  if (!root) return;

  const { scheduleId, brand, supportEmail, paymentUrl } = root.dataset;
  if (!scheduleId) {
    root.textContent = 'This portal needs a schedule id.';
    return;
  }

  mountActiveSchedule(root, {
    scheduleID: scheduleId,
    brand,
    supportEmail,
    paymentURL: paymentUrl,
    // Screens 3, 4 and 5 do not exist yet, so no transition handlers are passed and
    // their controls do not render. See the note at the top of screen-active.js.
  });
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', boot, { once: true });
} else {
  boot();
}
