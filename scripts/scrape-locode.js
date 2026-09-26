#!/usr/bin/env node
/* scrape-locode.js — FORK-LOCAL. Refresh public/locode.json from UN/LOCODE.
 *
 * Source: the CSV distribution of UN/LOCODE, via the datasets/un-locode mirror.
 *
 * This used to scrape service.unece.org/trade/locode/<cc>.htm and parse the HTML
 * tables. That stopped working: the host now answers 403 to a plain client (a
 * Cloudflare human check, which a browser passes and this does not), so the data
 * had quietly frozen at whatever the last successful run produced. The CSV mirror
 * answers 200, carries the same 2025-1 edition, and needs no HTML parsing.
 *
 * It also carries a Coordinates column, which the HTML route never gave us. Those
 * go to a SEPARATE public/locode-coords.json rather than into locode.json: the
 * coordinates for these five countries are a few hundred KB and only a page that
 * resolves a location from GPS needs them, so folding them in would grow an asset
 * every page already loads.
 *
 * MERGES, does not overwrite. The old version wrote {countries, types, locations}
 * over the whole file while locode.json also holds `regions`, `operators` and
 * `power`, which are maintained by hand and appear in no source: 35 region codes
 * across 3 countries, plus 3 operator kinds and 3 power kinds, all of which a
 * successful run would have silently deleted. Those are read back and kept.
 *
 * Usage: node scripts/scrape-locode.js [--dry-run]
 */
'use strict';
const https = require('https');
const path = require('path');
const fs = require('fs');

const CSV_URL = 'https://raw.githubusercontent.com/datasets/un-locode/main/data/code-list.csv';

// Unchanged from the HTML version on purpose, so this is a data refresh and not
// also a scope change. Luxembourg borders the mesh and has 165 coded places with
// coordinates; adding it is a separate decision.
const COUNTRIES = {
  BE: 'Belgium',
  NL: 'Netherlands',
  DE: 'Germany',
  GB: 'United Kingdom',
  FR: 'France',
};

// MeshCore's own naming convention, not part of UN/LOCODE.
const TYPES = {
  COR: 'Core Repeater',
  DIS: 'Distribution',
  EDG: 'Edge',
  MOB: 'Mobile',
  EBP: 'Edge · Battery Powered',
  ESP: 'Edge · Solar Powered',
  EMP: 'Edge · Mains Powered',
  RP:  'Repeater',
};

function get(url) {
  return new Promise((resolve, reject) => {
    https.get(url, { headers: { 'User-Agent': 'corescope-locode-refresh' } }, res => {
      if (res.statusCode !== 200) {
        res.resume();
        reject(new Error(url + ' answered HTTP ' + res.statusCode));
        return;
      }
      const chunks = [];
      res.on('data', c => chunks.push(c));
      res.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    }).on('error', reject);
  });
}

// Minimal RFC 4180 reader. The Name column carries commas and quoted strings, so
// splitting on commas drops rows without saying so.
function parseCsv(text) {
  const rows = [];
  let row = [], field = '', quoted = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quoted) {
      if (c === '"') {
        if (text[i + 1] === '"') { field += '"'; i++; }
        else quoted = false;
      } else field += c;
    } else if (c === '"') {
      quoted = true;
    } else if (c === ',') {
      row.push(field); field = '';
    } else if (c === '\n') {
      row.push(field); field = '';
      if (row.length > 1 || row[0] !== '') rows.push(row);
      row = [];
    } else if (c !== '\r') {
      field += c;
    }
  }
  if (field !== '' || row.length) { row.push(field); rows.push(row); }
  return rows;
}

// "5113N 00425E" -> [51.2167, 4.4167]. Degrees and minutes, so the source is
// precise to one arc-minute, about 1.85 km of latitude. Anything built on these
// inherits that: they identify a town, not a spot.
function toDecimal(coords) {
  const m = /^(\d{2})(\d{2})([NS])\s+(\d{3})(\d{2})([EW])$/.exec((coords || '').trim());
  if (!m) return null;
  let lat = Number(m[1]) + Number(m[2]) / 60;
  let lon = Number(m[4]) + Number(m[5]) / 60;
  if (m[3] === 'S') lat = -lat;
  if (m[6] === 'W') lon = -lon;
  return [Number(lat.toFixed(4)), Number(lon.toFixed(4))];
}

