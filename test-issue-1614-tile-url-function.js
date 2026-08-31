/* test-issue-1614-tile-url-function.js — regression test for #1614.
 *
 * Bug: window.getTileUrl() in public/roles.js returns the provider's
 * `url` *property as-is*. When the active dark-mode provider declares
 * `url: function()` (carto/osm/stamen lazy resolvers added in #1533),
 * the helper returns the function itself. Callers pass that function to
 * L.tileLayer(), which stringifies the function source as the URL
 * template — every tile request 404s, the map is blank, no console error.
 *
 * Contract under test:
 *   - getTileUrl() MUST return a string URL template, regardless of
 *     whether the active provider declares `url` as a string or a function.
 *   - It must contain the leaflet template placeholders {z}/{x}/{y}.
 *   - For string-`url` providers it must return the string verbatim
 *     (so we don't double-invoke or otherwise regress).
 *
 * Loads only roles.js — provider registry is mocked directly via
 * window.MC_TILE_PROVIDERS + window.MC_getDarkTileProvider so the test
 * stays focused on the consumer (the source of the bug) and would still
 * fail if map-tile-providers.js drifted.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

function makeSandbox(theme) {
  const ctx = {
    console, setTimeout, clearTimeout,
    JSON, Date, Math, Object, Array, String, Number, Boolean, Set, Map,
    fetch: () => Promise.resolve({ ok: false, json: () => Promise.resolve({}) }),
    CustomEvent: function (type, init) { this.type = type; this.detail = (init && init.detail) || null; },
    document: {
      documentElement: { getAttribute: () => theme, style: { getPropertyValue: () => '' } },
      querySelector: () => null,
      querySelectorAll: () => [],
      getElementById: () => null,
      createElement: () => ({ style: {}, appendChild: () => {}, setAttribute: () => {}, addEventListener: () => {} }),
      addEventListener: () => {},
      body: { appendChild: () => {}, style: {} },
      head: { appendChild: () => {} },
      readyState: 'complete',
    },
    window: {
      addEventListener: () => {},
      matchMedia: () => ({ matches: theme === 'dark', addEventListener: () => {} }),
    },
  };
  ctx.window.document = ctx.document;
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  const src = fs.readFileSync(path.join(__dirname, 'public', 'roles.js'), 'utf8');
  vm.runInContext(src, ctx, { filename: 'public/roles.js' });
  // Mirror window.* back so bare-name refs inside roles.js (TILE_DARK etc.) resolve.
  for (const k of Object.keys(ctx.window)) if (!(k in ctx)) ctx[k] = ctx.window[k];
  return ctx;
}

console.log('── #1614 getTileUrl() must return a string (not a function) ──');

test('dark + provider with url:function → getTileUrl returns string URL template', () => {
  const ctx = makeSandbox('dark');
  ctx.window.MC_TILE_PROVIDERS = {
    'fn-provider': {
      provider: 'carto',
      label: 'Fn Provider',
      url: function () { return 'https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png'; },
      attribution: '© OSM © CartoDB',
    },
  };
  ctx.window.MC_getDarkTileProvider = function () { return 'fn-provider'; };

  const out = ctx.window.getTileUrl();
  assert.strictEqual(typeof out, 'string',
    'getTileUrl() must return a string, got typeof ' + typeof out +
    ' (value: ' + String(out).slice(0, 80) + '…)');
  assert.ok(/\{z\}/.test(out) && /\{x\}/.test(out) && /\{y\}/.test(out),
    'returned string must be a leaflet URL template with {z}/{x}/{y}; got: ' + out);
});

test('dark + provider with url:string → getTileUrl returns it verbatim', () => {
  const ctx = makeSandbox('dark');
  const STR = 'https://tiles.example.com/dark/{z}/{x}/{y}.png';
  ctx.window.MC_TILE_PROVIDERS = {
    'str-provider': { provider: 'carto', label: 'Str', url: STR, attribution: 'x' },
  };
  ctx.window.MC_getDarkTileProvider = function () { return 'str-provider'; };

  const out = ctx.window.getTileUrl();
  assert.strictEqual(out, STR, 'string-url provider must round-trip verbatim');
});

test('dark + provider with only baseUrl as function → getTileUrl returns string', () => {
  // Defense-in-depth: roles.js falls back to p.baseUrl when p.url is missing.
  // Same function/string treatment must apply.
  const ctx = makeSandbox('dark');
  ctx.window.MC_TILE_PROVIDERS = {
    'base-fn': {
      provider: 'carto', label: 'Base',
      baseUrl: function () { return 'https://tiles.example.com/{z}/{x}/{y}.png'; },
      attribution: 'x',
    },
  };
  ctx.window.MC_getDarkTileProvider = function () { return 'base-fn'; };

  const out = ctx.window.getTileUrl();
  assert.strictEqual(typeof out, 'string',
    'baseUrl function must also be invoked; got typeof ' + typeof out);
  assert.ok(/\{z\}/.test(out), 'returned string must contain {z}; got: ' + out);
});

console.log('\n── light mode must resolve through the registry too ──');

// The light branch used to `return TILE_LIGHT` without ever consulting the
// registry. Invisible until CARTO started requiring an API key: the registry's
// carto styles append `?key=`, the bare TILE_LIGHT constant does not, so every
// light-theme caller (analytics.js, nodes.js) served watermarked tiles even
// with a key configured.

test('light + provider with url:function → getTileUrl resolves it, not the bare constant', () => {
  const ctx = makeSandbox('light');
  ctx.window.MC_TILE_PROVIDERS = {
    'carto-light': {
      provider: 'carto', label: 'Carto Positron',
      url: function () { return 'https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png?key=abc123'; },
      attribution: '© OSM © CartoDB',
    },
  };
  ctx.window.MC_getLightTileProvider = function () { return 'carto-light'; };

  const out = ctx.window.getTileUrl();
  assert.strictEqual(typeof out, 'string', 'must return a string, got ' + typeof out);
  assert.ok(out.indexOf('?key=abc123') !== -1,
    'light mode must carry the configured Carto key; got: ' + out);
  assert.notStrictEqual(out, ctx.window.TILE_LIGHT,
    'light mode must not short-circuit to the unkeyed TILE_LIGHT constant');
});

test('light + provider with url:string → returned verbatim', () => {
  const ctx = makeSandbox('light');
  const STR = 'https://tiles.example.com/light/{z}/{x}/{y}.png';
  ctx.window.MC_TILE_PROVIDERS = {
    'str-light': { provider: 'carto', label: 'Str', url: STR, attribution: 'x' },
  };
  ctx.window.MC_getLightTileProvider = function () { return 'str-light'; };

  assert.strictEqual(ctx.window.getTileUrl(), STR);
});

test('light + no registry → still falls back to TILE_LIGHT', () => {
  const ctx = makeSandbox('light');
  delete ctx.window.MC_TILE_PROVIDERS;
  delete ctx.window.MC_getLightTileProvider;

  assert.strictEqual(ctx.window.getTileUrl(), ctx.window.TILE_LIGHT,
    'the constant remains the last-resort fallback when the registry is absent');
});

test('dark fallback is unchanged when the registry is absent', () => {
  const ctx = makeSandbox('dark');
  delete ctx.window.MC_TILE_PROVIDERS;
  delete ctx.window.MC_getDarkTileProvider;

  assert.strictEqual(ctx.window.getTileUrl(), ctx.window.TILE_DARK);
});

console.log('\n#1614 getTileUrl() string contract: ' + passed + ' passed, ' + failed + ' failed');
process.exit(failed === 0 ? 0 : 1);
