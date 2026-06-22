'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;

function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

function makeSandbox() {
  const ctx = {
    window: {}, document: {
      createElement: () => ({ id: '', setAttribute: () => {}, style: {}, innerHTML: '' }),
      body: { appendChild: () => {} },
      addEventListener: () => {},
    },
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    console,
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  return ctx;
}

const ctx = makeSandbox();
vm.runInContext(fs.readFileSync('public/locode.js', 'utf8'), ctx);
const { parseLocodeName, locodeAttr, buildLocodeHtml } = ctx;

// Data fixture for buildLocodeHtml (mirrors locode.json shape)
const DATA = {
  countries: { BE: 'Belgium', NL: 'Netherlands', DE: 'Germany' },
  types: { EBP: 'Edge · Battery Powered', RP: 'Repeater' },
  locations: {
    BE: { GNE: 'Gent', VLD: 'Oedelem', BLZ: 'Bilzen', BGS: 'Brugge', ANT: 'Antwerpen', BRE: 'Bree' },
    NL: { RTM: 'Rotterdam', AMS: 'Amsterdam' },
  },
  regions: {
    DE: { NW: 'Nordrhein-Westfalen', NRW: 'Nordrhein-Westfalen', BY: 'Bayern' },
  },
  power: { solar: 'Solar + battery', grid: 'Grid only', gridbattery: 'Grid + battery backup' },
  operators: { ra: 'Radio-Actief member', ham: 'HAM callsign', emcomm: 'Emergency comms operator' },
};

// parseLocodeName
test('standard format: parses cc, loc, type', () => {
  const r = parseLocodeName('BE-BLZ-Tabaart-EBP-01');
  assert.strictEqual(r.cc, 'BE'); assert.strictEqual(r.loc, 'BLZ'); assert.strictEqual(r.type, 'EBP');
});
test('callsign suffix stripped before parse', () => {
  const r = parseLocodeName('BE-BRE-RP01 | ON8AR');
  assert.strictEqual(r.cc, 'BE'); assert.strictEqual(r.loc, 'BRE'); assert.strictEqual(r.type, 'RP');
});
test('type with digits appended (RP01) recognized', () => {
  const r = parseLocodeName('BE-BRE-RP01');
  assert.strictEqual(r.type, 'RP');
});
test('DIS type recognized', () => {
  assert.strictEqual(parseLocodeName('BE-ZOD-MT Zolder-DIS-01').type, 'DIS');
});
test('type absent returns null type', () => {
  const r = parseLocodeName('BE-BLZ-SomeNode');
  assert.ok(r); assert.strictEqual(r.type, null);
});
test('lowercase country code: returns null', () => {
  assert.strictEqual(parseLocodeName('be-BRE-test-EDG-01'), null);
});
test('no dash pattern: returns null', () => {
  assert.strictEqual(parseLocodeName('SomeRandomName'), null);
});
test('empty string: returns null', () => {
  assert.strictEqual(parseLocodeName(''), null);
});
test('null: returns null', () => {
  assert.strictEqual(parseLocodeName(null), null);
});
// locodeAttr
test('valid name produces data-locode attr', () => {
  const a = locodeAttr('BE-BLZ-Tabaart-EBP-01');
  assert.ok(a.includes('data-locode='), `got: ${a}`);
  assert.ok(a.includes('BE-BLZ-Tabaart-EBP-01'));
});
test('invalid name produces empty string', () => {
  assert.strictEqual(locodeAttr('SomeRandomName'), '');
  assert.strictEqual(locodeAttr(null), '');
});
test('locodeAttr escapes HTML special chars in name', () => {
  // Construct a name that parses but has special chars in callsign suffix
  const a = locodeAttr('BE-BLZ-Test-EDG-01 | <ON8AR>');
  assert.ok(!a.includes('<ON8AR>'), 'raw < not allowed in attribute');
  assert.ok(a.includes('&lt;ON8AR&gt;') || !a.includes('<'), 'must be escaped');
});

// --- radio-actief convention: operator-ID + power source ---
// parseLocodeName operator extraction
test('radio-actief RRY: operator kind=ra with member number', () => {
  const r = parseLocodeName('BE-GNE-RRY01');
  assert.strictEqual(r.cc, 'BE'); assert.strictEqual(r.loc, 'GNE');
  assert.strictEqual(r.type, null);
  assert.strictEqual(r.operator.kind, 'ra'); assert.strictEqual(r.operator.num, '01');
});
test('radio-actief HAM callsign: operator kind=ham', () => {
  const r = parseLocodeName('BE-VLD-ON7MHZ');
  assert.strictEqual(r.operator.kind, 'ham'); assert.strictEqual(r.operator.call, 'ON7MHZ');
});
test('radio-actief emergency: operator kind=emcomm', () => {
  const r = parseLocodeName('BE-BGS-1A001');
  assert.strictEqual(r.operator.kind, 'emcomm'); assert.strictEqual(r.operator.code, '1A001');
});
// power source emoji
test('solar emoji -> power=solar (and operator still parsed)', () => {
  const r = parseLocodeName('BE-VLD-ON7MHZ☀️');
  assert.strictEqual(r.power, 'solar');
  assert.strictEqual(r.operator.kind, 'ham'); assert.strictEqual(r.operator.call, 'ON7MHZ');
});
test('grid-only emoji -> power=grid', () => {
  const r = parseLocodeName('BE-LGG-RRY01⚡️');
  assert.strictEqual(r.power, 'grid');
  assert.strictEqual(r.operator.kind, 'ra'); assert.strictEqual(r.operator.num, '01');
});
test('grid+battery emoji -> power=gridbattery', () => {
  assert.strictEqual(parseLocodeName('BE-GNE-RRY01🔋').power, 'gridbattery');
});
// coexistence: LOCODE names get no operator/power
test('LOCODE name: operator null, power null, type preserved', () => {
  const r = parseLocodeName('BE-BLZ-Tabaart-EBP-01');
  assert.strictEqual(r.type, 'EBP');
  assert.strictEqual(r.operator, null);
  assert.strictEqual(r.power, null);
});
test('plain LOCODE description not mistaken for operator', () => {
  assert.strictEqual(parseLocodeName('BE-BLZ-SomeNode').operator, null);
});

// HAM callsign recognition: Belgian ON + digit + 2-3 letters, Dutch P[ABCEFGHI] + digit + 2-3 letters
test('Belgian callsign ON + digit + 2 letters', () => {
  const r = parseLocodeName('BE-ANT-ON4AB');
  assert.strictEqual(r.operator.kind, 'ham'); assert.strictEqual(r.operator.call, 'ON4AB');
});
test('Dutch callsign PA + digit + 3 letters', () => {
  const r = parseLocodeName('NL-RTM-PA3ABC');
  assert.strictEqual(r.cc, 'NL'); assert.strictEqual(r.loc, 'RTM');
  assert.strictEqual(r.operator.kind, 'ham'); assert.strictEqual(r.operator.call, 'PA3ABC');
});
test('Dutch callsign PH + digit + 2 letters', () => {
  assert.strictEqual(parseLocodeName('NL-AMS-PH9XY').operator.call, 'PH9XY');
});
test('Dutch PD prefix is NOT recognized as callsign', () => {
  assert.strictEqual(parseLocodeName('NL-RTM-PD3ABC').operator, null);
});
// separator after LOCODE may be a space, not only a dash
test('space separator after LOC: RRY operator parsed', () => {
  const r = parseLocodeName('BE-GNE RRY01');
  assert.strictEqual(r.cc, 'BE'); assert.strictEqual(r.loc, 'GNE');
  assert.strictEqual(r.operator.kind, 'ra'); assert.strictEqual(r.operator.num, '01');
});
test('space separator after LOC: callsign parsed', () => {
  assert.strictEqual(parseLocodeName('BE-VLD ON7MHZ').operator.call, 'ON7MHZ');
});
// callsign anywhere in the name, including the " | " suffix on a LOCODE-typed name
test('callsign in pipe-suffix detected alongside LOCODE type', () => {
  const r = parseLocodeName('BE-BRE-RP01 | ON8AR');
  assert.strictEqual(r.type, 'RP');
  assert.strictEqual(r.operator.kind, 'ham'); assert.strictEqual(r.operator.call, 'ON8AR');
});

// region codes: 2-letter (ISO 3166-2) as well as 3-letter
test('2-letter region code DE-NW parses (loc=NW)', () => {
  const r = parseLocodeName('DE-NW-Cologne-EDG-01');
  assert.ok(r, 'should parse'); assert.strictEqual(r.cc, 'DE'); assert.strictEqual(r.loc, 'NW');
});
test('bare 2-letter region DE-NW parses', () => {
  const r = parseLocodeName('DE-NW');
  assert.ok(r); assert.strictEqual(r.loc, 'NW');
});

// buildLocodeHtml: full tooltip rendering against data
test('radio-actief full tooltip: country/city + member + power', () => {
  const h = buildLocodeHtml('BE-GNE-RRY01☀️', DATA);
  assert.ok(h.includes('Belgium · Gent'), `city: ${h}`);
  assert.ok(h.includes('Radio-Actief member 01'), `operator: ${h}`);
  assert.ok(h.includes('Solar + battery'), `power: ${h}`);
});
test('HAM tooltip shows callsign label', () => {
  assert.ok(buildLocodeHtml('BE-VLD-ON7MHZ', DATA).includes('HAM callsign: ON7MHZ'));
});
test('emergency tooltip shows emcomm label', () => {
  assert.ok(buildLocodeHtml('BE-BGS-1A001', DATA).includes('Emergency comms operator'));
});
test('LOCODE tooltip still renders country/city + type label', () => {
  const h = buildLocodeHtml('BE-BLZ-Tabaart-EBP-01', DATA);
  assert.ok(h.includes('Belgium · Bilzen'));
  assert.ok(h.includes('Edge · Battery Powered'));
});
test('unknown city but operator present: still renders operator line', () => {
  const h = buildLocodeHtml('BE-XXX-RRY09', DATA);
  assert.ok(h && h.includes('Radio-Actief member 09'), `got: ${h}`);
});
test('Dutch tooltip: country/city + callsign', () => {
  const h = buildLocodeHtml('NL-RTM-PA3ABC', DATA);
  assert.ok(h.includes('Netherlands · Rotterdam'), `city: ${h}`);
  assert.ok(h.includes('HAM callsign: PA3ABC'), `call: ${h}`);
});
test('LOCODE-typed name with pipe-suffix callsign shows both type and callsign', () => {
  const h = buildLocodeHtml('BE-BRE-RP01 | ON8AR', DATA);
  assert.ok(h.includes('Repeater'), `type: ${h}`);
  assert.ok(h.includes('HAM callsign: ON8AR'), `call: ${h}`);
});
test('German Bundesland tooltip: 2-letter DE-NW -> region name', () => {
  const h = buildLocodeHtml('DE-NW-Cologne-EDG-01', DATA);
  assert.ok(h.includes('Germany · Nordrhein-Westfalen'), `got: ${h}`);
});
test('German Bundesland tooltip: 3-letter alias DE-NRW -> region name', () => {
  const h = buildLocodeHtml('DE-NRW', DATA);
  assert.ok(h && h.includes('Germany · Nordrhein-Westfalen'), `got: ${h}`);
});
test('region code wins over a colliding UN/LOCODE city code', () => {
  // real data: NRW is also a UN/LOCODE city (Neuweier) — Bundesland must win
  const data = JSON.parse(JSON.stringify(DATA));
  data.locations.DE = { NRW: 'Neuweier' };
  const h = buildLocodeHtml('DE-NRW', data);
  assert.ok(h.includes('Nordrhein-Westfalen'), `region should win: ${h}`);
  assert.ok(!h.includes('Neuweier'), `city should not show: ${h}`);
});
test('nothing resolvable: buildLocodeHtml returns null', () => {
  assert.strictEqual(buildLocodeHtml('SomeRandomName', DATA), null);
});

console.log(`\nTotal: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
