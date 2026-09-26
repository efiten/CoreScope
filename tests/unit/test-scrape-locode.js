/* test-scrape-locode.js — FORK-LOCAL. The two pure halves of the locode refresh.
 *
 * scripts/scrape-locode.js rebuilds public/locode.json and public/locode-coords.json
 * from the UN/LOCODE CSV. Requiring it fetches nothing: main() is guarded by
 * require.main, so these run offline.
 *
 * Two things are worth pinning. The CSV reader has to survive quoted fields,
 * because place names carry commas and apostrophes and a naive split on commas
 * drops rows without saying so. And the coordinate conversion has to be exact,
 * because everything a GPS lookup would do rests on it.
 */
'use strict';

const assert = require('assert');
const fs = require('fs');
const path = require('path');
const { parseCsv, toDecimal, COUNTRIES, PREFER_NAME_STARTING } = require('../../scripts/scrape-locode.js');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

console.log('\n=== scrape-locode: the CSV reader ===');

test('plain rows split on commas', () => {
  const r = parseCsv('a,b,c\n1,2,3\n');
  assert.deepStrictEqual(r, [['a', 'b', 'c'], ['1', '2', '3']]);
});

test('a quoted field keeps its commas', () => {
  // Real shape: ,"Ferrières-Saint-Mary, Le",  — splitting on commas would make
  // this two fields and shift every later column, silently.
  const r = parseCsv('Country,Location,Name\nFR,ABC,"Ferrieres-Saint-Mary, Le"\n');
  assert.strictEqual(r[1][2], 'Ferrieres-Saint-Mary, Le');
  assert.strictEqual(r[1].length, 3, 'the row gained a field, so later columns would misalign');
});

test('a doubled quote inside a quoted field becomes one quote', () => {
  const r = parseCsv('x\n"say ""hi"""\n');
  assert.strictEqual(r[1][0], 'say "hi"');
});

test('CRLF line endings do not leak into the last field', () => {
  const r = parseCsv('a,b\r\n1,2\r\n');
  assert.deepStrictEqual(r[1], ['1', '2'], 'a stray \\r would end up inside the value');
});

test('a final row without a trailing newline is not dropped', () => {
  const r = parseCsv('a,b\n1,2');
  assert.strictEqual(r.length, 2, 'the last row was lost');
});

test('an apostrophe needs no quoting and survives', () => {
  // "Sint-Job-in-'t-Goor" and similar are common in the Belgian rows.
  const r = parseCsv("a\nSint-Job-in-'t-Goor\n");
  assert.strictEqual(r[1][0], "Sint-Job-in-'t-Goor");
});

console.log('\n=== scrape-locode: coordinates ===');

test('a northern, eastern coordinate converts', () => {
  // Antwerpen, BE ANR. 51 degrees 13 minutes north, 4 degrees 25 minutes east.
  assert.deepStrictEqual(toDecimal('5113N 00425E'), [51.2167, 4.4167]);
});

test('south and west are negative', () => {
  assert.deepStrictEqual(toDecimal('3352S 01827E'), [-33.8667, 18.45]);
  assert.deepStrictEqual(toDecimal('4030N 07400W'), [40.5, -74]);
});

test('minutes are sixtieths, not decimals', () => {
  // The trap this guards: reading "5130" as 51.30 rather than 51.5 puts a place
  // 22 km from where it belongs, which is more than a municipality is wide.
  const [lat] = toDecimal('5130N 00000E');
  assert.strictEqual(lat, 51.5);
});

test('an absent or malformed coordinate yields null, never a zero', () => {
  // 35% of the Dutch rows have no coordinate. Rendering those at 0,0 would put
  // them in the Gulf of Guinea and a nearest-place lookup would believe it.
  for (const bad of ['', '   ', undefined, null, 'N/A', '5113N', '5113 00425', '51N 004E', '511300425']) {
    assert.strictEqual(toDecimal(bad), null, 'accepted ' + JSON.stringify(bad));
  }
});

console.log('\n=== scrape-locode: scope ===');

test('the country list is the five the fork resolves', () => {
  // A country added here needs a matching entry in locode.json's regions to be
  // useful, so the list is not incidental.
  assert.deepStrictEqual(Object.keys(COUNTRIES).sort(), ['BE', 'DE', 'FR', 'GB', 'NL']);
});

console.log('\n=== scrape-locode: the bilingual preference reached the shipped data ===');

// The source lists 22 codes twice, and which row wins is a preference rather than
// a correctness question. efite asked for the Dutch form first, so the check that
// matters is on the committed file: re-running the scraper without the table, or
// with a broken lookup, changes public/locode.json and fails here. No network.
const SHIPPED = JSON.parse(
  fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'locode.json'), 'utf8'));

test('every preferred name is the one in public/locode.json', () => {
  const wrong = [];
  for (const [key, want] of Object.entries(PREFER_NAME_STARTING)) {
    const [cc, code] = key.split('-');
    const got = ((SHIPPED.locations || {})[cc] || {})[code];
    if (got === undefined) { wrong.push(key + ' is absent from the shipped data'); continue; }
    if (!got.startsWith(want)) {
      wrong.push(key + ': wanted a name starting ' + JSON.stringify(want) + ', shipped ' + JSON.stringify(got));
    }
  }
  assert.deepStrictEqual(wrong, [], 'the shipped data does not match the preference table');
});

test('the preference table is shaped as CC-CODE to a non-empty name', () => {
  for (const [key, want] of Object.entries(PREFER_NAME_STARTING)) {
    assert.ok(/^[A-Z]{2}-[A-Z0-9]{2,3}$/.test(key), 'bad key: ' + key);
    assert.ok(typeof want === 'string' && want.length > 1, 'bad value for ' + key + ': ' + JSON.stringify(want));
  }
});

test('Dutch-first is what actually landed, on codes a reader would notice', () => {
  // Full names rather than prefixes, so a lookup that happened to satisfy every
  // startsWith above still shows up here if it picked the wrong row.
  const be = SHIPPED.locations.BE;
  assert.strictEqual(be.BRU, 'Brussel (Bruxelles)');
  assert.strictEqual(be.TRN, 'Doornik (Tournai)');
  assert.strictEqual(be.MOS, 'Moeskroen (Mouscron)');
  assert.strictEqual(be.LNY, 'Ternaaien (Lanaye)');
  assert.strictEqual(be.KAN, 'Kanne (Canne)');
});

console.log(`\nTotal: ${passed} passed, ${failed} failed`);
if (failed) process.exit(1);
