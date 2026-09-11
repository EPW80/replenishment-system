const storageKey = 'cadenceos.console.connection';

const state = {
  config: readConfig(),
  schedules: [],
  schedule: null,
  occurrences: [],
  loading: false,
  activeIntentKey: null,
  dialogSubmit: null,
};

const elements = {
  content: document.querySelector('#content'),
  list: document.querySelector('#schedule-list'),
  customerLabel: document.querySelector('#customer-label'),
  connection: document.querySelector('#connection-state'),
  globalMessage: document.querySelector('#global-message'),
  dialog: document.querySelector('#action-dialog'),
  dialogForm: document.querySelector('#dialog-form'),
  dialogEyebrow: document.querySelector('#dialog-eyebrow'),
  dialogTitle: document.querySelector('#dialog-title'),
  dialogBody: document.querySelector('#dialog-body'),
  dialogError: document.querySelector('#dialog-error'),
  dialogSubmit: document.querySelector('#dialog-submit'),
  toast: document.querySelector('#toast'),
};

class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

function readConfig() {
  try {
    return JSON.parse(sessionStorage.getItem(storageKey)) || { customerId: '', portalToken: '', serviceKey: '' };
  } catch {
    return { customerId: '', portalToken: '', serviceKey: '' };
  }
}

function saveConfig(config) {
  state.config = config;
  sessionStorage.setItem(storageKey, JSON.stringify(config));
  updateConnectionState();
}

function escapeHTML(value) {
  return String(value ?? '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#039;');
}

function pathForSchedule(id) {
  return `/console/schedules/${encodeURIComponent(id)}`;
}

function currentScheduleId() {
  const match = location.pathname.match(/^\/console\/schedules\/([^/]+)\/?$/);
  return match ? decodeURIComponent(match[1]) : null;
}

function updateConnectionState() {
  const ready = Boolean(state.config.customerId && state.config.portalToken);
  elements.connection.classList.toggle('ready', ready);
  elements.connection.lastChild.textContent = ready ? 'Customer API ready' : 'Not connected';
  elements.customerLabel.textContent = state.config.customerId || 'Not set';
}

function setGlobalError(message = '') {
  elements.globalMessage.hidden = !message;
  elements.globalMessage.textContent = message;
}

function setDialogError(message = '') {
  elements.dialogError.hidden = !message;
  elements.dialogError.textContent = message;
}

function showToast(message) {
  elements.toast.textContent = message;
  elements.toast.hidden = false;
  clearTimeout(showToast.timer);
  showToast.timer = setTimeout(() => { elements.toast.hidden = true; }, 3500);
}

async function api(path, { method = 'GET', body, credential = 'portal' } = {}) {
  const token = credential === 'service' ? state.config.serviceKey : state.config.portalToken;
  if (!token) {
    throw new ApiError(0, credential === 'service'
      ? 'Add a service API key in API connection before creating a schedule.'
      : 'Add a customer portal token in API connection before loading schedules.');
  }

  const headers = { Authorization: `Bearer ${token}` };
  if (body !== undefined) headers['Content-Type'] = 'application/json';

  let response;
  try {
    response = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new ApiError(0, 'The API could not be reached. Check that CadenceOS is running, then try again.');
  }

  let payload = null;
  try {
    payload = await response.json();
  } catch {
    if (!response.ok) throw new ApiError(response.status, `The API returned HTTP ${response.status}.`);
  }
  if (!response.ok) {
    throw new ApiError(response.status, payload?.error || `The API returned HTTP ${response.status}.`);
  }
  return payload;
}

function statusInfo(status, occurrence = false) {
  const labels = occurrence
    ? { planned: 'Scheduled', pending: 'Charging soon', placed: 'Placed', skipped: 'Skipped by you', failed: 'Could not be charged', canceled: 'Canceled' }
    : { active: 'Active', paused: 'Paused', canceled: 'Cancelled', failed: 'Needs payment update' };
  const variants = { active: 'ok', placed: 'ok', pending: 'warn', failed: 'crit', planned: 'idle', paused: 'idle', skipped: 'idle', canceled: 'idle' };
  return { label: labels[status] || status, variant: variants[status] || 'idle' };
}

