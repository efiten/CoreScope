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

// makeControllableSandbox (FIX 8) — like makeSandbox, but api() never
// resolves on its own; the test resolves each call's promise explicitly via
// the returned `pending` map (keyed by the request path), so response
// arrival order can be controlled independently of request order. This is
// what lets the loadGen guard be exercised: start an older request, start a
// newer one before the older resolves, resolve the newer one first, THEN
// resolve the stale older one and assert it did not overwrite the render.
function makeControllableSandbox() {
  const calls = [];
  const pending = {};
  const sandbox = {
    window: {},
    escapeHtml: function (s) {
      return String(s).replace(/[&<>"']/g, function (c) {
        return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
      });
    },
    timeAgo: function (iso) {
      if (!iso) return '—';
      return 'AGE(' + iso + ')';
    },
    api: function (p) {
      calls.push(p);
      return new Promise(function (resolve) { pending[p] = resolve; });
    },
    console: console,
  };
  sandbox.global = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(src, sandbox, { filename: 'node-scopes.js' });
  return { sandbox, calls, pending };
}

function fakeContainer() {
  return {
    _html: '',
    set innerHTML(v) { this._html = v; },
    get innerHTML() { return this._html; },
    // render() delegates a click listener on the container itself; accept and
    // ignore it here. Button behaviour is exercised by fakeContainerWithBar.
    addEventListener: function () {},
    querySelector: function () { return null; },
  };
}