async function main() {
  const dryRun = process.argv.includes('--dry-run');
  const outPath = path.join(__dirname, '..', 'public', 'locode.json');
  const coordsPath = path.join(__dirname, '..', 'public', 'locode-coords.json');

  // Read first: regions/operators/power exist only here.
  let existing = {};
  try {
    existing = JSON.parse(fs.readFileSync(outPath, 'utf8'));
  } catch (e) {
    console.log('No existing ' + outPath + ' (' + (e.code || e.message) + '); writing a fresh one.');
  }

  console.log('Fetching ' + CSV_URL + ' ...');
  const rows = parseCsv(await get(CSV_URL));
  const header = rows.shift() || [];
  const col = {};
  header.forEach((h, i) => { col[h.trim()] = i; });
  for (const need of ['Country', 'Location', 'Name', 'Coordinates']) {
    if (col[need] === undefined) {
      throw new Error('CSV has no ' + need + ' column; header was: ' + header.join(','));
    }
  }
  console.log('  ' + rows.length + ' rows, columns: ' + header.join(', '));

  const locations = {};
  const coords = {};
  let skipped = 0, collapsed = 0;
  for (const r of rows) {
    const cc = (r[col.Country] || '').trim();
    if (!COUNTRIES[cc]) continue;
    const loc = (r[col.Location] || '').trim();
    if (!/^[A-Z0-9]{2,3}$/.test(loc)) continue;
    const name = (r[col.Name] || '').trim();
    // '=' marks a cross-reference row and '.' a placeholder, the same filter the
    // HTML version applied.
    if (!name || name.startsWith('=') || name === '.') { skipped++; continue; }
    const bucket = (locations[cc] = locations[cc] || {});
    if (bucket[loc] === undefined) {
      bucket[loc] = name;
    } else if (name !== bucket[loc]) {
      // 22 codes in these five countries appear twice, all Belgian bilingual
      // listings of one place with the languages swapped: "Brussel (Bruxelles)"
      // against "Bruxelles (Brussel)", same coordinates, same status. Either is
      // correct, so the choice only has to be reproducible: taking whichever row
      // came last would change the committed file if the source ever reorders.
      // Alphabetical does not depend on row order.
      collapsed++;
      if (name < bucket[loc]) bucket[loc] = name;
    }
    const d = toDecimal(r[col.Coordinates]);
    if (d && (coords[cc] = coords[cc] || {})[loc] === undefined) coords[cc][loc] = d;
  }

  for (const cc of Object.keys(COUNTRIES)) {
    const n = Object.keys(locations[cc] || {}).length;
    const c = Object.keys(coords[cc] || {}).length;
    const before = Object.keys((existing.locations || {})[cc] || {}).length;
    console.log('  ' + cc + ': ' + n + ' locations (was ' + before + '), ' +
      c + ' with coordinates (' + (n ? Math.round((100 * c) / n) : 0) + '%)');
  }
  if (skipped) console.log('  skipped ' + skipped + ' cross-reference or placeholder rows');
  if (collapsed) console.log('  collapsed ' + collapsed + ' duplicate codes to the alphabetically first name (bilingual listings)');

  // Merge: replace what this source owns, keep everything it does not.
  const merged = Object.assign({}, existing, {
    countries: COUNTRIES,
    types: TYPES,
    locations: locations,
  });
  for (const key of ['regions', 'operators', 'power']) {
    if (existing[key]) {
      merged[key] = existing[key];
      console.log('  preserved ' + key + ': ' + Object.keys(existing[key]).length + ' entries');
    } else {
      console.log('  NOTE: no ' + key + ' in the existing file, so none written');
    }
  }

  if (dryRun) {
    console.log('\n--dry-run: nothing written.');
    return;
  }
  fs.writeFileSync(outPath, JSON.stringify(merged, null, 2));
  fs.writeFileSync(coordsPath, JSON.stringify(coords));
  const kb = (p) => Math.round(fs.statSync(p).size / 1024);
  console.log('\nWritten: ' + outPath + ' (' + kb(outPath) + ' KB)');
  console.log('Written: ' + coordsPath + ' (' + kb(coordsPath) + ' KB)');
}

// Exported for tests/unit/test-scrape-locode.js. main() runs only as a script, so
// requiring this file parses no CSV and fetches nothing.
module.exports = { parseCsv, toDecimal, COUNTRIES, TYPES, CSV_URL };

if (require.main === module) {
  main().catch(err => { console.error(err); process.exit(1); });
}
