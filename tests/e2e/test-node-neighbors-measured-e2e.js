/**
 * Measured neighbours on the node page (#1865).
 * Route-mocks /api/nodes/<pk> and /api/nodes/<pk>/neighbors with one measured,
 * one measured-only and one timeout neighbour, then checks the Measured cells,
 * the verified badges, the report summary, the "Floods under" row and axe on
 * the neighbours card.
 * Usage: BASE_URL=http://localhost:13581 node tests/e2e/test-node-neighbors-measured-e2e.js
 * Defaults to localhost:3000, never point it at a public instance.
 */
'use strict';
const { chromium } = require('playwright');
const { AxeBuilder } = require('@axe-core/playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';
const PK = 'aabbccdd11223344556677889900aabbccddeeff00112233445566778899001122';
const NB_MEASURED = '1111111111111111111111111111111111111111111111111111111111111111';
const NB_TIMEOUT = '3333333333333333333333333333333333333333333333333333333333333333';
const NOW = Date.now();
const iso = (agoMs) => new Date(NOW - agoMs).toISOString();

const nodeDetail = {
  node: {
    public_key: PK, name: 'Measured-Test', role: 'repeater', lat: 50.8, lon: 4.3,
    last_seen: iso(60000), first_seen: iso(86400000), advert_count: 3,
    default_flood_scope: '*', default_flood_scope_at: iso(120000),
    neighbor_report: { reportedAt: iso(300000), total: 5, queried: 3, truncated: true, verified: true },
  },
  recentAdverts: [],
};

const neighbors = {
  node: PK,
  neighbors: [
    { pubkey: NB_MEASURED, prefix: '11', name: 'Both-Ways', role: 'repeater', count: 40, score: 0.9, ambiguous: false,
      last_seen: iso(60000), inferred: true,
      measured: { snrIn: 5.5, snrOut: -2, heardAt: iso(300000), reportedAt: iso(300000), verified: true, status: '' } },
    { pubkey: null, prefix: '22', name: null, role: null, count: 0, score: 0, ambiguous: false, inferred: false,
      measured: { snrIn: null, snrOut: 7.25, heardAt: iso(600000), reportedAt: iso(600000), verified: false, status: '' } },
    { pubkey: NB_TIMEOUT, prefix: '33', name: 'Silent-One', role: 'repeater', count: 5, score: 0.4, ambiguous: false,
      last_seen: iso(90000), inferred: true,
      measured: { snrIn: -8, snrOut: null, heardAt: iso(900000), reportedAt: iso(900000), verified: true, status: 'timeout' } },
  ],
  total_observations: 45,
};

const results = [];
async function test(name, fn) {
  try { await fn(); results.push(true); console.log(`  ok   ${name}`); }
  catch (e) { results.push(false); console.log(`  FAIL ${name}: ${e.message}`); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const launchOpts = { headless: true, args: ['--no-sandbox', '--disable-gpu'] };
  if (process.env.CHROMIUM_PATH) launchOpts.executablePath = process.env.CHROMIUM_PATH;
  const browser = await chromium.launch(launchOpts);
  const context = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await context.newPage();

  await page.route(new RegExp('/api/nodes/' + PK + '/neighbors'), (r) =>
    r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(neighbors) }));
  await page.route(new RegExp('/api/nodes/' + PK + '(\\?.*)?$'), (r) =>
    r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(nodeDetail) }));

  console.log(`Measured neighbours E2E against ${BASE}`);
  await page.goto(`${BASE}/#/nodes/${PK}`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#fullNeighborsContent .neighbor-sort-table tbody tr', { timeout: 20000 });

  const rows = '#fullNeighborsContent .neighbor-sort-table tbody tr';
  const rowFor = (text) => page.locator(rows, { hasText: text });

  await test('three neighbour rows, Measured header present', async () => {
    assert((await page.locator(rows).count()) === 3, 'row count');
    assert((await page.locator('#fullNeighborsContent th', { hasText: 'Measured' }).count()) === 1, 'header');
  });

  await test('measured neighbour shows both SNRs and the verified badge', async () => {
    const r = rowFor('Both-Ways');
    const t = await r.innerText();
    assert(t.includes('in 5.5 dB'), 'in: ' + t);
    assert(t.includes('out -2.0 dB'), 'out: ' + t);
    assert((await r.locator('.nb-verified', { hasText: 'verified' }).count()) === 1, 'verified badge');
    assert((await r.locator('.nb-unverified').count()) === 0, 'no unverified badge');
    assert((await r.locator('.nb-snr-fair').count()) === 1, 'weaker side (-2 dB) is the fair band');
  });

  await test('measured-only neighbour is named by prefix, flags the missing side, not verified', async () => {
    const r = rowFor('22…');
    const t = await r.innerText();
    assert(t.includes('(heard only by this neighbour)'), t);
    assert(t.includes('in not measured'), t);
    assert(t.includes('out 7.3 dB') || t.includes('out 7.2 dB'), t);
    assert((await r.locator('.nb-unverified', { hasText: 'not verified' }).count()) === 1, 'not verified badge');
  });

  await test('timeout neighbour explains it does not answer scope queries', async () => {
    const r = rowFor('Silent-One');
    assert((await r.locator('[title="does not answer scope queries"]').count()) === 1, 'title');
    assert((await r.locator('.nb-snr-poor').count()) === 1, 'poor band at -8 dB');
  });

  await test('Measured column sorts by the weaker SNR, and a second click reverses it', async () => {
    const th = page.locator('#fullNeighborsContent th[data-sort-key="measured"]');
    const weakest = async () => (await page.locator(rows).evaluateAll((trs) => trs.map((tr) => {
      const c = tr.querySelector('.nb-measured');
      return c ? c.closest('td').getAttribute('data-value') : '';
    }))).filter((v) => v !== '').map(Number);
    await th.click();
    const first = await weakest();
    assert(first.length === 3, 'three measured values: ' + first.join(','));
    const asc = [-8, -2, 7.25];
    const isAsc = first.join(',') === asc.join(',');
    const isDesc = first.join(',') === asc.slice().reverse().join(',');
    assert(isAsc || isDesc, 'sorted after first click: ' + first.join(','));
    await th.click();
    const second = await weakest();
    assert(second.join(',') === first.slice().reverse().join(','), 'second click reverses: ' + first.join(',') + ' -> ' + second.join(','));
  });

  await test('report summary reads "3 of 5 neighbours queried" and "list cut short"', async () => {
    const t = await page.locator('#fullNeighborsContent .nb-report').innerText();
    assert(t.includes('3 of 5 neighbours queried, reported'), t);
    assert(t.includes('list cut short'), t);
  });

  await test('"Floods under" row shows unscoped for *', async () => {
    const r = page.locator('#row-flood-scope');
    assert((await r.count()) === 1, 'row missing');
    const t = await r.innerText();
    assert(t.includes('Floods under') && t.includes('unscoped'), t);
  });

  await test('axe finds no serious or critical violation in the Measured cells and the report line', async () => {
    // The neighbour name links (#4a9eff on white, 2.7:1) predate this feature and are
    // not part of what it renders, so the scan is scoped to the new elements.
    await page.waitForFunction(() => document.getAnimations().every((a) =>
      a.playState !== 'running' || !a.effect || a.effect.getComputedTiming().endTime === Infinity));
    const res = await new AxeBuilder({ page }).include('.nb-report').include('.nb-measured').withTags(['wcag2a', 'wcag2aa']).analyze();
    const bad = res.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
    assert(bad.length === 0, bad.map((v) => v.id + ' ' + v.nodes.map((n) => n.target.join(' ') + ' ' + JSON.stringify((n.any[0] || {}).data || {})).join('|')).join(', '));
  });

  await browser.close();
  const failed = results.filter((x) => !x).length;
  console.log(`${results.length - failed} passed, ${failed} failed`);
  if (failed) process.exit(1);
})().catch((e) => { console.error(e); process.exit(1); });
