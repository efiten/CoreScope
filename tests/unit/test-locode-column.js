/* test-locode-column.js — FORK-LOCAL. The Location column's resolution rules.
 *
 * public/locode-column.js turns a node name into a place, using the real
 * public/locode.json. This suite runs against that file rather than a fixture,
 * because the whole feature is a claim about that data: if a code stops
 * resolving, the column silently empties and a fixture would not notice.
 *
 * parseLocodeName is taken from the shipped public/locode.js by slicing out its
 * pure functions. The tail of that file calls initLocodeTooltips(), which needs a
 * DOM, so the whole file cannot be evaluated here. Same approach as
 * tests/unit/test-nav-drawer-version-footer.js.
 */
'use strict';

const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

let passed = 0, failed = 0;
// Async-aware on purpose: four cases below return promises, and a synchronous
// runner would count those as passed however they settled.
async function test(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

const LOCODE_SRC = fs.readFileSync(path.join(REPO_ROOT, 'public/locode.js'), 'utf8');
const COLUMN_SRC = fs.readFileSync(path.join(REPO_ROOT, 'public/locode-column.js'), 'utf8');
const DATA = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, 'public/locode.json'), 'utf8'));

// Slice the pure parsing half: from the type codes up to the export of
// buildLocodeHtml, which is the first line that refers to anything defined
// further down. If either marker moves this fails loudly rather than quietly
// testing nothing.
function sliceParser() {
  const start = LOCODE_SRC.indexOf('const TYPE_CODES');
  const end = LOCODE_SRC.indexOf('window.buildLocodeHtml');
  assert.ok(start !== -1, 'could not find "const TYPE_CODES" in public/locode.js');
  assert.ok(end !== -1 && end > start, 'could not find the "window.buildLocodeHtml" boundary');
  return LOCODE_SRC.slice(start, end);
}

function load(opts) {
  opts = opts || {};
  const sandbox = { window: {}, console: { warn() {}, error() {} } };
  vm.createContext(sandbox);
  if (!opts.withoutParser) {
    vm.runInContext(sliceParser(), sandbox);
    assert.ok(sandbox.window.parseLocodeName, 'the slice must define window.parseLocodeName');
  }
  if (opts.loader) sandbox.window.ensureLocodeData = opts.loader;
  vm.runInContext(COLUMN_SRC, sandbox);
  assert.ok(sandbox.window.LocodeColumn, 'locode-column.js must expose window.LocodeColumn');
  return sandbox.window.LocodeColumn;
}

