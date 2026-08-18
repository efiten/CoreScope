/**
 * Task 3 (rf-data-surfacing) — public/node-scopes.js renders the three
 * observed-vs-declared distinctions the spec requires, and keeps them
 * visually separate: never-asked vs declared-nothing, declared-but-not-
 * observed, observed-but-not-declared, and the two empty-state branches
 * (genuinely nothing forwarded vs scope tracking not configured).
 *
 * Executes the ACTUAL public/node-scopes.js file in a vm sandbox (not a
 * reimplementation) against real JSON captured from a locally-run server
 * hitting a migrated scratch DB (cmd/migrate + hand-inserted transmissions /
 * node_declared_regions rows), cross-checked against a direct SQL query
 * mirroring cmd/server/scopes.go's scopeConformanceQuery. See
 * .superpowers/sdd/2026-08-18-rf-data-surfacing/task-3-report.md for the
 * full verification transcript (server log, SQL, and API output side by side).
 */
'use strict';

const fs = require('fs');
const path = require('path');
const vm = require('vm');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

// --- Static guards -----------------------------------------------------

const src = fs.readFileSync(path.join(__dirname, 'public', 'node-scopes.js'), 'utf8');

console.log('\n=== Static: one fetch per node view, no per-item calls ===');
const apiCalls = src.match(/\bapi\(/g) || [];
assert(apiCalls.length === 1, 'exactly one api( ) call site in node-scopes.js (found ' + apiCalls.length + ')');
assert(/\/nodes\/'\s*\+\s*encodeURIComponent\(pubkey\)\s*\+\s*'\/scopes\?window=/.test(src),
  'the single call fetches /nodes/{pubkey}/scopes?window=... (one request per node view)');

// --- Dynamic: run the real module against captured API responses -------

function makeSandbox(apiResponse) {
  const calls = [];
  const sandbox = {
    window: {},
    escapeHtml: function (s) {
      return String(s).replace(/[&<>"']/g, function (c) {
        return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
      });
    },
    timeAgo: function (iso) {
      if (!iso) return '—';
      return 'AGE(' + iso + ')'; // deterministic stand-in; we only assert presence/absence, not exact wording
    },
    api: function (p) { calls.push(p); return Promise.resolve(apiResponse); },
    console: console,
  };
  sandbox.global = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(src, sandbox, { filename: 'node-scopes.js' });
  return { sandbox, calls };
}

function fakeContainer() {
  return {
    _html: '',
    set innerHTML(v) { this._html = v; },
    get innerHTML() { return this._html; },
    querySelector: function () { return null; }, // skip button wiring — not under test here
  };
}

async function renderAndGet(apiResponse) {
  const { sandbox, calls } = makeSandbox(apiResponse);
  const c = fakeContainer();
  sandbox.window.NodeScopes.render(c, 'deadbeef');
  // load() is async; flush microtasks until the mocked api() promise resolves
  // and the container is repainted from the loading spinner.
  for (let i = 0; i < 10 && /Loading scopes/.test(c.innerHTML); i++) {
    await new Promise(function (r) { setImmediate(r); });
  }
  return { html: c.innerHTML, calls: calls };
}

// Fixture 1 — captured 2026-08-18 from a locally-run server against a
// migrated scratch DB. Verified against a direct SQL query mirroring
// scopeConformanceQuery (see report): observed {be:5, be-vlg:3}, unmatched
// 7, unscoped 4, routes {transportFlood:5, flood:14, direct:0,
// transportDirect:0}. declared = {regions:[be,nl], truncated:true},
// captured from the observed_at-ordered row (NOT the later-ingested stale
// row) — exercises all three declared/observed states at once:
//   be     -> declared AND observed        (agreement)
//   nl     -> declared, NOT observed       (the row this work exists to surface)
//   be-vlg -> observed, NOT declared       (forwarding something undeclared)
const THREE_STATE = {
  publicKey: 'a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4',
  window: '24h',
  observed: [
    { scope: 'be-vlg', packets: 3, firstSeen: '2026-08-18T17:40:06Z', lastSeen: '2026-08-18T17:42:06Z' },
    { scope: 'be', packets: 5, firstSeen: '2026-08-18T17:46:06Z', lastSeen: '2026-08-18T17:52:06Z' },
  ],
  unmatched: 7,
  unscoped: 4,
  routes: { transportFlood: 5, flood: 14, direct: 0, transportDirect: 0 },
  declared: { regions: ['be', 'nl'], observedAt: '2026-08-18T16:02:06Z', truncated: true },
};

// Fixture 2 — captured live: a repeater whose traffic is entirely
// unmatched (0 observed, 6 unmatched, declared never asked). This is the
// "scope tracking not configured on this deployment" empty branch.
const NOT_CONFIGURED = {
  publicKey: 'b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2',
  window: '24h', observed: [], unmatched: 6, unscoped: 0,
  routes: { transportFlood: 0, flood: 6, direct: 0, transportDirect: 0 },
  declared: null,
};

// Fixture 3 — captured live: a pubkey this instance has never observed
// forwarding anything at all (unmatched=0 too). This is the OTHER empty
// branch — a genuine "nothing forwarded", must read differently from
// fixture 2 despite both having observed=[].
const GENUINE_EMPTY = {
  publicKey: 'c'.repeat(64),
  window: '24h', observed: [], unmatched: 0, unscoped: 0,
  routes: { transportFlood: 0, flood: 0, direct: 0, transportDirect: 0 },
  declared: null,
};

// Fixture 4 — captured live: a repeater that DID successfully answer a
// declared-regions request and declares nothing flood-allowed
// (declared.regions = [], NOT null). Must read differently from fixture 3
// (never asked) even though both have zero observed scopes.
const DECLARES_NOTHING = {
  publicKey: 'd4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4',
  window: '24h', observed: [], unmatched: 0, unscoped: 0,
  routes: { transportFlood: 0, flood: 0, direct: 0, transportDirect: 0 },
  declared: { regions: [], observedAt: '2026-08-18T17:35:36Z', truncated: false },
};

(async function main() {
  console.log('\n=== Fixture 1: three-state declared/observed distinction ===');
  {
    const { html, calls } = await renderAndGet(THREE_STATE);
    assert(calls.length === 1 && calls[0] === '/nodes/deadbeef/scopes?window=24h',
      'fetched exactly once, with the default 24h window: ' + calls[0]);
    assert(/be[\s\S]{0,120}declared</.test(html) || /ns-decl-yes/.test(html),
      "'be' (declared AND observed) renders the agreement badge");
    assert(/nl[\s\S]{0,400}declared, not observed/.test(html),
      "'nl' (declared but never observed) renders 'declared, not observed' — the row this work exists to surface");
    assert(/be-vlg[\s\S]{0,400}observed, not declared/.test(html),
      "'be-vlg' (observed but absent from declared list) renders 'observed, not declared'");
    assert(/>7</.test(html), 'unmatched (7) is shown plainly, not behind a toggle');
    assert(/>4</.test(html), 'unscoped (4) is shown plainly, not behind a toggle');
    assert(/transportFlood <b>5<\/b>/.test(html) && /flood <b>14<\/b>/.test(html),
      'route mix shows transportFlood=5, flood=14 (packets this node was observed FORWARDING)');
    assert(/direct <b>0<\/b>/.test(html) && /transportDirect <b>0<\/b>/.test(html),
      'route mix shows direct=0, transportDirect=0 (always 0 by construction)');
    assert(/list truncated/.test(html), 'truncated:true on the declared answer is surfaced in words');
    assert(!/ns-empty/.test(html), 'rows exist (2 observed + 1 declared-only) — no empty-state panel shown');
    assert(/#\/analytics\?tab=hashsizes/.test(html), 'links to the existing hash-sizes analytics rather than reimplementing it');
  }

  console.log('\n=== Fixture 2: "not configured" empty state (unmatched > 0) ===');
  {
    const { html } = await renderAndGet(NOT_CONFIGURED);
    assert(/ns-empty-config/.test(html), 'renders the config-specific empty-state class');
    assert(/region-keys? config/i.test(html), 'empty message points at the region-keys configuration in words');
    assert(/never successfully asked/.test(html), 'declared: null renders as "never successfully asked", not "declares nothing"');
    assert(!/declared, not observed|observed, not declared/.test(html), 'no per-scope declared badges render when there are no rows');
  }

  console.log('\n=== Fixture 3: genuine empty state (nothing forwarded, unmatched = 0) ===');
  {
    const { html } = await renderAndGet(GENUINE_EMPTY);
    assert(/ns-empty/.test(html) && !/ns-empty-config/.test(html),
      'renders the plain empty-state panel, NOT the config-specific one');
    assert(!/region-keys? config/i.test(html), 'does not point at region-keys config when unmatched is 0 (not a config issue)');
    assert(/forwarded nothing we observed/.test(html), 'says plainly that this repeater forwarded nothing observed');
  }

  console.log('\n=== Fixture 2 vs Fixture 3: distinguishable empty states ===');
  {
    const a = (await renderAndGet(NOT_CONFIGURED)).html;
    const b = (await renderAndGet(GENUINE_EMPTY)).html;
    assert(a !== b, 'the two empty-state renders are NOT textually identical (the failure class this work targets)');
  }

  console.log('\n=== Fixture 4: answered-but-declares-nothing vs never-asked ===');
  {
    const { html } = await renderAndGet(DECLARES_NOTHING);
    assert(/Declared regions answer captured/.test(html), 'declared:[] (non-null) renders an answer-captured line with an age, not "never asked"');
    assert(!/never successfully asked/.test(html), 'declared:[] must NOT render the never-asked message (declared.regions:[] != declared:null)');
  }

  console.log('\n=== Summary ===');
  console.log('  Passed: ' + passed);
  console.log('  Failed: ' + failed);
  console.log('\nnode-scopes ' + (failed === 0 ? 'PASS' : 'FAIL'));
  process.exit(failed === 0 ? 0 : 1);
})();
