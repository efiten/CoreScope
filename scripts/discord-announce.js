#!/usr/bin/env node
/* discord-announce.js — FORK-LOCAL. Post a Dutch release note to Discord.
 *
 * Reads docs/release-notes/nl/<tag>.md, which a human wrote, and posts it as one
 * embed to the webhook in DISCORD_ANALYZER_WEBHOOK. Nothing here generates or
 * translates text: the note is posted verbatim or not at all.
 *
 * Usage:
 *   node scripts/discord-announce.js --tag v3.12.0-on8ar.2 [--dry-run]
 *
 * The webhook URL is a credential. It is read from the environment, never
 * logged, and never included in an error message, because a failing job's log is
 * readable. Requiring this file does nothing; main() runs only as a script.
 *
 * Spec: docs/superpowers/specs/2026-09-27-discord-announce-design.md
 */
'use strict';
const fs = require('fs');
const path = require('path');

const EMBED_DESCRIPTION_LIMIT = 4096;
const COLOR = 3447003;
const ANALYZER_URL = 'https://analyzer.on8ar.eu';
const SAFE_TAG = /^[A-Za-z0-9._-]+$/;

// noteFileFor bounds the path: the tag becomes a file name, so it may not carry
// a separator or a parent reference. Tags are ours, but a path built from input
// stays bounded anyway.
function noteFileFor(repoRoot, tag) {
  if (typeof tag !== 'string' || !SAFE_TAG.test(tag) || tag.includes('..')) {
    throw new Error('refusing to build a path from tag ' + JSON.stringify(tag));
  }
  return path.join(repoRoot, 'docs', 'release-notes', 'nl', tag + '.md');
}

// readNote also normalises what a Windows editor leaves behind: a leading BOM
// would render as a stray character at the top of a public announcement, and a
// CR would travel into the embed.
function readNote(file) {
  if (!fs.existsSync(file)) {
    throw new Error('note not found: ' + file +
      '\nWrite the Dutch note for this tag before tagging, or this deploy goes unannounced.');
  }
  const raw = fs.readFileSync(file, 'utf8').replace(/^﻿/, '').replace(/\r\n/g, '\n').trim();
  if (!raw) throw new Error('note is empty: ' + file);
  return raw;
}

function buildPayload(tag, body) {
  if (body.length > EMBED_DESCRIPTION_LIMIT) {
    throw new Error('note is too long for one embed: ' + body.length +
      ' characters, limit is ' + EMBED_DESCRIPTION_LIMIT + '. Shorten it; it will not be truncated.');
  }
  return {
    embeds: [{
      title: 'CoreScope ' + tag + ' staat live',
      description: body,
      url: ANALYZER_URL,
      color: COLOR,
    }],
  };
}

async function post(webhookUrl, payload) {
  // wait=true so Discord returns the created message instead of an empty 204,
  // which lets the caller assert the message exists rather than trust a status.
  const res = await fetch(webhookUrl + '?wait=true', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  const text = await res.text();
  if (!res.ok) {
    // Discord's body is safe to show; the URL is not, and is deliberately absent.
    throw new Error('Discord answered HTTP ' + res.status + ': ' + text.slice(0, 500));
  }
  return JSON.parse(text);
}

async function main(argv) {
  const tagIdx = argv.indexOf('--tag');
  const tag = tagIdx !== -1 ? argv[tagIdx + 1] : '';
  const dryRun = argv.includes('--dry-run');
  if (!tag) throw new Error('usage: node scripts/discord-announce.js --tag <tag> [--dry-run]');

  const repoRoot = path.resolve(__dirname, '..');
  const payload = buildPayload(tag, readNote(noteFileFor(repoRoot, tag)));

  if (dryRun) {
    console.log('DRY RUN, posting nothing. Payload:');
    console.log(JSON.stringify(payload, null, 2));
    return;
  }
  const url = process.env.DISCORD_ANALYZER_WEBHOOK;
  if (!url) throw new Error('DISCORD_ANALYZER_WEBHOOK is not set');
  const msg = await post(url, payload);
  console.log('Posted message ' + msg.id + ' to channel ' + msg.channel_id);
}

module.exports = { noteFileFor, readNote, buildPayload, post, EMBED_DESCRIPTION_LIMIT };

if (require.main === module) {
  main(process.argv.slice(2)).catch((err) => { console.error(err.message); process.exit(1); });
}
