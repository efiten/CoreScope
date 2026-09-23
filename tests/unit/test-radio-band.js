/* test-radio-band.js — band derivation from the observer's radio config.
 *
 * public/radio-band.js turns the observer-published "freq,bw,sf,cr" string
 * into a band label. Two properties matter more than the happy path and are
 * pinned hardest here:
 *
 *   - an unparseable or out-of-band frequency yields null, NOT a default band.
 *     Roughly 4% of observations on the reference deployment come from
 *     observers that publish no radio config at all, and rendering those as
 *     868 would be inventing data.
 *   - the string is publisher-controlled, so nothing it contains may reach the
 *     DOM unescaped.
 */
'use strict';

const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

function load() {
  const sandbox = { window: {}, console };
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(path.join(REPO_ROOT, 'public/radio-band.js'), 'utf8'), sandbox);
  assert.ok(sandbox.window.RadioBand, 'radio-band.js must expose window.RadioBand');
  return sandbox.window.RadioBand;
}

const RB = load();

console.log('\n=== radio-band: deriving the RF band from an observer ===');

test('parses a real observer string', () => {
  const r = RB.parseRadio('869.6179809,62.5,8,8');
  assert.strictEqual(r.freqMHz, 869.6179809);
  assert.strictEqual(r.bwKHz, 62.5);
  assert.strictEqual(r.sf, 8);
  assert.strictEqual(r.cr, 8);
});

test('the three bands map from real deployment values', () => {
  // Measured on live: 869.6179809 and 433.65. 915.0 is the firmware default
  // in the examples (LORA_FREQ 915.0); the EU variants build 869.618.
  assert.strictEqual(RB.bandOfRadio('869.6179809,62.5,8,8'), '868');
  assert.strictEqual(RB.bandOfRadio('433.65,62.5,8,8'), '433');
  assert.strictEqual(RB.bandOfRadio('915.0,250,7,5'), '915');
});

test('an unknown frequency is null, not a default band', () => {
  assert.strictEqual(RB.bandOf(2400), null, '2.4 GHz is not one of ours');
  assert.strictEqual(RB.bandOf(500), null, 'between the allocations');
  assert.strictEqual(RB.bandOf(0), null);
});

test('missing or malformed radio config yields null rather than a guess', () => {
  assert.strictEqual(RB.parseRadio(''), null);
  assert.strictEqual(RB.parseRadio(null), null);
  assert.strictEqual(RB.parseRadio(undefined), null);
  assert.strictEqual(RB.bandOfRadio(''), null);
  assert.strictEqual(RB.bandOfRadio('not,a,radio,string'), null);
  assert.strictEqual(RB.bandOfRadio('NaN,62.5,8,8'), null);
});

test('a partial string still yields a band when the frequency is usable', () => {
  // Observers have published short strings before; the band is the only part
  // this feature needs, so a missing sf/cr must not discard it.
  assert.strictEqual(RB.bandOfRadio('869.618'), '868');
  const r = RB.parseRadio('869.618');
  assert.strictEqual(r.bwKHz, null);
  assert.strictEqual(r.sf, null);
});

test('no badge is rendered for an unknown band', () => {
  assert.strictEqual(RB.badgeHtml(''), '');
  assert.strictEqual(RB.badgeHtml('2400,62.5,8,8'), '');
  assert.strictEqual(RB.badgeHtml(null), '');
});

test('the badge carries the band and a styling hook', () => {
  const html = RB.badgeHtml('433.65,62.5,8,8');
  assert.ok(/>433</.test(html), 'label: ' + html);
  assert.ok(/band-433/.test(html), 'per-band class so colouring later is CSS-only: ' + html);
  assert.ok(/badge-iata/.test(html), 'reuses the existing pill rather than adding CSS: ' + html);
});

test('a hostile radio string cannot inject markup', () => {
  // The frequency is parsed as a number before it reaches the title, so this
  // can only ever be the numeric prefix — but assert it, because the field is
  // publisher-controlled and this is the only place it is rendered.
  const html = RB.badgeHtml('433.65" onmouseover=alert(1) x="');
  assert.ok(html.indexOf('onmouseover') === -1, 'no attribute injection: ' + html);
  assert.ok(html.indexOf('<script') === -1);
});

// --- decorateObservers, against a minimal DOM ------------------------------

function fakeRow(id, hasNameCell) {
  const cell = { html: '', insertAdjacentHTML(_pos, s) { this.html += s; } };
  return {
    _id: id,
    _cell: hasNameCell ? cell : null,
    _badge: null,
    getAttribute(n) { return n === 'data-observer-id' ? id : null; },
    querySelector(sel) {
      if (sel === '.band-badge') return this._badge;
      if (sel === '[data-testid="obs-cell-name"]') return this._cell;
      return null;
    },
  };
}
function fakeRoot(rows) {
  return { querySelectorAll(sel) { return sel === 'tr[data-observer-id]' ? rows : []; } };
}

test('decorates one row per observer that publishes a band', () => {
  const rows = [fakeRow('a', true), fakeRow('b', true), fakeRow('c', true)];
  const n = RB.decorateObservers(fakeRoot(rows), [
    { id: 'a', radio: '869.618,62.5,8,8' },
    { id: 'b', radio: '433.65,62.5,8,8' },
    { id: 'c', radio: '' }, // publishes nothing: no badge, no guess
  ]);
  assert.strictEqual(n, 2);
  assert.ok(/>868</.test(rows[0]._cell.html));
  assert.ok(/>433</.test(rows[1]._cell.html));
  assert.strictEqual(rows[2]._cell.html, '', 'an observer without radio config gets no badge');
});

test('is idempotent, so a re-render cannot double up badges', () => {
  const row = fakeRow('a', true);
  const root = fakeRoot([row]);
  const data = [{ id: 'a', radio: '869.618,62.5,8,8' }];
  assert.strictEqual(RB.decorateObservers(root, data), 1);
  row._badge = {}; // the badge is now in the DOM
  assert.strictEqual(RB.decorateObservers(root, data), 0, 'second pass must do nothing');
});

test('missing hooks report zero rather than throwing', () => {
  // If upstream renames data-testid or drops data-observer-id, this feature
  // must go quiet, not break the observers page. The return value is what
  // makes that distinguishable from "no observers".
  const rows = [fakeRow('a', false)];
  assert.strictEqual(RB.decorateObservers(fakeRoot(rows), [{ id: 'a', radio: '869.618,62.5,8,8' }]), 0);
  assert.strictEqual(RB.decorateObservers(null, [{ id: 'a' }]), 0);
  assert.strictEqual(RB.decorateObservers(fakeRoot([]), []), 0);
});

console.log(`\nTotal: ${passed} passed, ${failed} failed`);
if (failed) process.exit(1);
