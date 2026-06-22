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
  countries: { BE: 'Belgium' },
  types: { EBP: 'Edge · Battery Powered' },
  locations: { BE: { GNE: 'Gent', VLD: 'Oedelem', BLZ: 'Bilzen', BGS: 'Brugge' } },
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
test('nothing resolvable: buildLocodeHtml returns null', () => {
  assert.strictEqual(buildLocodeHtml('SomeRandomName', DATA), null);
});

console.log(`\nTotal: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
