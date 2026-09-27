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
  noteFileFor, readNote, buildPayload, post, resolveWebhook, parseArgs, main, EMBED_DESCRIPTION_LIMIT,
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

console.log('\n=== discord-announce: the webhook from the environment ===');

test('an absent, empty or whitespace-only secret is an error', () => {
  // A dry run resolves this too, so a dry run proves the secret is wired without
  // posting. Without it there is no way to test the wiring except by posting.
  for (const env of [{}, { DISCORD_ANALYZER_WEBHOOK: '' }, { DISCORD_ANALYZER_WEBHOOK: '  \n ' }]) {
    assert.throws(() => resolveWebhook(env), /not set/i, 'accepted ' + JSON.stringify(env));
  }
});

test('a URL that is not a Discord webhook endpoint is refused', () => {
  // The run on 2026-09-27 got an HTML page back, which means the request never
  // reached the API. Checking the shape up front turns that into a clear message
  // before anything is sent, and the message never echoes the value.
  for (const bad of [
    'https://discord.com/channels/123/456',
    'https://example.test/hook/abc',
    'discord.com/api/webhooks/1/abc',
    'https://discord.com/api/webhook/1/abc',
  ]) {
    const err = (() => { try { resolveWebhook({ DISCORD_ANALYZER_WEBHOOK: bad }); } catch (e) { return e; } })();
    assert.ok(err, 'accepted ' + bad);
    assert.ok(/webhook/i.test(err.message), 'unclear message for ' + bad + ': ' + err.message);
    assert.ok(!err.message.includes(bad), 'the value was echoed back: ' + err.message);
  }
});

test('both Discord hostnames are accepted', () => {
  for (const good of [
    'https://discord.com/api/webhooks/1553507181817765898/TOKEN',
    'https://discordapp.com/api/webhooks/1/TOKEN',
    'https://discord.com/api/v10/webhooks/1/TOKEN',
  ]) {
    assert.strictEqual(resolveWebhook({ DISCORD_ANALYZER_WEBHOOK: good }), good, 'refused ' + good);
  }
});

test('surrounding whitespace is trimmed off the URL', () => {
  // A secret pasted with a trailing newline would otherwise become
  // "...token\n?wait=true", which fails with a confusing error instead of a clear
  // one.
  const url = 'https://discord.com/api/webhooks/1/TOKEN';
  assert.strictEqual(resolveWebhook({ DISCORD_ANALYZER_WEBHOOK: url + '\n' }), url);
  assert.strictEqual(resolveWebhook({ DISCORD_ANALYZER_WEBHOOK: '  ' + url + ' \r\n' }), url);
});

console.log('\n=== discord-announce: the argument gate ===');

test('the dry-run flag is read from argv, both ways', () => {
  // The only thing between a test run and a public post. A regression that
  // hardcodes either direction is silent: every tag push would post nothing, or
  // every preview would post for real.
  assert.deepStrictEqual(parseArgs(['--tag', 'v1.0.0-on8ar.1']), { tag: 'v1.0.0-on8ar.1', dryRun: false });
  assert.deepStrictEqual(parseArgs(['--tag', 'v1.0.0-on8ar.1', '--dry-run']), { tag: 'v1.0.0-on8ar.1', dryRun: true });
  assert.deepStrictEqual(parseArgs(['--dry-run', '--tag', 'v1.0.0-on8ar.1']), { tag: 'v1.0.0-on8ar.1', dryRun: true });
});

test('a missing tag is refused', () => {
  assert.throws(() => parseArgs([]), /usage/i);
  assert.throws(() => parseArgs(['--dry-run']), /usage/i);
});

console.log('\n=== discord-announce: the workflow keeps two promises ===');

const WORKFLOW = fs.readFileSync(
  path.join(__dirname, '..', '..', '.github', 'workflows', 'discord-announce.yml'), 'utf8');

test('the checkout does not use the tag as its ref', () => {
  // The tag being announced generally predates the announcer: v3.12.0-on8ar.2
  // points at a commit with no scripts/discord-announce.js in it, so checking
  // out the tag makes the job die on "Cannot find module". The note is a
  // communication artifact that lives on the default branch, and the tag is
  // only its name.
  assert.ok(!/ref:\s*\$\{\{\s*steps\.t\.outputs\.tag\s*\}\}/.test(WORKFLOW),
    'the workflow checks out the tag again; a tag older than this feature cannot be announced');
});

