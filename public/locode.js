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
