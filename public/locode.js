'use strict';
/* global window */

// Code keys only — descriptions live in locode.json under "types"
// so new types can be added without touching this file.
const TYPE_CODES = ['COR', 'DIS', 'EDG', 'MOB', 'EBP', 'ESP', 'EMP', 'RP'];

function parseLocodeName(name) {
  if (!name || typeof name !== 'string') return null;
  const stripped = name.split(' | ')[0].trim();
  const m = stripped.match(/^([A-Z]{2})-([A-Z]{3})-/);
  if (!m) return null;
  const cc = m[1];
  const loc = m[2];
  const segments = stripped.split('-');
  let type = null;
  for (const seg of segments.slice(2)) {
    const upper = seg.toUpperCase();
    for (const code of TYPE_CODES) {
      if (upper === code || (upper.startsWith(code) && /^\d+$/.test(upper.slice(code.length)))) {
        type = code;
        break;
      }
    }
    if (type) break;
  }
  return { cc, loc, type };
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

let _locodeData = null;
let _locodePromise = null;

function _ensureData() {
  if (_locodeData) return Promise.resolve();
  if (!_locodePromise) _locodePromise = fetch('/locode.json').then(r => r.json()).then(d => { _locodeData = d; });
  return _locodePromise;
}

function _buildTooltipContent(name) {
  if (!_locodeData) return null;
  const parsed = parseLocodeName(name);
  if (!parsed) return null;
  const { cc, loc, type } = parsed;
  const countryName = _locodeData.countries[cc];
  if (!countryName) return null;
  const cityName = (_locodeData.locations[cc] || {})[loc];
  if (!cityName) return null;
  let html = `<div class="lc-line1">${countryName} · ${cityName}</div>`;
  const typeLabel = type && _locodeData.types && _locodeData.types[type];
  if (typeLabel) {
    html += `<div class="lc-line2">${typeLabel}</div>`;
  }
  return html;
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