test('the dry-run flag fails safe, not open', () => {
  // This guards a channel real people read. Any value that is not exactly
  // "false" must mean dry, so an unexpected input cannot post.
  assert.ok(/IN_DRY"\s*!=\s*"false"/.test(WORKFLOW),
    'dry_run is compared for equality with "true", so an unexpected value posts for real');
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

  await asyncTest('the request really is a POST of JSON, and carries the payload', async () => {
    // Asserting on JSON.parse(JSON.stringify(x)) tests the JSON module, not this
    // code. The rule can only be broken in the request itself, so the request is
    // what gets inspected: without this, `body: String(payload)` sends the literal
    // "[object Object]" and every test still passes.
    const payload = buildPayload('v1.0.0-on8ar.1', 'Een "citaat", een \\ backslash,\nregel en 📡.');
    let seen = null;
    global.fetch = async (url, init) => {
      seen = init;
      return { ok: true, status: 200, text: async () => '{"id":"1","channel_id":"2"}' };
    };
    await post('https://discord.test/hook/T', payload);
    assert.strictEqual(seen.method, 'POST');
    assert.strictEqual(seen.headers['Content-Type'], 'application/json');
    assert.deepStrictEqual(JSON.parse(seen.body), payload, 'the payload did not survive into the request body');
  });

  await asyncTest('a fetch that throws does not carry the URL into the message', async () => {
    // Reached when the secret is stored without a scheme: fetch throws
    // "Failed to parse URL from <the whole credential>". GitHub masks secrets in
    // its logs, but this repository is public and that protection is GitHub's,
    // not this code's.
    global.fetch = async (url) => { throw new TypeError('Failed to parse URL from ' + url); };
    const err = await post('discord.test/api/webhooks/123/SECRET-TOKEN', { embeds: [] }).catch((e) => e);
    assert.ok(!String(err.message).includes('SECRET-TOKEN'), 'the token reached the message: ' + err.message);
    assert.ok(/request failed/i.test(err.message), 'the error should still say what happened: ' + err.message);
  });

  await asyncTest('a 2xx that is not JSON says what came back, without the URL', async () => {
    // Hit for real on 2026-09-27: Discord answered 200 with an HTML page and
    // JSON.parse threw "Unexpected token '<'", which says nothing about the
    // status, the endpoint or whether anything was posted.
    global.fetch = async () => ({
      ok: true, status: 200,
      text: async () => '<!DOCTYPE html><html><head><title>Cloudflare</title></head></html>',
    });
    const err = await post('https://discord.test/hook/SECRET-TOKEN', { embeds: [] }).catch((e) => e);
    assert.ok(/200/.test(err.message), 'the status belongs in the message: ' + err.message);
    assert.ok(/not JSON|DOCTYPE/i.test(err.message), 'say what came back instead: ' + err.message);
    assert.ok(!err.message.includes('SECRET-TOKEN'), 'the token reached the message: ' + err.message);
  });

  console.log('\n=== discord-announce: the dry-run gate in main ===');

  await asyncTest('a dry run posts nothing, whatever fetch would have done', async () => {
    let called = false;
    global.fetch = async () => { called = true; throw new Error('a dry run must not reach the network'); };
    const lines = [];
    await main(['--tag', 'v3.12.0-on8ar.2', '--dry-run'], { DISCORD_ANALYZER_WEBHOOK: 'https://x/y' }, (l) => lines.push(l));
    assert.strictEqual(called, false, 'a dry run called fetch');
    assert.ok(lines.join('\n').includes('DRY RUN'), 'a dry run should say so: ' + lines.join(' | '));
  });

  await asyncTest('a dry run still previews when the secret is absent', async () => {
    // The spec promises any note can be previewed before it goes out. Requiring
    // the secret for that would break previewing on any machine without it.
    const lines = [];
    await main(['--tag', 'v3.12.0-on8ar.2', '--dry-run'], {}, (l) => lines.push(l));
    const out = lines.join('\n');
    assert.ok(out.includes('DRY RUN'), 'the preview did not run');
    assert.ok(/absent|not set/i.test(out), 'the preview should report the missing secret: ' + out);
  });

  await asyncTest('a dry run reports the secret as present without describing it', async () => {
    // "present" carries the same wiring proof as a length would, and a length is
    // a property of a credential in a world-readable log.
    const lines = [];
    await main(['--tag', 'v3.12.0-on8ar.2', '--dry-run'], { DISCORD_ANALYZER_WEBHOOK: 'https://a/bcdef' }, (l) => lines.push(l));
    const out = lines.join('\n');
    assert.ok(/present/i.test(out), 'the preview should confirm the secret is wired: ' + out);
    assert.ok(!/\b15\b|characters/i.test(out), 'the preview described the credential: ' + out);
  });

  await asyncTest('a real run without the secret fails instead of posting', async () => {
    global.fetch = async () => { throw new Error('must not be reached'); };
    await assert.rejects(
      () => main(['--tag', 'v3.12.0-on8ar.2'], {}, () => {}),
      /not set/i);
  });

  global.fetch = realFetch;
  console.log(`\nTotal: ${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
})();
