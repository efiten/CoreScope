#!/usr/bin/env node
'use strict';
// Uplink layer on the Mobile RX coverage page: the "Uplink" button exists only
// with clientUplink, asks /api/uplink-coverage, colours each cell by its best
// class, swaps in the uplink legend, is deep-linkable (layer=uplink), and is
// narrowed by the observer filter and "My coverage" like the other layers.
// Backend-independent: every /api/* call is mocked.
const assert = require('assert');
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';

const poly = (lon, lat) => ({ type: 'Polygon', coordinates: [[[lon, lat], [lon + 0.01, lat], [lon + 0.01, lat + 0.01], [lon, lat + 0.01], [lon, lat]]] });
const cell = (lon, lat, best) => ({ type: 'Feature', geometry: poly(lon, lat), properties: {
  cell: 'u' + lon, pings: 2, both: best === 'both' ? 1 : 0, observed: best === 'observed' ? 1 : 0, echo: 0, none: 1,
  best, first_hops: ['1403'], max_observers: 17, max_hops: 11, last_at: '2026-10-16T14:51:15.018Z' } });

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.log('  ✗ ' + name + ': ' + e.message); }
}

async function openPage(browser, { uplink, hash, user }) {
  const page = await browser.newPage();
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  const uplinkRequests = [];
  await page.route('**/api/**', route => {
    const url = route.request().url();
    const path = new URL(url).pathname;
    if (path === '/api/config/client') return route.fulfill({ json: { clientRxCoverage: true, clientRfSamples: false, clientUplink: uplink, userManagement: { enabled: !!user } } });
    if (path === '/api/auth/me' && user) return route.fulfill({ json: user });
    if (path === '/api/config/map') return route.fulfill({ json: { center: [51, 5.4], zoom: 11 } });
    if (path === '/api/rx-leaderboard') return route.fulfill({ json: { observers: [] } });
    if (path === '/api/rx-coverage') return route.fulfill({ json: { type: 'FeatureCollection', features: [] } });
    if (path === '/api/uplink-coverage') {
      uplinkRequests.push(url);
      return route.fulfill({ json: { type: 'FeatureCollection', features: [cell(5.38, 51.0, 'both'), cell(5.45, 51.0, 'observed')] } });
    }
    return route.fulfill({ json: {} });
  });
  await page.goto(BASE + '/#/rx-coverage' + (hash || ''));
  await page.waitForSelector('#rxMap.leaflet-container', { timeout: 15000 });
  return { page, errors, uplinkRequests };
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  console.log(`\n=== RX coverage uplink layer E2E against ${BASE} ===`);

  await step('no Uplink button without clientUplink', async () => {
    const { page } = await openPage(browser, { uplink: false });
    assert.strictEqual(await page.$('#rxLayerBar button[data-layer="uplink"]'), null);
    await page.close();
  });

  await step('the Uplink button draws coloured cells with the uplink legend', async () => {
    const { page, errors, uplinkRequests } = await openPage(browser, { uplink: true });
    await page.click('#rxLayerBar button[data-layer="uplink"]');
    await page.waitForFunction(() => document.querySelectorAll('#rxMap path.leaflet-interactive').length === 2, null, { timeout: 10000 });
    assert.ok(uplinkRequests.length >= 1 && /bbox=/.test(uplinkRequests[0]), 'requested /api/uplink-coverage with a bbox');
    const legend = await page.$eval('#rxLegend', el => el.textContent);
    assert.ok(legend.includes('both directions') && legend.includes('not heard'), legend);
    const fills = await page.$$eval('#rxMap path.leaflet-interactive', els => els.map(e => e.getAttribute('fill')));
    assert.notStrictEqual(fills[0], fills[1], 'both and observed cells differ in colour');
    assert.ok(/layer=uplink/.test(await page.evaluate(() => location.hash)), 'layer is in the URL');
    assert.deepStrictEqual(errors, []);
    await page.close();
  });

  await step('a layer=uplink deep link opens on the uplink layer', async () => {
    const { page, uplinkRequests } = await openPage(browser, { uplink: true, hash: '?layer=uplink&days=7' });
    await page.waitForFunction(() => document.querySelectorAll('#rxMap path.leaflet-interactive').length === 2, null, { timeout: 10000 });
    assert.ok(uplinkRequests.length >= 1);
    await page.close();
  });

  await step('the uplink layer sends the observer filter and My coverage', async () => {
    const rx = 'cd'.repeat(32);
    const { page, errors, uplinkRequests } = await openPage(browser, {
      uplink: true, hash: '?layer=uplink&rx=' + rx,
      user: { id: 1, email: 'alice@example.org', displayName: 'Alice', role: 'user', csrfToken: 't' },
    });
    await page.waitForSelector('#rxMineBar button[data-mine]', { timeout: 10000 });
    for (let i = 0; i < 50 && !uplinkRequests.length; i++) await page.waitForTimeout(100);
    assert.ok(uplinkRequests.length >= 1, 'a ?layer=uplink link must fetch the uplink layer');
    assert.strictEqual(new URL(uplinkRequests[uplinkRequests.length - 1]).searchParams.get('rx'), rx, 'rx is sent to /api/uplink-coverage');
    const before = uplinkRequests.length;
    await page.click('#rxMineBar button[data-mine]');
    for (let i = 0; i < 50 && uplinkRequests.length === before; i++) await page.waitForTimeout(100);
    assert.ok(uplinkRequests.length > before, 'toggling My coverage refetches the uplink layer');
    const last = new URL(uplinkRequests[uplinkRequests.length - 1]).searchParams;
    assert.strictEqual(last.get('mine'), '1', 'mine=1 is sent to /api/uplink-coverage');
    assert.strictEqual(last.get('rx'), rx, 'the observer filter stays on');
    assert.deepStrictEqual(errors, []);
    await page.close();
  });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