function pill(status, occurrence = false) {
  const info = statusInfo(status, occurrence);
  return `<span class="pill ${info.variant}">${escapeHTML(info.label)}</span>`;
}

function formatDate(value) {
  if (!value) return 'Not scheduled';
  const date = new Date(`${value}T12:00:00Z`);
  if (Number.isNaN(date.valueOf())) return value;
  return new Intl.DateTimeFormat(undefined, { weekday: 'short', month: 'long', day: 'numeric', year: 'numeric', timeZone: 'UTC' }).format(date);
}

function shortId(id) {
  return id?.length > 13 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id;
}

function loadingCard() {
  return `<div class="card"><div class="card-section"><div class="skeleton skeleton-title"></div><div class="skeleton skeleton-copy"></div></div><div class="card-section facts"><div class="skeleton"></div><div class="skeleton"></div><div class="skeleton"></div></div><div class="card-section"><div class="skeleton skeleton-row"></div><div class="skeleton skeleton-row"></div></div></div>`;
}

function renderWelcome() {
  elements.content.innerHTML = `<section class="empty"><div class="empty-inner"><div class="empty-mark" aria-hidden="true">C</div><span class="eyebrow">Development console</span><h1>Exercise the schedule lifecycle</h1><p>Connect a customer portal token to load schedules, or add the service credential to create test data.</p><button class="button primary" type="button" data-open-settings>Set up API connection</button></div></section>`;
}

function renderEmptyList() {
  elements.content.innerHTML = `<section class="empty"><div class="empty-inner"><div class="empty-mark" aria-hidden="true">0</div><span class="eyebrow">No schedules</span><h1>Create a test schedule</h1><p>This customer has no recurring orders yet. Create one to test cadence and lifecycle changes.</p><button class="button primary" type="button" data-create>Create test schedule</button></div></section>`;
}

function renderScheduleList() {
  const selected = currentScheduleId();
  if (state.loading && !state.schedules.length) {
    elements.list.innerHTML = '<div class="skeleton"></div><div class="skeleton"></div><div class="skeleton"></div>';
    return;
  }
  if (!state.schedules.length) {
    elements.list.innerHTML = '<p class="schedule-list-empty">No schedules found</p>';
    return;
  }
  elements.list.innerHTML = state.schedules.map(schedule => `
    <a class="schedule-link ${selected === schedule.id ? 'active' : ''}" href="${pathForSchedule(schedule.id)}" data-link>
      <strong>${escapeHTML(schedule.items?.[0]?.sku || 'Schedule')}</strong>
      ${pill(schedule.status)}
      <small class="mono">${escapeHTML(shortId(schedule.id))} · every ${escapeHTML(schedule.interval_days)} days</small>
    </a>`).join('');
}

async function loadSchedules({ render = true } = {}) {
  if (!state.config.customerId || !state.config.portalToken) {
    state.schedules = [];
    renderScheduleList();
    renderWelcome();
    return;
  }

  state.loading = true;
  setGlobalError();
  renderScheduleList();
  try {
    const data = await api(`/customers/${encodeURIComponent(state.config.customerId)}/schedules`);
    state.schedules = data.schedules || [];
    if (render && !currentScheduleId()) {
      if (state.schedules.length) navigate(pathForSchedule(state.schedules[0].id), { replace: true });
      else renderEmptyList();
    }
  } catch (error) {
    state.schedules = [];
    setGlobalError(error.message);
    if (error.status === 401) renderWelcome();
  } finally {
    state.loading = false;
    renderScheduleList();
  }
}

