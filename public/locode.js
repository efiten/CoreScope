'use strict';
/* global window */

// Code keys only — descriptions live in locode.json under "types"
// so new types can be added without touching this file.
const TYPE_CODES = ['COR', 'DIS', 'EDG', 'MOB', 'EBP', 'ESP', 'EMP', 'RP'];

// Power-source emoji → key. Matched on the base codepoint so the U+FE0F
// variation selector (☀️ vs ☀) doesn't matter.
function parsePower(name) {
  if (name.indexOf('☀') !== -1) return 'solar';        // ☀
  if (name.indexOf('⚡') !== -1) return 'grid';         // ⚡
  if (name.indexOf('🔋') !== -1) return 'gridbattery'; // 🔋
  return null;
}

// HAM callsign anywhere in the name. Belgian: ON + digit + 2-3 letters.
// Dutch: P[ABCEFGHI] + digit + 2-3 letters (PD and other prefixes excluded by
// design). Tokenised on any non-alphanumeric (dash, space, '|', emoji) so the
// callsign is found wherever it sits — operator-ID position or " | suffix".
function findCallsign(name) {
  const tokens = name.toUpperCase().split(/[^A-Z0-9]+/);
  for (const t of tokens) {
    if (/^ON\d[A-Z]{2,3}$/.test(t)) return t;
    if (/^P[ABCEFGHI]\d[A-Z]{2,3}$/.test(t)) return t;
  }
  return null;
}

// radio-actief member / emergency operator-IDs: the token right after CC-LOC.
// Trailing non-alnum (e.g. an appended power emoji) is stripped before matching.
function parseOperatorId(seg) {
  if (!seg) return null;
  const s = seg.replace(/[^A-Za-z0-9].*$/, '').toUpperCase();
  let m;
  if ((m = s.match(/^RRY(\d+)$/))) return { kind: 'ra', num: m[1] };
  if (/^\dA\d+$/.test(s)) return { kind: 'emcomm', code: s };
  return null;
}

function parseLocodeName(name) {
  if (!name || typeof name !== 'string') return null;
  const stripped = name.split(' | ')[0].trim();
  // CC-LOC: country (2) + location code (2-3 letters — a UN/LOCODE city or an
  // ISO 3166-2 region). The separator after the code may be a dash, a space, or
  // the end of the name.
  const m = stripped.match(/^([A-Z]{2})-([A-Z]{2,3})(?=[-\s]|$)/);
  if (!m) return null;
  const cc = m[1];
  const loc = m[2];
  // Tokens after the location code; handles both '-' and ' ' separators.
  const restTokens = stripped.slice(m[0].length).split(/[^A-Za-z0-9]+/).filter(Boolean);
  let type = null;
  for (const seg of restTokens) {
    const upper = seg.toUpperCase();
    for (const code of TYPE_CODES) {
      if (upper === code || (upper.startsWith(code) && /^\d+$/.test(upper.slice(code.length)))) {
        type = code;
        break;
      }
    }
    if (type) break;
  }
  // radio-actief extras (independent of the LOCODE type code; both conventions
  // share the CC-LOC prefix and are used interchangeably). A HAM callsign may
  // appear anywhere; RRY/emergency IDs sit in the token right after CC-LOC.
  const call = findCallsign(name);
  const operator = call ? { kind: 'ham', call } : parseOperatorId(restTokens[0]);
  const power = parsePower(name);
  return { cc, loc, type, operator, power };
}

function locodeAttr(name) {
  if (!parseLocodeName(name)) return '';
  const safe = name
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
  return `data-locode="${safe}"`;
}

window.parseLocodeName = parseLocodeName;
window.locodeAttr = locodeAttr;
window.buildLocodeHtml = buildLocodeHtml;

let _locodeData = null;
let _locodePromise = null;

function _ensureData() {
  if (_locodeData) return Promise.resolve();
  if (!_locodePromise) _locodePromise = fetch('/locode.json').then(r => r.json()).then(d => { _locodeData = d; });
  return _locodePromise;
}

// buildLocodeHtml renders the tooltip from a name + the locode data object.
// Pure (no module state) so it is unit-testable. Each piece is shown only if it
// resolves; returns null when nothing does. Handles both the LOCODE type code
// and the radio-actief operator-ID / power-source convention.
function buildLocodeHtml(name, data) {
  if (!data) return null;
  const parsed = parseLocodeName(name);
  if (!parsed) return null;
  const { cc, loc, type, operator, power } = parsed;
  const lines = [];
  const countryName = (data.countries || {})[cc];
  // The location code resolves to an ISO 3166-2 region (e.g. a German Bundesland
  // like NW / NRW) or a UN/LOCODE city. Regions are checked first so an explicit
  // region code wins over a colliding city code (NRW is also the city Neuweier).
  const place = countryName && (((data.regions || {})[cc] || {})[loc] || ((data.locations || {})[cc] || {})[loc]);
  if (countryName && place) lines.push(`<div class="lc-line1">${countryName} · ${place}</div>`);
  const typeLabel = type && data.types && data.types[type];
  if (typeLabel) lines.push(`<div class="lc-line2">${typeLabel}</div>`);
  const ops = data.operators || {};
  if (operator) {
    let txt = null;
    if (operator.kind === 'ra' && ops.ra) txt = `${ops.ra} ${operator.num}`;
    else if (operator.kind === 'ham' && ops.ham) txt = `${ops.ham}: ${operator.call}`;
    else if (operator.kind === 'emcomm' && ops.emcomm) txt = ops.emcomm;
    if (txt) lines.push(`<div class="lc-line2">${txt}</div>`);
  }
  const powerLabel = power && data.power && data.power[power];
  if (powerLabel) lines.push(`<div class="lc-line2">${powerLabel}</div>`);
  return lines.length ? lines.join('') : null;
}

function _buildTooltipContent(name) {
  return buildLocodeHtml(name, _locodeData);
}

function initLocodeTooltips() {
  const tip = document.createElement('div');
  tip.id = 'locode-tooltip';
  tip.setAttribute('aria-hidden', 'true');
  document.body.appendChild(tip);

  let _active = null;

  document.addEventListener('mouseover', function(e) {
    const el = e.target.closest('[data-locode]');
    if (!el) { tip.style.display = 'none'; _active = null; return; }
    if (el === _active) return;
    _active = el;
    const name = el.getAttribute('data-locode');
    _ensureData().then(function() {
      const content = _buildTooltipContent(name);
      if (!content) { tip.style.display = 'none'; return; }
      tip.innerHTML = content;
      tip.style.display = 'block';
    });
  });

  document.addEventListener('mouseout', function(e) {
    if (e.target.closest('[data-locode]')) {
      tip.style.display = 'none';
      _active = null;
    }
  });

  document.addEventListener('mousemove', function(e) {
    if (tip.style.display !== 'block') return;
    const w = tip.offsetWidth, h = tip.offsetHeight;
    let x = e.clientX + 14, y = e.clientY + 14;
    if (x + w > window.innerWidth) x = e.clientX - w - 14;
    if (y + h > window.innerHeight) y = e.clientY - h - 14;
    tip.style.left = x + 'px';
    tip.style.top  = y + 'px';
  });
}

window.initLocodeTooltips = initLocodeTooltips;
initLocodeTooltips();
