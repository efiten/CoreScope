/**
 * Measured links layer on the map (#1865).
 * Route-mocks /api/nodes and /api/links/measured with four nodes and three pairs
 * (two-way verified good, one-way verified, two-way unverified weak) and checks
 * the toggle, the hash state, the colour classes, the dashes, the popup and that
 * nothing is fetched while the layer is off.
 * Usage: BASE_URL=http://localhost:13581 node tests/e2e/test-map-measured-links-e2e.js
 * Defaults to localhost:3000, never point it at a public instance.
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';
const pk = (c) => c.repeat(64);
const NODES = [
  { public_key: pk('a'), name: 'Node-A', role: 'repeater', lat: 50.80, lon: 4.30 },
  { public_key: pk('b'), name: 'Node-B', role: 'repeater', lat: 50.85, lon: 4.40 },
  { public_key: pk('c'), name: 'Node-C<i>', role: 'repeater', lat: 50.90, lon: 4.35 },
  { public_key: pk('e'), name: 'Node-E', role: 'repeater', lat: 50.70, lon: 4.25 },
  { public_key: pk('d'), name: 'Node-D', role: 'companion', lat: 50.75, lon: 4.45 },
].map((n) => Object.assign({ last_seen: new Date().toISOString(), advert_count: 1 }, n));
const LINKS = { links: [
  { a: pk('a'), b: pk('b'), snrAB: 8, snrBA: 6.5, verified: true, ageSec: 120, ageSecAB: 120, ageSecBA: 7300 },
  { a: pk('a'), b: pk('c'), snrAB: null, snrBA: -2, verified: true, ageSec: 3700, ageSecAB: null, ageSecBA: 3700 },
  { a: pk('b'), b: pk('d'), snrAB: -9, snrBA: 1, verified: false, ageSec: 90000, ageSecAB: 90000, ageSecBA: 90000 },
  { a: pk('a'), b: pk('e'), snrAB: null, snrBA: null, verified: true, ageSec: 30, ageSecAB: 30, ageSecBA: null },
] };

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

  let linkFetches = 0;
  await page.route(/\/api\/links\/measured/, (r) => {
    linkFetches++;
    r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(LINKS) });
  });
  await page.route(/\/api\/nodes\?/, (r) => r.fulfill({ status: 200, contentType: 'application/json',
    body: JSON.stringify({ nodes: NODES, total: NODES.length, has_more: false, counts: { repeaters: 4, rooms: 0, companions: 1, sensors: 0 } }) }));

  const paths = 'path.ml-link';
  const hash = () => page.evaluate(() => location.hash);
  const waitMap = async () => {
    await page.waitForSelector('#mcLinks', { state: 'attached', timeout: 20000 });
    await page.waitForSelector('.leaflet-container', { timeout: 20000 });
  };
  const waitPaths = (n) => page.waitForFunction(([s, k]) => document.querySelectorAll(s).length === k, [paths, n], { timeout: 10000 });
  const classes = () => page.locator(paths).evaluateAll((els) => els.map((e) => (e.getAttribute('class') || '').split(/\s+/)));

  console.log(`Measured links map E2E against ${BASE}`);
  await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
  await waitMap();
  await page.waitForTimeout(1000);

  await test('off by default: unchecked, no paths, no fetch', async () => {
    assert(!(await page.locator('#mcLinks').isChecked()), 'checked');
    assert((await page.locator(paths).count()) === 0, 'paths drawn');
    assert(linkFetches === 0, 'fetched ' + linkFetches);
  });

  await test('toggle on draws four paths, sets links=1 in the hash, fetches once', async () => {
    await page.locator('#mcLinks').check();
    await waitPaths(4);
    assert((await hash()).includes('links=1'), 'hash ' + (await hash()));
    assert(linkFetches === 1, 'fetches ' + linkFetches);
  });

  await test('colour classes follow the weaker SNR', async () => {
    const cls = await classes();
    const has = (c) => cls.filter((x) => x.includes(c)).length;
    assert(has('ml-good') === 1 && has('ml-fair') === 1 && has('ml-poor') === 2, JSON.stringify(cls));
  });

  await test('three paths are dashed: one-way, unverified and no SNR', async () => {
    const cls = await classes();
    assert(cls.filter((x) => x.includes('ml-dashed')).length === 3, JSON.stringify(cls));
    const dashes = await page.locator(paths).evaluateAll((els) => els.map((e) => getComputedStyle(e).strokeDasharray));
    assert(dashes.filter((d) => d && d !== 'none').length === 3, dashes.join(' | '));
  });

  await test('stroke colour comes from the CSS variable', async () => {
    const good = await page.locator('path.ml-good').first().evaluate((e) => getComputedStyle(e).stroke);
    const poor = await page.locator('path.ml-poor').first().evaluate((e) => getComputedStyle(e).stroke);
    assert(good && poor && good !== poor && good !== 'none', good + ' / ' + poor);
  });

  await test('popup shows both SNRs, age, verified label, and escapes names', async () => {
    await page.locator('path.ml-fair').first().dispatchEvent('click');
    await page.waitForSelector('.leaflet-popup-content', { timeout: 5000 });
    const html = await page.locator('.leaflet-popup-content').innerHTML();
    const t = await page.locator('.leaflet-popup-content').innerText();
    assert(t.includes('Node-A') && t.includes('Node-C<i>'), t);
    assert(!html.includes('<i>'), 'unescaped name');
    assert(/Node-A hears Node-C<i>: not measured\s*$/m.test(t), 'unmeasured direction carries no age: ' + t);
    assert(/Node-C<i> hears Node-A: -2\.0 dB, reported 1 h ago/.test(t), 'measured direction with its age: ' + t);
    assert(t.includes('verified') && !t.includes('not verified'), t);
  });

  await test('popup shows the age of each direction', async () => {
    await page.locator('path.ml-good').first().dispatchEvent('click');
    await page.waitForFunction(() => /Node-B hears/.test((document.querySelector('.leaflet-popup-content') || {}).innerText || ''), null, { timeout: 5000 });
    const t = await page.locator('.leaflet-popup-content').innerText();
    assert(/Node-A hears Node-B: 8\.0 dB, reported 2 min ago/.test(t), t);
    assert(/Node-B hears Node-A: 6\.5 dB, reported 2 h ago/.test(t), t);
  });

  await test('a pair with no SNR is poor and dashed, popup says No SNR', async () => {
    const cls = await classes();
    const both = cls.filter((x) => x.includes('ml-poor') && x.includes('ml-dashed'));
    assert(both.length === 2, JSON.stringify(cls));
    const idx = cls.findIndex((x, i) => x.includes('ml-poor') && i === 3);
    await page.locator(paths).nth(3).dispatchEvent('click');
    await page.waitForFunction(() => /No SNR/.test((document.querySelector('.leaflet-popup-content') || {}).innerText || ''), null, { timeout: 5000 });
    assert(idx === 3, 'order');
  });

  await test('filtering a role out removes the pair to that node, with no refetch', async () => {
    const before = linkFetches;
    await page.locator('input[data-role="companion"]').uncheck();
    await waitPaths(3);
    assert(linkFetches === before, 'refetched');
    await page.locator('input[data-role="companion"]').check();
    await waitPaths(4);
  });

  await test('reload with the hash restores the layer', async () => {
    await page.reload({ waitUntil: 'domcontentloaded' });
    await waitMap();
    await waitPaths(4);
    assert(await page.locator('#mcLinks').isChecked(), 'checkbox');
  });

  await test('toggle off removes the paths and the hash flag', async () => {
    await page.locator('#mcLinks').uncheck();
    await waitPaths(0);
    assert(!(await hash()).includes('links=1'), 'hash still set');
  });

  await test('toggling on again reuses the cached response', async () => {
    const before = linkFetches;
    await page.locator('#mcLinks').check();
    await waitPaths(4);
    assert(linkFetches === before, 'refetched');
  });

  await browser.close();
  const failed = results.filter((x) => !x).length;
  console.log(`${results.length - failed} passed, ${failed} failed`);
  if (failed) process.exit(1);
})().catch((e) => { console.error(e); process.exit(1); });