async function loadDetail(id) {
  state.loading = true;
  state.schedule = null;
  state.occurrences = [];
  setGlobalError();
  elements.content.innerHTML = loadingCard();
  renderScheduleList();

  const scheduleRequest = api(`/schedules/${encodeURIComponent(id)}`);
  const occurrencesRequest = api(`/schedules/${encodeURIComponent(id)}/occurrences`);
  const [scheduleResult, occurrencesResult] = await Promise.allSettled([scheduleRequest, occurrencesRequest]);

  if (scheduleResult.status === 'rejected') {
    state.loading = false;
    setGlobalError(scheduleResult.reason.message);
    elements.content.innerHTML = `<section class="empty"><div class="empty-inner"><h1>Schedule unavailable</h1><p>The detail view could not be loaded.</p><button class="button secondary" type="button" data-retry>Try again</button></div></section>`;
    return;
  }

  state.schedule = scheduleResult.value;
  if (occurrencesResult.status === 'fulfilled') {
    state.occurrences = occurrencesResult.value.occurrences || [];
  } else {
    setGlobalError(occurrencesResult.reason.message);
  }
  state.loading = false;
  renderScheduleList();
  renderDetail();
}

function actionButtons(schedule) {
  const buttons = [];
  if (schedule.status === 'active') {
    buttons.push(['cadence', 'Change interval', 'secondary'], ['pause', 'Pause', 'secondary'], ['skip', 'Skip next', 'secondary'], ['defer', 'Push back', 'primary']);
  } else if (schedule.status === 'paused') {
    buttons.push(['cadence', 'Change interval', 'secondary'], ['resume', 'Resume now', 'primary']);
  }
  if (schedule.status !== 'canceled') buttons.push(['cancel', 'Cancel schedule', 'secondary']);
  return buttons.map(([action, label, variant]) => `<button class="button ${variant}" type="button" data-action="${action}">${label}</button>`).join('');
}

function occurrenceRows() {
  const visible = state.occurrences.filter(item => item.status !== 'canceled');
  if (!visible.length) {
    const message = state.schedule.status === 'paused' ? 'Paused, so nothing is scheduled.' : 'No upcoming orders have been materialized yet.';
    return `<p class="muted-copy">${message}</p>`;
  }
  return `<ol class="timeline">${visible.map(item => {
    const actionable = state.schedule.status === 'active' && ['planned', 'pending'].includes(item.status);
    return `<li class="timeline-row">
      <span class="sequence">${String(item.sequence_no).padStart(2, '0')}</span>
      <span class="timeline-date"><strong>${escapeHTML(formatDate(item.scheduled_for))}</strong><small class="mono">${escapeHTML(item.scheduled_for)}${item.order_id ? ` · order ${escapeHTML(item.order_id)}` : ''}</small></span>
      ${pill(item.status, true)}
      <span class="row-actions">${actionable ? '<button class="button" type="button" data-action="skip">Skip</button><button class="button" type="button" data-action="defer">Push back</button>' : ''}</span>
    </li>`;
  }).join('')}</ol>`;
}

function renderDetail() {
  const schedule = state.schedule;
  if (!schedule) return;
  elements.content.innerHTML = `
    <div class="page-header">
      <div><span class="eyebrow">Recurring order</span><h1>${escapeHTML(schedule.items?.[0]?.sku || 'Schedule detail')}</h1><p class="lede mono">${escapeHTML(schedule.id)}</p></div>
      ${pill(schedule.status)}
    </div>
    <article class="card">
      <section class="card-section">
        <div class="section-heading"><div><h2>Schedule</h2><p>Order timing and checkout references</p></div></div>
        <dl class="facts">
          <div class="fact"><dt>Next order</dt><dd>${escapeHTML(formatDate(schedule.next_run_date))}</dd></div>
          <div class="fact"><dt>Interval</dt><dd>Every ${escapeHTML(schedule.interval_days)} days</dd></div>
          <div class="fact"><dt>Started</dt><dd>${escapeHTML(formatDate(schedule.anchor_date))}</dd></div>
          <div class="fact"><dt>Time zone</dt><dd>${escapeHTML(schedule.timezone)}</dd></div>
          <div class="fact"><dt>Origin order</dt><dd class="mono">${escapeHTML(schedule.origin_order_id)}</dd></div>
          <div class="fact"><dt>Recurring discount</dt><dd>${escapeHTML(schedule.discount_pct)}%</dd></div>
          ${schedule.paused_until ? `<div class="fact"><dt>Resumes</dt><dd>${escapeHTML(formatDate(schedule.paused_until))}</dd></div>` : ''}
        </dl>
      </section>
      <section class="card-section">
        <div class="section-heading"><div><h3>What ships each time</h3><p>SKU and quantity from the schedule API</p></div></div>
        <ul class="item-list">${schedule.items.map(item => `<li><span class="mono">${escapeHTML(item.sku)}</span><strong>Quantity ${escapeHTML(item.quantity)}</strong></li>`).join('')}</ul>
      </section>
      <section class="card-section">
        <div class="section-heading"><div><h3>Upcoming-occurrence timeline</h3><p>Scheduled, placed, skipped, and failed orders</p></div></div>
        ${occurrenceRows()}
      </section>
      <section class="card-section action-bar">${actionButtons(schedule)}</section>
    </article>`;
}

