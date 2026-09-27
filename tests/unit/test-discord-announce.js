/* test-discord-announce.js — FORK-LOCAL. The announce script's pure parts.
 *
 * scripts/discord-announce.js posts a Dutch release note to Discord. Requiring it
 * does nothing: main() is guarded by require.main, so these run offline and post
 * nothing. The three cases at the bottom stub the global fetch, so nothing leaves
 * the machine there either.
 */
'use strict';

const assert = require('assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const {
  noteFileFor, readNote, buildPayload, post, EMBED_DESCRIPTION_LIMIT,
} = require('../../scripts/discord-announce.js');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

function tmpNote(contents) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'announce-'));
  const file = path.join(dir, 'note.md');
  fs.writeFileSync(file, contents);
  return file;
}

console.log('\n=== discord-announce: locating the note ===');

test('the note file is the tag verbatim, under docs/release-notes/nl', () => {
  const p = noteFileFor('/repo', 'v3.12.0-on8ar.2');
  assert.strictEqual(p, path.join('/repo', 'docs', 'release-notes', 'nl', 'v3.12.0-on8ar.2.md'));
});

test('a tag cannot escape the notes directory', () => {
  // Review Focus 2. Tags are ours, but a path built from input stays bounded.
  for (const bad of ['../../etc/passwd', 'a/b', '..', 'v1/../..', 'v1\\..\\x']) {
    assert.throws(() => noteFileFor('/repo', bad), /tag/i, 'accepted ' + JSON.stringify(bad));
  }
});

console.log('\n=== discord-announce: reading the note ===');

test('a missing note is an error, not an empty post', () => {
  assert.throws(() => readNote('/does/not/exist.md'), /not found|ENOENT/i);
});

test('an empty or whitespace-only note is an error', () => {
  // Review Focus 4. An empty embed under a confident title is worse than a red run.
  for (const body of ['', '   ', '\n\n', '\r\n \t']) {
    assert.throws(() => readNote(tmpNote(body)), /empty/i, 'accepted ' + JSON.stringify(body));
  }
});

test('a BOM is stripped and CRLF becomes LF', () => {
  // Review Focus 5. These notes are written on Windows.
  const body = readNote(tmpNote('﻿Eerste regel\r\ntweede regel\r\n'));
  assert.ok(!body.startsWith('﻿'), 'BOM survived into the post');
  assert.ok(!body.includes('\r'), 'CR survived into the post');
  assert.strictEqual(body, 'Eerste regel\ntweede regel');
});

console.log('\n=== discord-announce: the payload ===');

test('the title is built from the tag and the body is verbatim', () => {
  const p = buildPayload('v3.12.0-on8ar.2', 'De kolom toont nu de locatie.');
  assert.strictEqual(p.embeds.length, 1);
  assert.strictEqual(p.embeds[0].title, 'CoreScope v3.12.0-on8ar.2 staat live');
  assert.strictEqual(p.embeds[0].description, 'De kolom toont nu de locatie.');
  assert.strictEqual(p.embeds[0].color, 3447003);
  assert.strictEqual(p.embeds[0].url, 'https://analyzer.on8ar.eu');
});

test('quotes, backslashes, newlines and emoji survive a JSON round trip', () => {
  // Review Focus 1. Concatenating this into a request body produces invalid JSON.
  const body = 'Een "citaat", een \\ backslash,\nnieuwe regel en 📡 emoji.';
  const parsed = JSON.parse(JSON.stringify(buildPayload('v1.0.0-on8ar.1', body)));
  assert.strictEqual(parsed.embeds[0].description, body);
});

test('a body over the embed limit is refused, not truncated', () => {
  const tooLong = 'x'.repeat(EMBED_DESCRIPTION_LIMIT + 1);
  assert.throws(() => buildPayload('v1.0.0-on8ar.1', tooLong), /4096|too long/i);
  assert.doesNotThrow(() => buildPayload('v1.0.0-on8ar.1', 'x'.repeat(EMBED_DESCRIPTION_LIMIT)));
});

console.log('\n=== discord-announce: what Discord answers ===');

// These stub the global fetch, so nothing leaves the machine.
async function asyncTest(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

(async () => {
  const realFetch = global.fetch;

  await asyncTest('a non-2xx answer is a failure, not a silent success', async () => {
    // Review Focus 3. A revoked webhook answers 401 and a wrong id answers 404.
    // Returning normally here would leave a green run and no message.
    global.fetch = async () => ({ ok: false, status: 401, text: async () => '{"message":"Invalid Webhook Token"}' });
    await assert.rejects(() => post('https://discord.test/hook/SECRET', { embeds: [] }), /401/);
  });

  await asyncTest('the thrown error never contains the webhook URL', async () => {
    // The URL is the credential, and a failing job's log is readable.
    global.fetch = async () => ({ ok: false, status: 404, text: async () => 'Not Found' });
    const err = await post('https://discord.test/hook/SECRET-TOKEN', { embeds: [] }).catch((e) => e);
    assert.ok(!String(err.message).includes('SECRET-TOKEN'), 'the token leaked into the error: ' + err.message);
  });

  await asyncTest('a 2xx returns the created message, so we can assert it exists', async () => {
    global.fetch = async (url) => {
      assert.ok(url.includes('wait=true'), 'wait=true is what makes Discord return the message');
      return { ok: true, status: 200, text: async () => '{"id":"123","channel_id":"456"}' };
    };
    const msg = await post('https://discord.test/hook/T', { embeds: [] });
    assert.strictEqual(msg.id, '123');
    assert.strictEqual(msg.channel_id, '456');
  });

  global.fetch = realFetch;
  console.log(`\nTotal: ${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
})();
