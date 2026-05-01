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
const { parseLocodeName, locodeAttr } = ctx;

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

console.log(`\nTotal: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