function openDialog({ eyebrow = 'Schedule action', title, body, submitLabel = 'Continue', submitVariant = 'primary', onSubmit }) {
  elements.dialogEyebrow.textContent = eyebrow;
  elements.dialogTitle.textContent = title;
  elements.dialogBody.innerHTML = body;
  elements.dialogSubmit.textContent = submitLabel;
  elements.dialogSubmit.className = `button ${submitVariant}`;
  elements.dialogSubmit.disabled = false;
  setDialogError();
  state.dialogSubmit = onSubmit;
  elements.dialog.showModal();
}

function openSettings() {
  openDialog({
    eyebrow: 'Development setup',
    title: 'API connection',
    submitLabel: 'Save and connect',
    body: `<p class="form-note">Credentials stay in this browser tab and are sent only to this CadenceOS origin. Use a customer JWT for reads and lifecycle changes; the service key is used only to create test schedules.</p>
      <div class="field-grid">
        <div class="field full"><label for="customer-id">Customer ID</label><input id="customer-id" name="customer_id" required value="${escapeHTML(state.config.customerId)}" autocomplete="off"></div>
        <div class="field full"><label for="portal-token">Customer portal token</label><input class="secret" id="portal-token" name="portal_token" required type="password" value="${escapeHTML(state.config.portalToken)}" autocomplete="off"><small>The JWT subject must match the customer ID.</small></div>
        <div class="field full"><label for="service-key">Service API key</label><input class="secret" id="service-key" name="service_key" type="password" value="${escapeHTML(state.config.serviceKey)}" autocomplete="off"><small>Optional until you create a schedule.</small></div>
      </div>`,
    onSubmit: async form => {
      saveConfig({
        customerId: form.customer_id.value.trim(),
        portalToken: form.portal_token.value.trim(),
        serviceKey: form.service_key.value.trim(),
      });
      elements.dialog.close();
      history.replaceState({}, '', '/console/');
      await loadSchedules();
    },
  });
}

function today() {
  return new Date().toISOString().slice(0, 10);
}

