/* Fork-local (meshmap owner): unit tests for public/meshmap-owner.js, loaded
 * into a vm sandbox with the real escapeHtml from app.js. */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ROOT = path.resolve(__dirname, '..', '..');
let passed = 0, failed = 0;
const pending = [];
function test(name, fn) {
  const p = Promise.resolve().then(fn).then(
    () => { passed++; console.log('  ok   ' + name); },
    (e) => { failed++; console.log('  FAIL ' + name + ': ' + e.message); });
  pending.push(p);
  return p;
}
const src = (f) => fs.readFileSync(path.join(ROOT, f), 'utf8');
const PK = 'ef'.repeat(32);
const SOURCE = 'https://meshmap.radio-actief.be';

function loadEscapeHtml() {
  const m = src('public/app.js').match(/function escapeHtml\(s\) \{[\s\S]*?\n\}/);
  assert(m, 'escapeHtml not found in app.js');
  return vm.runInNewContext('(' + m[0].replace(/^function escapeHtml/, 'function') + ')');
}

function load(answer) {
  const calls = [];
  const CSApi = {
    fetch(p) {
      calls.push(p);
      if (answer instanceof Error) return Promise.reject(answer);
      return Promise.resolve({ ok: answer.ok !== false, json: () => Promise.resolve(answer.body) });
    },
  };
  const ctx = { CSApi, console, escapeHtml: loadEscapeHtml() };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(src('public/meshmap-owner.js'), ctx);
  return { M: ctx.CSMeshmapOwner, calls };
}
const feed = (nodes) => ({ body: { nodes, fetchedAt: '2026-11-17T10:00:00Z', source: SOURCE } });
const node = (owner, managers) => ({ pubkey: PK, owner: owner == null ? null : { name: owner }, managers: (managers || []).map((n) => ({ name: n })) });

test('owner row: label, @name and a safe meshmap link', async () => {
  const l = load(feed([node('erwinfiten')]));
  await l.M.ready();
  const h = l.M.rowHtml({ public_key: PK, lat: 51.1378, lon: 5.5902 });
  assert(h.includes('<dt>Owner</dt>'), h);
  assert(h.includes('@erwinfiten'), h);
  assert(h.includes('View on #BEmesh map'), h);
  assert(h.includes('target="_blank"') && h.includes('rel="noopener noreferrer"'), h);
  assert(!h.includes('Managers'), 'no managers row without managers: ' + h);
});

test('hidden owner name shows "claimed" without a name', async () => {
  const l = load(feed([node('')]));
  await l.M.ready();
  const h = l.M.rowHtml({ public_key: PK });
  assert(h.includes('<dt>Owner</dt>') && h.includes('claimed'), h);
  assert(!h.includes('@'), h);
  assert(h.includes('View on #BEmesh map'), h);
});

test('managers row lists named managers and counts hidden ones', async () => {
  const l = load(feed([node('erwinfiten', ['amy', 'zed', ''])]));
  await l.M.ready();
  const h = l.M.rowHtml({ public_key: PK });
  assert(h.includes('<dt>Managers</dt>'), h);
  assert(h.includes('@amy, @zed'), h);
  assert(h.includes('1 with a hidden name'), h);
  assert.strictEqual(h.split('View on #BEmesh map').length - 1, 1, 'one meshmap link: ' + h);
});

test('only managers, no owner: managers row carries the link', async () => {
  const l = load(feed([node(null, ['amy'])]));
  await l.M.ready();
  const h = l.M.rowHtml({ public_key: PK });
  assert(!h.includes('<dt>Owner</dt>'), h);
  assert(h.includes('<dt>Managers</dt>') && h.includes('@amy') && h.includes('View on #BEmesh map'), h);
});

test('names are escaped', async () => {
  const l = load(feed([node('<img src=x onerror=1>', ['<b>m</b>'])]));
  await l.M.ready();
  const h = l.M.rowHtml({ public_key: PK });
  assert(!h.includes('<img') && h.includes('&lt;img'), h);
  assert(!h.includes('<b>') && h.includes('&lt;b&gt;'), h);
});

