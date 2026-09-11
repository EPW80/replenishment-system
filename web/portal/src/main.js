/*
 * Entry point.
 *
 * Markup configures the widget, so the host sets it up without editing JavaScript. The
 * `data-cad-portal` marker is what boot() looks for -- the class alone is styling and
 * will not mount anything:
 *
 *   <div class="cad-portal" data-cad-portal
 *        data-schedule-id="..."
 *        data-brand="House of Helix"
 *        data-support-email="support@example.com"
 *        data-payment-url="/my-account/payment-methods"
 *        data-order-url="/my-account/view-order/{id}"></div>
 *
 * One thing cannot be expressed as an attribute: the token exchange. The host supplies
 * it on `window.cadenceOSPortal` before this module loads, and mountActiveSchedule is
 * exported for a host that would rather mount the widget itself:
 *
 *   window.cadenceOSPortal = { onReauthenticate: () => fetch('/wp-json/cadence/v1/token') };
 *
 * onReauthenticate matters more than it looks: without it a 401 is terminal, because
 * the retry path in screen-active.js has nothing to call. In production the WordPress
 * mu-plugin owns the nonce-to-JWT exchange (spec §4), so it is the one that fills this
 * in. Locally cmd/portaldev mints a token per request and never 401s, which is exactly
 * why this needs saying rather than discovering.
 */

import { mountActiveSchedule } from './screen-active.js';

export { mountActiveSchedule };

function boot() {
  const root = document.querySelector('[data-cad-portal]');
  if (!root) return;

  const { scheduleId, brand, supportEmail, paymentUrl, orderUrl } = root.dataset;
  if (!scheduleId) {
    root.textContent = 'This portal needs a schedule id.';
    return;
  }

  const host = window.cadenceOSPortal ?? {};

  mountActiveSchedule(root, {
    scheduleID: scheduleId,
    brand,
    supportEmail,
    paymentURL: paymentUrl,
    // `{id}` is substituted with the occurrence's order_id. WooCommerce owns the order
    // (spec §1), so the host decides what an order links to.
    orderURLTemplate: orderUrl,
    onReauthenticate: host.onReauthenticate,
    // The transitions are the portal's own now -- it opens the sheets and calls the API
    // itself, so the host supplies only what it alone knows (the URLs above) and the
    // token exchange. Handlers passed here would be ignored.
  });
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', boot, { once: true });
} else {
  boot();
}