function openCreate() {
  openDialog({
    eyebrow: 'Test data',
    title: 'Create schedule',
    submitLabel: 'Create schedule',
    body: `<div class="field-grid">
      <div class="field"><label for="create-customer">Customer ID</label><input id="create-customer" name="customer_id" required value="${escapeHTML(state.config.customerId)}"></div>
      <div class="field"><label for="create-email">Customer email</label><input id="create-email" name="customer_email" required type="email" value="demo@example.com"></div>
      <div class="field"><label for="create-order">Origin order ID</label><input id="create-order" name="origin_order_id" required value="demo-${Date.now()}"></div>
      <div class="field"><label for="create-anchor">Anchor date</label><input id="create-anchor" name="anchor_date" required type="date" value="${today()}"></div>
      <div class="field"><label for="create-interval">Days between orders</label><input id="create-interval" name="interval_days" required type="number" min="7" max="180" value="30"></div>
      <div class="field"><label for="create-timezone">Time zone</label><input id="create-timezone" name="timezone" required value="${escapeHTML(Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')}"></div>
      <div class="field"><label for="create-payment">Payment token reference</label><input id="create-payment" name="payment_token_ref" value="demo-payment-token"></div>
      <div class="field"><label for="create-shipping">Shipping address ID</label><input id="create-shipping" name="shipping_address_id" value="demo-address"></div>
      <div class="field"><label for="create-discount">Recurring discount %</label><input id="create-discount" name="discount_pct" required type="number" min="0" max="100" step="0.01" value="0"></div>
      <div class="field full"><span class="field-label">Item</span><div class="item-fields"><input aria-label="SKU" name="sku" required placeholder="SKU" value="DEMO-SKU"><input aria-label="Quantity" name="quantity" required type="number" min="1" value="1"></div></div>
    </div>`,
    onSubmit: async form => {
      const customerId = form.customer_id.value.trim();
      if (customerId !== state.config.customerId) throw new Error('Customer ID must match the connected customer token. Update API connection first.');
      const schedule = await api('/schedules', {
        method: 'POST', credential: 'service', body: {
          customer_id: customerId,
          customer_email: form.customer_email.value.trim(),
          origin_order_id: form.origin_order_id.value.trim(),
          interval_days: Number(form.interval_days.value),
          anchor_date: form.anchor_date.value,
          timezone: form.timezone.value.trim(),
          payment_token_ref: form.payment_token_ref.value.trim(),
          shipping_address_id: form.shipping_address_id.value.trim(),
          discount_pct: Number(form.discount_pct.value),
          items: [{ sku: form.sku.value.trim(), quantity: Number(form.quantity.value) }],
        },
      });
      elements.dialog.close();
      showToast('Schedule created.');
      await loadSchedules({ render: false });
      navigate(pathForSchedule(schedule.id));
    },
  });
}

function openAction(action) {
  const schedule = state.schedule;
  if (!schedule) return;
  state.activeIntentKey = ['skip', 'defer'].includes(action) ? crypto.randomUUID() : null;

  const actions = {
    pause: {
      title: 'Pause recurring orders', submitLabel: 'Pause orders',
      body: `<p class="form-note">Nothing is charged while paused, and orders already scheduled are cleared.</p><div class="radio-stack">
        <label class="radio-option"><input type="radio" name="pause_mode" value="open" checked><span><strong>Pause until I say otherwise</strong><small>Resume from this console whenever you want to reorder.</small></span></label>
        <label class="radio-option"><input type="radio" name="pause_mode" value="date"><span><strong>Resume automatically on a date</strong><small>CadenceOS starts the schedule again that morning.</small></span></label>
      </div><div class="field spaced-field"><label for="pause-date">Resume date</label><input id="pause-date" name="paused_until" type="date" min="${today()}" disabled></div>`,
      setup: () => document.querySelectorAll('[name=pause_mode]').forEach(input => input.addEventListener('change', () => {
        const date = document.querySelector('#pause-date');
        date.disabled = input.value === 'open' && input.checked;
        date.required = !date.disabled;
      })),
      submit: form => postTransition('pause', form.pause_mode.value === 'date' ? { paused_until: form.paused_until.value } : undefined),
    },
    resume: {
      title: 'Resume recurring orders?', submitLabel: 'Resume now',
      body: `<p class="form-note">The first order is scheduled from the resume date and the ${escapeHTML(schedule.interval_days)}-day rhythm starts again from there.</p>`,
      submit: () => postTransition('resume'),
    },
    skip: {
      title: 'Skip the next order?', submitLabel: 'Skip this order',
      body: `<p class="form-note">You will not be charged for this one. The schedule keeps its existing rhythm.</p>`,
      submit: () => postTransition('skip', { idempotency_key: state.activeIntentKey }),
    },
    defer: {
      title: 'Push back just this order', submitLabel: 'Push back',
      body: `<p class="form-note">Move the next order later without changing the interval. Afterwards, the schedule returns to its normal rhythm.</p><div class="field"><label for="defer-days">Days to push back</label><input id="defer-days" name="days" required type="number" min="1" max="180" value="7"><small>Choose 1–180 days.</small></div>`,
      submit: form => postTransition('defer', { days: Number(form.days.value), idempotency_key: state.activeIntentKey }),
    },
    cadence: {
      title: 'How often should this repeat?', submitLabel: 'Save interval',
      body: `<p class="form-note">This changes every future order, not just the next one. Choose a gap from 7 to 180 days.</p><div class="field"><label for="cadence-days">Days between orders</label><input id="cadence-days" name="interval_days" required type="number" min="7" max="180" value="${escapeHTML(schedule.interval_days)}"></div>`,
      submit: form => postTransition('cadence', { interval_days: Number(form.interval_days.value) }),
    },
    cancel: {
      title: 'Cancel recurring orders?', submitLabel: 'Cancel recurring orders', submitVariant: 'danger',
      body: `<p class="form-note">Nothing further will be charged and scheduled orders are cleared. This ends only this schedule.</p><fieldset class="choice-fieldset"><legend class="field-label choice-legend">Why are you cancelling?</legend><div class="radio-stack">
        ${[
          ['too_frequent', 'Orders arrive closer together than I want'],
          ['too_expensive', 'It costs more than I want to spend'],
          ['no_longer_wanted', 'I do not want this product any more'],
          ['switched_brand', 'I am buying this somewhere else'],
          ['delivery_issue', 'There was a problem with delivery'],
          ['payment_issue', 'There was a problem with payment'],
          ['other', 'Something else'],
        ].map(([value, label], index) => `<label class="radio-option"><input type="radio" name="reason_code" value="${value}" ${index === 0 ? 'checked' : ''}><span>${label}</span></label>`).join('')}
      </div></fieldset>`,
      submit: form => postTransition('cancel', { reason_code: form.reason_code.value }),
    },
  };

  const config = actions[action];
  if (!config) return;
  openDialog({
    title: config.title,
    body: config.body,
    submitLabel: config.submitLabel,
    submitVariant: config.submitVariant,
    onSubmit: config.submit,
  });
  config.setup?.();
}

