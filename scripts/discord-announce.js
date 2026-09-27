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
const HEALTH_URL = ANALYZER_URL + '/api/health';
const SAFE_TAG = /^[A-Za-z0-9._-]+$/;
// discord.com and the legacy discordapp.com, with an optional /vN API version.
const WEBHOOK_URL = /^https:\/\/(discord|discordapp)\.com\/api\/(v\d+\/)?webhooks\/\d+\/[\w-]+$/;

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
  let res;
  try {
    res = await fetch(webhookUrl + '?wait=true', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  } catch (e) {
    // fetch puts the URL in its own message: a secret stored without a scheme
    // gives "Failed to parse URL from <the whole credential>". GitHub masks
    // secrets in its logs, but this repository is public and that protection is
    // GitHub's rather than this code's, so the URL is dropped here.
    throw new Error('request failed before Discord answered: ' + ((e && e.name) || 'Error'));
  }
  const text = await res.text();
  if (!res.ok) {
    // Discord's body is safe to show; the URL is not, and is deliberately absent.
    throw new Error('Discord answered HTTP ' + res.status + ': ' + text.slice(0, 500));
  }
  try {
    return JSON.parse(text);
  } catch (e) {
    // Hit on 2026-09-27: a 200 carrying an HTML page, where JSON.parse's own
    // "Unexpected token '<'" said nothing about the status, the endpoint, or
    // whether anything had been posted. Report both, and never the URL.
    throw new Error('Discord answered HTTP ' + res.status +
      ' but the body is not JSON, so it is unclear whether anything was posted: ' +
      text.slice(0, 200));
  }
}

// resolveWebhook trims, because a secret pasted with a trailing newline would
// otherwise become "...token\n?wait=true" and fail with a confusing error rather
// than a clear one. The error never echoes the rejected value: it is the
// credential, and a failing job's log is readable.
function resolveWebhook(env) {
  const raw = (env && env.DISCORD_ANALYZER_WEBHOOK) || '';
  const url = String(raw).trim();
  if (!url) throw new Error('DISCORD_ANALYZER_WEBHOOK is not set');
  // Checked before anything is sent, because the failure otherwise arrives as an
  // HTML page parsed as JSON, long after the request left. The value is never
  // echoed back: it is the credential, and this log is world-readable.
  if (!WEBHOOK_URL.test(url)) {
    throw new Error('DISCORD_ANALYZER_WEBHOOK is not a Discord webhook endpoint. ' +
      'Expected https://discord.com/api/webhooks/<id>/<token>, optionally with an ' +
      'API version, and the whole URL rather than the token alone.');
  }
  return url;
}

// assertLiveVersion refuses to say "staat live" about something that is not.
//
// The tag push and the deploy are separate acts: deploy.yml triggers on pushes to
// master, while the live deploy is the manual /root/bin/deploy-live.sh. Nothing
// orders them, so a tag pushed before the deploy would otherwise announce a
// version that is not serving. /api/health reports the running version, so the
// claim can be checked instead of assumed.
//
// Anything unreadable also stops it: not being able to confirm what is live is
// not permission to say it is.
function assertLiveVersion(tag, health) {
  const live = health && typeof health === 'object' ? String(health.version || '') : '';
  if (!live) {
    throw new Error('could not read the running version from ' + HEALTH_URL +
      ', so "' + tag + ' staat live" cannot be confirmed and will not be posted');
  }
  if (live !== tag) {
    throw new Error('refusing to announce ' + tag + ': ' + ANALYZER_URL + ' is running ' + live +
      '. Deploy first, then announce.');
  }
}

async function liveHealth() {
  try {
    const res = await fetch(HEALTH_URL);
    return await res.json();
  } catch (e) {
    return null;
  }
}

// parseArgs is separate and exported because the dry-run flag is the only thing
// between a test run and a post in a channel people read. Hardcoding either
// direction is a silent regression, so both directions are asserted.
function parseArgs(argv) {
  const tagIdx = argv.indexOf('--tag');
  const tag = tagIdx !== -1 ? argv[tagIdx + 1] : '';
  if (!tag || tag.startsWith('--')) {
    throw new Error('usage: node scripts/discord-announce.js --tag <tag> [--dry-run]');
  }
  return { tag: tag, dryRun: argv.includes('--dry-run') };
}

// env and log are injected rather than reached for, so the dry-run gate can be
// tested without touching process state.
async function main(argv, env, log) {
  env = env || process.env;
  log = log || console.log;
  const parsed = parseArgs(argv);
  const repoRoot = path.resolve(__dirname, '..');
  const payload = buildPayload(parsed.tag, readNote(noteFileFor(repoRoot, parsed.tag)));

  if (parsed.dryRun) {
    // A dry run reports whether the secret is wired but does not require it: the
    // spec promises any note can be previewed before it goes out, and demanding
    // the credential for a preview would break that on any machine without it.
    // "present" carries the same proof as a length would, without describing a
    // credential in a world-readable log.
    // The shape is checked here too, so a dry run can tell a well-formed secret
    // from a wrong one WITHOUT posting. Without that, a malformed URL only
    // surfaces as an HTML page parsed as JSON on a real run.
    let status;
    try {
      resolveWebhook(env);
      status = 'present and well-formed';
    } catch (e) {
      status = /not set/.test(e.message) ? 'absent (preview only)' : 'present but NOT a Discord webhook endpoint';
    }
    log('DRY RUN, posting nothing.');
    log('DISCORD_ANALYZER_WEBHOOK: ' + status);
    log(JSON.stringify(payload, null, 2));
    return;
  }
  // The local check first, so a missing secret says so instead of spending a
  // network call to fail on something else.
  const url = resolveWebhook(env);

  // Then the live check, before the post rather than after: an announcement is
  // not retractable.
  assertLiveVersion(parsed.tag, await liveHealth());

  const msg = await post(url, payload);
  log('Posted message ' + msg.id + ' to channel ' + msg.channel_id);
}

module.exports = { noteFileFor, readNote, buildPayload, post, resolveWebhook, parseArgs, main, assertLiveVersion, EMBED_DESCRIPTION_LIMIT };

if (require.main === module) {
  main(process.argv.slice(2), process.env, console.log).catch((err) => { console.error(err.message); process.exit(1); });
}
