'use strict';
// Unit test for node-reach-coverage.js color buckets. Loads the browser IIFE in
// a vm sandbox (pattern from test-frontend-helpers.js) and exercises the pure
// coverageColorVar mapping.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const code = fs.readFileSync(path.join(__dirname, 'public', 'node-reach-coverage.js'), 'utf8');
const sandbox = { window: {}, document: {}, getComputedStyle: function () { return { getPropertyValue: function () { return ''; } }; } };
vm.createContext(sandbox);
vm.runInContext(code, sandbox);

const { coverageColorVar } = sandbox.window.NodeReachCoverage;

assert.strictEqual(coverageColorVar({ has_sig: false }), '--nq-cov-grey', 'no-sig → grey');
assert.strictEqual(coverageColorVar({ has_sig: true, best_snr: null }), '--nq-cov-grey', 'null snr → grey');
assert.strictEqual(coverageColorVar({ has_sig: true, best_snr: -3 }), '--nq-cov-strong', 'strong');
assert.strictEqual(coverageColorVar({ has_sig: true, best_snr: -6 }), '--nq-cov-strong', 'boundary strong');
assert.strictEqual(coverageColorVar({ has_sig: true, best_snr: -10 }), '--nq-cov-mid', 'mid');
assert.strictEqual(coverageColorVar({ has_sig: true, best_snr: -18 }), '--nq-cov-weak', 'weak');
assert.strictEqual(coverageColorVar(null), '--nq-cov-grey', 'null props → grey');

console.log('node-reach-coverage color buckets OK');