async function postTransition(action, body) {
  await api(`/schedules/${encodeURIComponent(state.schedule.id)}/${action}`, { method: 'POST', body });
  elements.dialog.close();
  state.activeIntentKey = null;
  showToast(`Schedule updated: ${action === 'cadence' ? 'interval changed' : action}.`);
  await Promise.all([loadSchedules({ render: false }), loadDetail(state.schedule.id)]);
}

function navigate(path, { replace = false } = {}) {
  if (replace) history.replaceState({}, '', path);
  else history.pushState({}, '', path);
  route();
}

async function route() {
  setGlobalError();
  const id = currentScheduleId();
  if (id) await loadDetail(id);
  else if (!state.config.customerId || !state.config.portalToken) renderWelcome();
  else if (!state.schedules.length) await loadSchedules();
  else navigate(pathForSchedule(state.schedules[0].id), { replace: true });
}

document.addEventListener('click', event => {
  const link = event.target.closest('[data-link]');
  if (link) {
    event.preventDefault();
    navigate(link.getAttribute('href'));
    return;
  }
  if (event.target.closest('[data-open-settings]')) openSettings();
  if (event.target.closest('[data-create]')) openCreate();
  const action = event.target.closest('[data-action]')?.dataset.action;
  if (action) openAction(action);
  if (event.target.closest('[data-retry]') && currentScheduleId()) loadDetail(currentScheduleId());
});

document.querySelector('#settings-button').addEventListener('click', openSettings);
document.querySelector('#create-button').addEventListener('click', openCreate);
document.querySelector('#refresh-button').addEventListener('click', async () => {
  await loadSchedules({ render: false });
  if (currentScheduleId()) await loadDetail(currentScheduleId());
});

elements.dialogForm.addEventListener('submit', async event => {
  event.preventDefault();
  if (event.submitter?.value === 'cancel') {
    elements.dialog.close();
    return;
  }
  if (!elements.dialogForm.reportValidity()) return;
  if (!state.dialogSubmit) return;

  elements.dialogSubmit.disabled = true;
  setDialogError();
  try {
    await state.dialogSubmit(elements.dialogForm.elements);
  } catch (error) {
    setDialogError(error.message || 'The request could not be completed.');
  } finally {
    elements.dialogSubmit.disabled = false;
  }
});

elements.dialog.addEventListener('close', () => {
  state.dialogSubmit = null;
  state.activeIntentKey = null;
});
window.addEventListener('popstate', route);

updateConnectionState();
loadSchedules({ render: false }).then(route);
