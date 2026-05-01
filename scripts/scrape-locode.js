#!/usr/bin/env node
'use strict';
const https = require('https');
const path = require('path');
const fs = require('fs');

const COUNTRIES = {
  BE: 'Belgium',
  NL: 'Netherlands',
  DE: 'Germany',
  GB: 'United Kingdom',
  FR: 'France',
};

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
    https.get(url, res => {
      const chunks = [];
      res.on('data', c => chunks.push(c));
      res.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    }).on('error', reject);
  });
}

function decodeEntities(s) {
  return s
    .replace(/&nbsp;/gi, ' ')
    .replace(/&#(\d+);/g, (_, n) => String.fromCharCode(+n))
    .replace(/&#x([0-9a-f]+);/gi, (_, n) => String.fromCharCode(parseInt(n, 16)))
    .replace(/&amp;/gi, '&')
    .replace(/&lt;/gi, '<')
    .replace(/&gt;/gi, '>')
    .replace(/&quot;/gi, '"')
    .replace(/&apos;/gi, "'");
}

function parsePage(html) {
  const locations = {};
  const rowRe = /<tr[^>]*>([\s\S]*?)<\/tr>/gi;
  let rowMatch;
  while ((rowMatch = rowRe.exec(html)) !== null) {
    const cells = [];
    const cellRe = /<td[^>]*>([\s\S]*?)<\/td>/gi;
    let cellMatch;
    while ((cellMatch = cellRe.exec(rowMatch[1])) !== null) {
      cells.push(decodeEntities(cellMatch[1].replace(/<[^>]+>/g, '')).trim());
    }
    if (cells.length < 3) continue;
    // Cell[1]: "BE ANT" — take location code after country prefix
    // Strip &nbsp; and collapse whitespace before matching
    const locFull = cells[1].replace(/&nbsp;/gi, ' ').replace(/\s+/g, ' ').trim();
    const locMatch = locFull.match(/^[A-Z]{2}\s+([A-Z0-9]{2,3})$/);
    if (!locMatch) continue;
    const loc = locMatch[1];
    const name = cells[2].replace(/&nbsp;/gi, ' ').trim();
    if (!name || name.startsWith('=') || name === '.') continue;
    locations[loc] = name;
  }
  return locations;
}

async function main() {
  const result = { countries: COUNTRIES, types: TYPES, locations: {} };
  for (const cc of Object.keys(COUNTRIES)) {
    console.log(`Fetching ${cc}...`);
    const url = `https://service.unece.org/trade/locode/${cc.toLowerCase()}.htm`;
    const html = await get(url);
    result.locations[cc] = parsePage(html);
    console.log(`  → ${Object.keys(result.locations[cc]).length} locations`);
  }
  const out = path.join(__dirname, '..', 'public', 'locode.json');
  fs.writeFileSync(out, JSON.stringify(result, null, 2));
  console.log(`Written: ${out}`);
}

main().catch(err => { console.error(err); process.exit(1); });