const DISCORD = 'https://discord.com/users/123456789012345678';

test('a named person with a Discord profile gets their name as a link to it', async () => {
  const l = load(feed([{ pubkey: PK, owner: { name: 'erwinfiten', discord: DISCORD },
    managers: [{ name: 'amy', discord: 'https://discord.com/users/223456789012345678' }, { name: 'zed', discord: '' }] }]));
  await l.M.ready();
  const h = l.M.rowHtml({ public_key: PK });
  assert(h.includes('<a href="' + DISCORD + '" target="_blank" rel="noopener noreferrer" title="Message on Discord">@erwinfiten</a>'), h);
  assert(h.includes('users/223456789012345678" target="_blank"') && h.includes('@amy</a>, @zed'), h);
});

test('only a discord.com/users address becomes a link', async () => {
  const bad = ['javascript:alert(1)', 'https://evil.example/users/123456789012345678',
    DISCORD + '"><img src=x>', 'http://discord.com/users/123456789012345678', 'https://discord.com/users/abc'];
  for (const url of bad) {
    const l = load(feed([{ pubkey: PK, owner: { name: 'erwinfiten', discord: url }, managers: [] }]));
    await l.M.ready();
    const h = l.M.rowHtml({ public_key: PK });
    assert(h.includes('@erwinfiten') && !h.includes('Message on Discord') && !h.includes('<img'), url + ': ' + h);
  }
});

test('unknown pubkey gives no row; an uppercase node key still matches', async () => {
  const l = load(feed([node('erwinfiten')]));
  await l.M.ready();
  assert.strictEqual(l.M.rowHtml({ public_key: 'cd'.repeat(32) }), '');
  assert(l.M.rowHtml({ public_key: PK.toUpperCase() }).includes('@erwinfiten'));
});

test('link uses the source from the API, with and without a position', () => {
  const T = load(feed([])).M._test;
  assert.strictEqual(T.linkFor(SOURCE, { public_key: PK, lat: 51.1378, lon: 5.5902 }),
    SOURCE + '/?zoom=15&lat=51.1378&lon=5.5902&node=' + PK);
  assert.strictEqual(T.linkFor(SOURCE, { public_key: PK }), SOURCE + '/?node=' + PK);
  assert.strictEqual(T.linkFor(SOURCE, { public_key: PK, lat: 0, lon: 0 }), SOURCE + '/?node=' + PK,
    '0,0 is "no position" in CoreScope');
});

test('a node that left the feed has no row', async () => {
  const l = load(feed([]));
  await l.M.ready();
  assert.strictEqual(l.M.rowHtml({ public_key: PK }), '');
});

test('ready() fetches once after a good answer', async () => {
  const l = load(feed([node('erwinfiten')]));
  await Promise.all([l.M.ready(), l.M.ready()]);
  await l.M.ready();
  assert.strictEqual(l.calls.length, 1);
  assert.strictEqual(l.calls[0], '/api/meshmap/owners');
});

// Review finding: one transient failure must not hide owners for the whole SPA session.
test('a failed fetch resolves, shows no row, and the next ready() tries again', async () => {
  const l = load(new Error('offline'));
  await l.M.ready();
  assert.strictEqual(l.M.rowHtml({ public_key: PK }), '');
  await l.M.ready();
  assert.strictEqual(l.calls.length, 2);
  const off = load({ ok: false, body: {} });
  await off.M.ready();
  assert.strictEqual(off.M.rowHtml({ public_key: PK }), '');
});

test('an answer from before the first server fetch (fetchedAt null) is retried', async () => {
  const l = load({ body: { nodes: [], fetchedAt: null, source: SOURCE } });
  await l.M.ready();
  await l.M.ready();
  assert.strictEqual(l.calls.length, 2);
});

Promise.all(pending).then(() => {
  console.log(passed + ' passed, ' + failed + ' failed');
  if (failed) process.exit(1);
});
