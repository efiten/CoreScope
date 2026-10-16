'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
// Uplink layer (rx-coverage.js): a cell is coloured by the best class of the
// pings sent there, and its tooltip lists the classes, the repeaters that
// picked a ping up, and how far it reached. Loaded in a vm like
// test-rx-coverage-mine.js.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const code = fs.readFileSync(path.join(REPO_ROOT, 'public', 'rx-coverage.js'), 'utf8');
const sandbox = {
  window: {},
  document: { getElementById: function () { return null; } },
  registerPage: function () {},
  console: { warn: function () {} },
  Promise: Promise,
  setTimeout: function () {}, clearTimeout: function () {},
  fetch: function () { throw new Error('no fetch expected'); },
  L: {}, getComputedStyle: function () { return { getPropertyValue: function () { return ''; } }; },
  escapeHtml: function (s) { return String(s).replace(/[&<>"']/g, function (c) { return '&#' + c.charCodeAt(0) + ';'; }); },
};
vm.createContext(sandbox);
vm.runInContext(code, sandbox);
const t = sandbox.window.CSRxCoverage._test;

assert.strictEqual(t.uplinkColorVar({ best: 'both' }), '--nq-up-both');
assert.strictEqual(t.uplinkColorVar({ best: 'observed' }), '--nq-up-observed');
assert.strictEqual(t.uplinkColorVar({ best: 'echo' }), '--nq-up-echo');
assert.strictEqual(t.uplinkColorVar({ best: 'none' }), '--nq-up-none');
assert.strictEqual(t.uplinkColorVar({ best: 'bogus' }), '--nq-up-none', 'unknown class falls back to none');
assert.strictEqual(t.uplinkColorVar(null), '--nq-up-none');

const html = t.uplinkCellHtml({ pings: 3, both: 1, observed: 0, echo: 1, none: 1, first_hops: ['c0cd', '1403'],
  max_observers: 17, max_hops: 11, last_at: '2026-10-16T14:51:15.018Z' });
assert.ok(html.includes('3 uplink pings'), html);
assert.ok(html.includes('1 both directions · 1 repeater only · 1 not heard'), html);
assert.ok(!html.includes('heard by observers'), 'zero classes are left out');
assert.ok(html.includes('picked up by: C0CD, 1403'), html);
assert.ok(html.includes('reached 17 observers, up to 11 hops'), html);
assert.ok(html.includes('2026-10-16 14:51 UTC'), html);
const one = t.uplinkCellHtml({ pings: 1, both: 0, observed: 0, echo: 0, none: 1, first_hops: ['<b>'], max_observers: 0, last_at: '' });
assert.ok(one.includes('1 uplink ping<') && !one.includes('reached'), one);
assert.ok(!one.includes('<b>'), 'first hops are escaped');
const named = t.uplinkCellHtml({ pings: 1, both: 1, observed: 0, echo: 0, none: 0, first_hops: ['c0cd', '1403'],
  first_hop_names: ['BE-ZHV-ON4AZP <x>', ''], max_observers: 0, last_at: '' });
assert.ok(named.includes('picked up by: BE-ZHV-ON4AZP &#60;x&#62;, 1403'), 'a resolved name replaces the prefix, escaped; an unresolved one keeps it: ' + named);

console.log('rx-coverage uplink layer OK');
