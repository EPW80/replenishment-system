/*
 * The portal component set: status pill, button, choice chip, card/sheet, inset panel,
 * field, occurrence row, notice band. components.md's inventory, built once so screens
 * compose them rather than restating their markup.
 *
 * The artboards are drawn with inline styles because that is what the design canvas
 * edits; components.md says not to port them that way, so styling lives in portal.css
 * and these factories only produce structure.
 */

/** Minimal element factory. `props` sets properties (className, textContent, onclick),
 *  except `dataset` and `attrs`, which are spread onto their namesakes. */
export function el(tag, props = {}, children = []) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props)) {
    if (value === undefined || value === null) continue;
    if (key === 'dataset') Object.assign(node.dataset, value);
    else if (key === 'attrs') {
      for (const [name, v] of Object.entries(value)) {
        if (v !== undefined && v !== null) node.setAttribute(name, v);
      }
    } else node[key] = value;
  }
  for (const child of [].concat(children)) {
    if (child === undefined || child === null || child === false) continue;
    node.append(child);
  }
  return node;
}

const SVG_NS = 'http://www.w3.org/2000/svg';

/** Icons are inline SVG paths rather than a font or sprite sheet: eight glyphs do not
 *  justify a second network request, and the widget cannot assume the host theme's. */
const ICON_PATHS = {
  rotate: ['M20 12a8 8 0 1 1-2.34-5.66', 'M20 4v4h-4'],
  pause: null, // drawn as two rects below
  check: ['M20 6 9 17l-5-5'],
  cross: ['M18 6 6 18', 'M6 6l12 12'],
  alert: ['M12 9v4', 'M12 17h.01', 'M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L14.7 3.9a2 2 0 0 0-3.4 0z'],
};

export function icon(name, { size = 16, className = '' } = {}) {
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('width', size);
  svg.setAttribute('height', size);
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('fill', 'none');
  svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '1.6');
  svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round');
  svg.setAttribute('aria-hidden', 'true');
  if (className) svg.setAttribute('class', className);

  if (name === 'pause') {
    for (const x of [7, 13.5]) {
      const rect = document.createElementNS(SVG_NS, 'rect');
      rect.setAttribute('x', x);
      rect.setAttribute('y', '5');
      rect.setAttribute('width', '3.5');
      rect.setAttribute('height', '14');
      rect.setAttribute('rx', '1');
      svg.append(rect);
    }
    return svg;
  }

  for (const d of ICON_PATHS[name] ?? []) {
    const path = document.createElementNS(SVG_NS, 'path');
    path.setAttribute('d', d);
    svg.append(path);
  }
  return svg;
}

/* ---------------------------------------------------------------------------
 * Status pill
 * ------------------------------------------------------------------------ */

/** The dot is redundant with the label deliberately -- color never carries state
 *  alone. Already-sent rows swap the dot for a check or cross (components.md). */
export function pill({ label, variant = 'idle', icon: iconName }) {
  const mark = iconName
    ? icon(iconName, { size: 12, className: 'cad-pill__icon' })
    : el('span', { className: 'cad-pill__dot' });
  return el('span', { className: `cad-pill cad-pill--${variant}` }, [mark, label]);
}

/* ---------------------------------------------------------------------------
 * Button
 * ------------------------------------------------------------------------ */

export function button({
  label,
  variant = 'secondary',
  icon: iconName,
  onClick,
  type = 'button',
  disabled = false,
}) {
  const classes = ['cad-btn'];
  if (variant !== 'secondary') classes.push(`cad-btn--${variant}`);
  return el(
    'button',
    { className: classes.join(' '), type, disabled, onclick: onClick },
    [iconName && icon(iconName, { className: 'cad-btn__icon' }), el('span', { textContent: label })],
  );
}

/* ---------------------------------------------------------------------------
 * Choice chip
 * ------------------------------------------------------------------------ */

/** The check's space is reserved in the unselected state so selecting a chip does not
 *  reflow the row. */