(async () => {
  const LC = load();

  console.log('\n=== locode column: resolving a node name to a place ===');

  await test('a city code resolves, with its country', () => {
    const r = LC.placeOf('BE-ANR-ON8AR-REPEATER', DATA);
    assert.ok(r, 'BE-ANR did not resolve');
    assert.strictEqual(r.place, 'Antwerpen');
    assert.strictEqual(r.country, 'Belgium');
  });

  await test('an ISO 3166-2 region code resolves too, not just cities', () => {
    const r = LC.placeOf('DE-NW-SOME-REPEATER', DATA);
    assert.ok(r, 'DE-NW did not resolve');
    assert.strictEqual(r.place, 'Nordrhein-Westfalen');
  });

  await test('on a collision the region wins over the town', () => {
    // DE-NRW is the only code in locode.json that is both, and it is why the
    // lookup order is regions before locations. Assert the collision still
    // exists, so this cannot pass by the data changing underneath it.
    assert.strictEqual(DATA.regions.DE.NRW, 'Nordrhein-Westfalen', 'DE-NRW is no longer a region in locode.json');
    assert.strictEqual(DATA.locations.DE.NRW, 'Neuweier', 'DE-NRW is no longer also a town in locode.json');
    const r = LC.placeOf('DE-NRW-REPEATER', DATA);
    assert.strictEqual(r.place, 'Nordrhein-Westfalen',
      'the town won, so the column and locode.js\'s tooltip now disagree about the same node');
  });

  await test('a name without the convention resolves to nothing, not a guess', () => {
    for (const name of ['My Repeater', 'ON8AR', '', 'be-anr-lowercase', 'BELGIUM-ANR']) {
      assert.strictEqual(LC.placeOf(name, DATA), null, 'invented a place for ' + JSON.stringify(name));
    }
  });

  await test('a well-formed code that is not in the data resolves to nothing', () => {
    // Measured on live: 9 names parsed but did not resolve, each once. Those must
    // render empty rather than fall back to the country alone.
    assert.strictEqual(LC.placeOf('BE-ZZZ-REPEATER', DATA), null);
    assert.strictEqual(LC.placeOf('QQ-ANR-REPEATER', DATA), null, 'resolved a place under an unknown country');
  });

  await test('no data means no values, and no throw', () => {
    assert.strictEqual(LC.placeOf('BE-ANR-X', null), null);
  });

  console.log('\n=== the table cell contract ===');

  await test('the header carries the sort key table-sort.js needs', () => {
    const h = LC.headerHtml();
    assert.ok(/data-sort-key="place"/.test(h), 'no data-sort-key, so the column would not sort: ' + h);
    assert.ok(/<th\b/.test(h) && /<\/th>/.test(h), 'header must be a complete <th>');
  });

  await test('an unresolved name still yields a cell, with a defined sort value', () => {
    // A missing <td> would shift every later column left by one on that row.
    const c = LC.cellHtml('My Repeater');
    assert.ok(/^<td\b/.test(c) && /<\/td>$/.test(c), 'not a complete cell: ' + c);
    assert.ok(/data-value=""/.test(c), 'sort value must be present and empty: ' + c);
  });

  await test('a resolved cell carries the place as both text and sort value', async () => {
    const LC2 = load({ loader: () => Promise.resolve(DATA) });
    await LC2.prime();
    const c = LC2.cellHtml('BE-ANR-ON8AR');
    assert.ok(/data-value="Antwerpen"/.test(c), 'sort value missing: ' + c);
    assert.ok(/>Antwerpen</.test(c), 'place not rendered: ' + c);
    assert.ok(/title="Belgium/.test(c), 'country belongs in the tooltip: ' + c);
  });

  await test('the node name never reaches the cell, which is why it is safe', async () => {
    // Node names are published by whoever owns the node, so the property worth
    // pinning is that none of that string is rendered: the cell holds only the
    // place from locode.json, the country, and the cc/loc the regex matched.
    //
    // An earlier version of this test asserted that no <img> appeared in the
    // output, which could never fail for exactly that reason. Mutation-testing it
    // is what exposed the tautology. This version fails the moment someone
    // renders the name, for instance as a fallback for an unresolved node.
    const LC2 = load({ loader: () => Promise.resolve(DATA) });
    await LC2.prime();
    const marker = '"><img src=x onerror=alert(1)>';
    const c = LC2.cellHtml('BE-ANR-' + marker);
    assert.strictEqual(c, '<td class="lc-place" data-value="Antwerpen" title="Belgium · BE-ANR">Antwerpen</td>',
      'the cell is no longer exactly place plus country: ' + c);
    for (const fragment of ['img', 'onerror', 'alert', marker]) {
      assert.ok(!c.includes(fragment), 'part of the node name reached the cell (' + fragment + '): ' + c);
    }
  });

  await test('escaping still guards the one string that is not regex-bounded', async () => {
    // The place name comes from locode.json, which is ours, so this is defence
    // in depth rather than a live threat. It is cheap and it is the only value in
    // the cell that is free-form, so it is pinned.
    const LC2 = load({ loader: () => Promise.resolve({ countries: { XX: 'Country' }, locations: { XX: { AAA: 'A"><img src=x>B' } } }) });
    await LC2.prime();
    const c = LC2.cellHtml('XX-AAA-NODE');
    assert.ok(!/<img/.test(c), 'a place name with markup was rendered raw: ' + c);
    assert.ok(/&quot;&gt;&lt;img/.test(c), 'expected the place name escaped: ' + c);
  });

  await test('prime never rejects, even when locode.json does not load', async () => {
    // It is awaited next to the page's own fetch. A rejection here would render
    // "Failed to load scope audit" for a page that loaded fine.
    const LC3 = load({ loader: () => Promise.reject(new Error('offline')) });
    const d = await LC3.prime();
    assert.strictEqual(d, null, 'a failed load must resolve to null');
    assert.strictEqual(LC3.cellHtml('BE-ANR-X'), '<td class="lc-place" data-value=""></td>',
      'with no data the cell must be empty rather than throwing');
  });

  await test('prime with no loader at all is survivable', async () => {
    // locode.js is what defines ensureLocodeData. If the two files ever ship
    // apart, the column empties and the page still works.
    const LC4 = load({ withoutParser: true });
    assert.strictEqual(await LC4.prime(), null);
  });

  console.log(`\nTotal: ${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
})();
