/**
 * #1862 — Repeater region-scope filtering on the map.
 *
 * Fork-local implementation. Upstream PR #1852 covers the same ground with a
 * server-side `hasScope`/`hashRegion` API; this one is purely client-side and
 * reuses `transported_scopes`, which the bulk /api/nodes payload already
 * carries per repeater (#1751). Logic lives in its own module so that when
 * #1852 lands, only the small wiring in map.js has to be re-applied.
 *
 * Acceptance:
 *   - collectScopes() returns the sorted, deduplicated scope set across nodes.
 *   - nodePassesScopeFilters() gates on region + hasScope, repeaters/rooms only.
 *   - Non-relay roles are never filtered out by these two controls.
 *   - map.js is wired to the module rather than open-coding the predicate.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const ctx = { window: {}, console };
ctx.window = ctx;
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(__dirname, 'public', 'map-scope-filter.js'), 'utf8'), ctx);
const { collectScopes, nodePassesScopeFilters } = ctx;

const ALL = { scopeRegion: 'all', hasScope: 'all' };
const rep = (scopes) => ({ role: 'repeater', transported_scopes: scopes });

console.log('\n=== #1862: collectScopes ===');
assert(JSON.stringify(collectScopes([])) === '[]', 'empty node list yields no scopes');
assert(
  JSON.stringify(collectScopes([rep(['#nl', '#be']), rep(['#be', '#de'])])) === JSON.stringify(['#be', '#de', '#nl']),
  'scopes are deduplicated and sorted'
);
assert(
  JSON.stringify(collectScopes([{ role: 'companion' }, rep(null), rep(['#eu'])])) === JSON.stringify(['#eu']),
  'nodes without scopes are skipped without throwing'
);
assert(
  JSON.stringify(collectScopes([{ role: 'room', transported_scopes: ['#dk'] }])) === JSON.stringify(['#dk']),
  'rooms relay too, so their scopes are collected'
);

console.log('\n=== #1862: region filter ===');
assert(nodePassesScopeFilters(rep(['#be']), ALL), 'the "all" default passes everything');
assert(nodePassesScopeFilters(rep(['#be', '#nl']), { scopeRegion: '#nl', hasScope: 'all' }), 'repeater forwarding the region passes');
assert(!nodePassesScopeFilters(rep(['#be']), { scopeRegion: '#nl', hasScope: 'all' }), 'repeater not forwarding the region is filtered out');
assert(!nodePassesScopeFilters(rep(null), { scopeRegion: '#nl', hasScope: 'all' }), 'repeater with no scopes at all is filtered out by a region pick');

console.log('\n=== #1862: hasScope toggle ===');
assert(nodePassesScopeFilters(rep(['#be']), { scopeRegion: 'all', hasScope: 'yes' }), 'hasScope=yes keeps a scoped repeater');
assert(!nodePassesScopeFilters(rep([]), { scopeRegion: 'all', hasScope: 'yes' }), 'hasScope=yes drops an unscoped repeater');
assert(nodePassesScopeFilters(rep(null), { scopeRegion: 'all', hasScope: 'no' }), 'hasScope=no keeps a repeater that never forwarded a scope');
assert(!nodePassesScopeFilters(rep(['#de']), { scopeRegion: 'all', hasScope: 'no' }), 'hasScope=no drops a scoped repeater');

console.log('\n=== #1862: only relaying roles are gated ===');
for (const role of ['companion', 'sensor', 'observer']) {
  assert(
    nodePassesScopeFilters({ role }, { scopeRegion: '#nl', hasScope: 'yes' }),
    role + ' is never hidden by the scope controls'
  );
}
assert(!nodePassesScopeFilters({ role: 'room', transported_scopes: ['#be'] }, { scopeRegion: '#nl', hasScope: 'all' }), 'rooms are gated like repeaters');
assert(nodePassesScopeFilters({ transported_scopes: ['#be'] }, { scopeRegion: '#nl', hasScope: 'all' }), 'a node with no role defaults to companion and is not gated');

console.log('\n=== #1862: map.js wiring ===');
const mapSrc = fs.readFileSync(path.join(__dirname, 'public', 'map.js'), 'utf8');
assert(/nodePassesScopeFilters/.test(mapSrc), 'map.js calls the shared predicate instead of open-coding it');
assert(/collectScopes/.test(mapSrc), 'map.js populates the region dropdown from collectScopes');
assert(/mcScopeRegion/.test(mapSrc), 'map.js renders the region select');
assert(/mcHasScope/.test(mapSrc), 'map.js renders the has-scope control');
assert(/meshcore-map-scope-region/.test(mapSrc), 'region choice persists to localStorage');
assert(/meshcore-map-has-scope/.test(mapSrc), 'has-scope choice persists to localStorage');

const indexSrc = fs.readFileSync(path.join(__dirname, 'public', 'index.html'), 'utf8');
assert(/map-scope-filter\.js\?v=__BUST__/.test(indexSrc), 'index.html loads the module with a cache buster');

console.log(`\nTotal: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