// fakeContainerWithBar (FIX 8) — a minimal DOM stub enough to drive
// wireWindowButtons: querySelector('#nsWindow') returns a fresh stub
// "element" every time innerHTML is set (mirroring how a real
// `container.innerHTML = ...` replaces the subtree and produces a brand
// element (whose own node survives its children being replaced), so the stub
// gives the CONTAINER the listener list and keeps it across innerHTML writes.
// That is the behaviour under test: a window button must stay live while a
// fetch is in flight, which is also the only way load()'s loadGen guard can
// ever be reached. clickWindowButton() fires every listener registered on the
// container with an event whose target.closest(sel) returns the button.
function fakeContainerWithBar() {
  var c = {
    _html: '',
    _listeners: [],
    set innerHTML(v) { this._html = v; },
    get innerHTML() { return this._html; },
    addEventListener: function (type, fn) {
      if (type === 'click') this._listeners.push(fn);
    },
    querySelector: function () { return null; },
  };
  c.clickWindowButton = function (windowKey) {
    var target = {
      closest: function (sel) {
        if (sel !== 'button[data-window]') return null;
        return { getAttribute: function (n) { return n === 'data-window' ? windowKey : null; } };
      },
    };
    c._listeners.forEach(function (fn) { fn({ target: target }); });
  };
  return c;
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

// Fixture 5 (FIX 1) — declared-only rows (nothing observed at all) combined
// with unmatched>0: a repeater that DID answer a declared-regions request,
// but this instance's region-key config could still be the reason none of
// its traffic could be named. Before FIX 1 the config note was gated on
// `rows.length === 0`, and rows is the observed+declared UNION — so this
// exact combination (declared-only row present, unmatched>0) rendered a
// bare "declared, not observed" row with no explanation that unmatched
// packets exist, silently pointing the operator at the repeater instead of
// at local config.
const DECLARED_ONLY_UNMATCHED = {
  publicKey: 'e'.repeat(64),
  window: '24h', observed: [], unmatched: 3, unscoped: 0,
  routes: { transportFlood: 0, flood: 3, direct: 0, transportDirect: 0 },
  declared: { regions: ['be'], observedAt: '2026-08-18T17:35:36Z', truncated: false },
};

// Fixture 6 (FIX 2) — a scope name and a declared region name that collide
// with Object.prototype keys. buildRows() used to key a plain {} by these
// names: `byName['__proto__'] = ...` writes the prototype object itself
// (no own key created, so Object.keys() never sees it — silently dropped);
// `if (!byName['constructor'])` is false against a plain {} because
// Object.prototype.constructor is already truthy, so a declared-only
// 'constructor' row is silently swallowed. Both names are attacker-
// influenceable (declared.regions comes from a repeater's own RF reply;
// scope names come from packets).
const PROTO_COLLISION = {
  publicKey: 'f'.repeat(64),
  window: '24h',
  observed: [{ scope: '__proto__', packets: 2, firstSeen: '2026-08-18T17:40:06Z', lastSeen: '2026-08-18T17:42:06Z' }],
  unmatched: 0, unscoped: 0,
  routes: { transportFlood: 0, flood: 2, direct: 0, transportDirect: 0 },
  declared: { regions: ['constructor'], observedAt: '2026-08-18T17:35:36Z', truncated: false },
};

// Fixture 7 (FIX 8) — the response to the NEWER (1h) request in the
// loadGen race test. Deliberately distinct from THE_STATE/NOT_CONFIGURED so
// a wrongly-rendered stale response is unmistakable in the assertions.
const STALE_TEST_NEW = {
  publicKey: 'a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4',
  window: '1h',
  observed: [{ scope: 'nl', packets: 99, firstSeen: '2026-08-18T17:40:06Z', lastSeen: '2026-08-18T17:42:06Z' }],
  unmatched: 0, unscoped: 0,
  routes: { transportFlood: 0, flood: 99, direct: 0, transportDirect: 0 },
  declared: null,
};

(async function main() {
  console.log('\n=== Fixture 1: three-state declared/observed distinction ===');
  {
    const { html, calls } = await renderAndGet(THREE_STATE);
    assert(calls.length === 1 && calls[0] === '/nodes/deadbeef/scopes?window=24h',
      'fetched exactly once, with the default 24h window: ' + calls[0]);
    assert(/<td class="ns-scope-name">be<\/td>[\s\S]{0,200}ns-decl-yes/.test(html),
      "'be' (declared AND observed) renders the agreement badge, bound to the 'be' row specifically");
    assert(/nl[\s\S]{0,400}declared, not observed/.test(html),
      "'nl' (declared but never observed) renders 'declared, not observed' — the row this work exists to surface");
    assert(/be-vlg[\s\S]{0,400}observed, not declared/.test(html),
      "'be-vlg' (observed but absent from declared list) renders 'observed, not declared'");
    assert(/Unmatched<\/div><div class="analytics-stat-value">7<\/div>/.test(html),
      'unmatched (7) is bound to the Unmatched stat card specifically');
    assert(/Unscoped<\/div><div class="analytics-stat-value">4<\/div>/.test(html),
      'unscoped (4) is bound to the Unscoped stat card specifically (would fail if unmatched/unscoped were swapped)');
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
    assert(/region-keys? config/i.test(a) && !/region-keys? config/i.test(b),
      'the config-not-set message text appears in fixture 2 (not configured) but not fixture 3 (genuinely empty)');
    assert(/forwarded nothing we observed/.test(b) && !/forwarded nothing we observed/.test(a),
      'the genuinely-empty message text appears in fixture 3 but not fixture 2 — the two empty states carry distinct wording, not just distinct stat-card numbers');
  }

  console.log('\n=== Fixture 4: answered-but-declares-nothing vs never-asked ===');
  {
    const { html } = await renderAndGet(DECLARES_NOTHING);
    assert(/Declared regions answer captured/.test(html), 'declared:[] (non-null) renders an answer-captured line with an age, not "never asked"');
    assert(!/never successfully asked/.test(html), 'declared:[] must NOT render the never-asked message (declared.regions:[] != declared:null)');
    assert(/it declares no regions flood-allowed/.test(html), 'FIX 5: an empty declared.regions list says plainly that it declares no regions flood-allowed');
  }

  console.log('\n=== FIX 1: config note must be judged from observed, not from the observed+declared union ===');
  {
    // Case A: observed empty, unmatched>0, declared present with a region
    // this instance never observed forwarding. rows.length > 0 (one
    // declared-only row), so the pre-fix `rows.length === 0` branch would
    // never show the config note here — exactly the bug: a declared list
    // masked the "no region key could ever name this traffic" signal.
    const { html: a } = await renderAndGet(DECLARED_ONLY_UNMATCHED);
    assert(/ns-empty-config/.test(a), 'the config note renders even though a declared-only row exists (rows.length > 0)');
    assert(/region-keys? config/i.test(a), 'config note text is present');
    assert(/<table class="ns-table">/.test(a), 'the table itself STILL renders — note is in addition to the table, not instead of it');
    assert(/<td class="ns-scope-name">be<\/td>[\s\S]{0,400}declared, not observed/.test(a),
      "the declared-only 'be' row renders alongside the note");

    // Case B: observed empty, unmatched>0, no declared list at all — current
    // behaviour (Fixture 2) must be unchanged: note only, no table.
    const { html: b } = await renderAndGet(NOT_CONFIGURED);
    assert(/ns-empty-config/.test(b), 'note renders with no declared list either (unchanged behaviour)');
    assert(!/<table class="ns-table">/.test(b), 'no table renders when there are truly zero rows');

    // Case C: observed empty, unmatched=0 — the plain "forwarded nothing"
    // message, never the config note (Fixture 3, re-asserted here under the
    // FIX 1 heading for clarity).
    const { html: c } = await renderAndGet(GENUINE_EMPTY);
    assert(!/ns-empty-config/.test(c), 'no config note when unmatched is 0');
    assert(/forwarded nothing we observed/.test(c), 'plain empty-state message renders instead');
  }

  console.log('\n=== FIX 2: Object.prototype-colliding scope/region names must not be dropped or swallowed ===');
  {
    const { html } = await renderAndGet(PROTO_COLLISION);
    assert(/<td class="ns-scope-name">__proto__<\/td><td class="ns-n">2<\/td>/.test(html),
      "a scope literally named '__proto__' gets its own row with the correct packet count (plain-object assignment would write the prototype and create no own key)");
    assert(/<td class="ns-scope-name">constructor<\/td>[\s\S]{0,400}declared, not observed/.test(html),
      "a declared region literally named 'constructor' gets its own declared-only row (a plain {} already has a truthy .constructor, which would swallow it via `if (!byName[r])`)");
  }

  console.log('\n=== FIX 8: wireWindowButtons — clicking a window button reloads with that window ===');
  {
    const { sandbox, calls } = makeSandbox(THREE_STATE);
    const c = fakeContainerWithBar();
    sandbox.window.NodeScopes.render(c, 'deadbeef');
    for (let i = 0; i < 10 && /Loading scopes/.test(c.innerHTML); i++) {
      await new Promise(function (r) { setImmediate(r); });
    }
    assert(calls.length === 1 && calls[0] === '/nodes/deadbeef/scopes?window=24h',
      'initial render fetches the default (24h) window');
    c.clickWindowButton('1h');
    for (let i = 0; i < 10 && calls.length < 2; i++) {
      await new Promise(function (r) { setImmediate(r); });
    }
    assert(calls.length === 2 && calls[1] === '/nodes/deadbeef/scopes?window=1h',
      "clicking the 1h button (e.target.closest('button[data-window]') resolving to it) triggers a reload fetching window=1h");
  }

  console.log('\n=== Delegation: a window button stays live WHILE a fetch is in flight ===');
  {
    // Regression guard. The listener used to be bound to the #nsWindow bar, which
    // load() destroys every time it reassigns container.innerHTML, and it was only
    // rebound after the fetch settled. For the whole duration of a load the buttons
    // were rendered but dead - they looked clickable and silently did nothing, and
    // no click sequence could ever reach load()'s loadGen guard. Delegating on the
    // container (its own node survives its children being replaced) fixes both.
    const { sandbox, calls } = makeSandbox(THREE_STATE);
    const c = fakeContainerWithBar();
    sandbox.window.NodeScopes.render(c, 'deadbeef');
    assert(/Loading scopes/.test(c.innerHTML), 'precondition: the first fetch is still in flight');
    assert(calls.length === 1, 'precondition: exactly the initial fetch so far');
    c.clickWindowButton('7d'); // clicked BEFORE the first load settles
    for (let i = 0; i < 10 && calls.length < 2; i++) {
      await new Promise(function (r) { setImmediate(r); });
    }
    assert(calls.length === 2 && calls[1] === '/nodes/deadbeef/scopes?window=7d',
      'a click landing mid-flight is honoured and fetches window=7d');
  }

  console.log('\n=== FIX 8: wireWindowButtons — loadGen guard rejects a stale in-flight response ===');
  {
    const { sandbox, calls, pending } = makeControllableSandbox();
    const c = fakeContainerWithBar();
    // NOTE (documented limitation — see FIX 8 report): wireWindowButtons only
    // (re)binds the click listener AFTER a fetch settles (load()'s success
    // and catch branches both call it at the end, never before the await).
    // So the rendered buttons are structurally unwired for the entire
    // duration a fetch is in flight — a second click cannot physically land
    // while the first is still pending; there is no click sequence that
    // produces two in-flight loads. The loadGen guard is instead exercised
    // here through two overlapping calls to NodeScopes.render() itself,
    // which drives the exact same load()/loadGen code a button click runs
    // (a second render() firing before the first settles is the realistic
    // trigger — e.g. a remount or refresh landing mid-fetch).
    sandbox.window.NodeScopes.render(c, 'deadbeef'); // load #1: myGen=1, window=24h
    await new Promise(function (r) { setImmediate(r); });
    assert(calls.length === 1 && calls[0] === '/nodes/deadbeef/scopes?window=24h',
      'first load is in flight and unresolved');

    sandbox.window.NodeScopes.render(c, 'deadbeef'); // load #2, started before #1 resolves: myGen=2
    await new Promise(function (r) { setImmediate(r); });
    assert(calls.length === 2 && calls[1] === '/nodes/deadbeef/scopes?window=24h',
      'a second, newer load starts while the first is still in flight (loadGen now 2)');

    // Resolve the NEWER request first — this is the one that should render.
    pending[calls[1]](STALE_TEST_NEW);
    await new Promise(function (r) { setImmediate(r); });
    assert(/<td class="ns-scope-name">nl<\/td><td class="ns-n">99<\/td>/.test(c.innerHTML),
      'the newer response, resolving first, renders');

    // Now resolve the STALE (older) request, arriving after. myGen for this
    // load is 1, but loadGen has since advanced to 2 — the guard
    // `if (myGen !== loadGen) return;` must discard this render entirely.
    pending[calls[0]](NOT_CONFIGURED);
    await new Promise(function (r) { setImmediate(r); });
    assert(/<td class="ns-scope-name">nl<\/td><td class="ns-n">99<\/td>/.test(c.innerHTML) && !/ns-empty-config/.test(c.innerHTML),
      'the stale response, resolving after the newer one, does NOT overwrite the render (loadGen guard holds)');
  }

  console.log('\n=== Summary ===');
  console.log('  Passed: ' + passed);
  console.log('  Failed: ' + failed);
  console.log('\nnode-scopes ' + (failed === 0 ? 'PASS' : 'FAIL'));
  process.exit(failed === 0 ? 0 : 1);
})();