export function chip({ label, selected = false, onClick }) {
  return el(
    'button',
    {
      className: 'cad-chip',
      type: 'button',
      onclick: onClick,
      attrs: { 'aria-pressed': String(selected) },
    },
    [icon('check', { size: 14, className: 'cad-chip__check' }), el('span', { textContent: label })],
  );
}

/* ---------------------------------------------------------------------------
 * Card and sheet
 *
 * The inset panel is the one component in components.md this screen has no use
 * for -- it carries consequence copy inside the cadence, skip and cancel sheets.
 * It arrives with those screens rather than sitting here unused.
 * ------------------------------------------------------------------------ */

export function card(children, { className = '' } = {}) {
  return el('div', { className: `cad-card ${className}`.trim() }, children);
}

export function section(children, { className = '' } = {}) {
  return el('div', { className: `cad-section ${className}`.trim() }, children);
}

/* ---------------------------------------------------------------------------
 * Field
 * ------------------------------------------------------------------------ */

/** `placeholder` renders the value muted, so an unresolved product name or address
 *  reads as pending rather than as content (components.md). */
export function field({ label, value, placeholder = false, accent = false, extra }) {
  const classes = ['cad-field__value'];
  if (placeholder) classes.push('cad-field__value--placeholder');
  if (accent) classes.push('cad-field__value--accent');
  return el('div', { className: 'cad-field' }, [
    el('div', { className: 'cad-field__label', textContent: label }),
    el('div', { className: classes.join(' ') }, [value, extra]),
  ]);
}

/* ---------------------------------------------------------------------------
 * Notice band
 * ------------------------------------------------------------------------ */

/** Two bands never stack: if a schedule is both armed and carrying a failure, the
 *  failure wins, because it is the one the customer can act on (components.md). */
export function band({ variant, heading, text, action }) {
  return el('div', { className: `cad-band cad-band--${variant}`, attrs: { role: 'status' } }, [
    icon('alert', { size: 20, className: 'cad-band__icon' }),
    el('div', { className: 'cad-band__body' }, [
      el('div', { className: 'cad-band__heading', textContent: heading }),
      text && el('div', { className: 'cad-band__text', textContent: text }),
    ]),
    action && el('div', { className: 'cad-band__actions' }, action),
  ]);
}

/* ---------------------------------------------------------------------------
 * Occurrence row
 * ------------------------------------------------------------------------ */

/** The action column keeps its width on rows that have none, so the pills stay on one
 *  vertical line down the list. */
export function occurrenceRow({ sequenceNo, date, dateNarrow, meta, status, actions = [] }) {
  return el('div', { className: 'cad-occ' }, [
    el('span', { className: 'cad-occ__seq', textContent: `#${sequenceNo}` }),
    el('div', { className: 'cad-occ__when' }, [
      el('span', {
        className: dateNarrow ? 'cad-occ__date cad-wide-only' : 'cad-occ__date',
        textContent: date,
      }),
      dateNarrow &&
        el('span', { className: 'cad-occ__date cad-narrow-only', textContent: dateNarrow }),
      meta && el('span', { className: 'cad-occ__meta', textContent: meta }),
    ]),
    pill(status),
    el('div', { className: 'cad-occ__actions' }, actions),
  ]);
}

/* ---------------------------------------------------------------------------
 * Item row
 * ------------------------------------------------------------------------ */

/** items[] carries sku and quantity only, by design -- WooCommerce owns the catalog
 *  (spec §1). A name that does not resolve is not an error; the SKU and quantity are
 *  enough to render the row. */
export function itemRow({ sku, quantity, name }) {
  return el('div', { className: 'cad-item' }, [
    el('span', { className: 'cad-item__qty', textContent: `${quantity} ×` }),
    el('span', { className: 'cad-item__sku', textContent: sku }),
    el('span', { className: 'cad-item__name', textContent: name ?? '[Product name]' }),
  ]);
}

/* ---------------------------------------------------------------------------
 * Skeleton
 * ------------------------------------------------------------------------ */

export function skeleton(kind = 'line', { width } = {}) {
  const node = el('div', { className: `cad-skel cad-skel--${kind}` });
  if (width) node.style.width = width;
  return node;
}
