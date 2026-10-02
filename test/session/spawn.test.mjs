import { alive, cfgWith, fakeDeps, freshDir, readRecords, sink, waitFor, writeIn } from './helpers.mjs';
import assert from 'node:assert/strict';
import { chmodSync, copyFileSync, existsSync, readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const { spawnSession, SessionError } = await import('../../src/session/spawn.mjs');
const { runSpawn } = await import('../../src/cli/spawn.mjs');
const { readAllRows } = await import('../../src/ledger/write.mjs');
const { currentRunRoot, pidsDir } = await import('../../src/util/tmp.mjs');
const { DEFAULT_ANSWER } = await import('../fixtures/bin/fake-common.mjs');
const { S2_SCHEMA } = await import('../../src/session/s2.mjs');
const { mergeForbidden, renderForCodex } = await import('../../src/util/forbidden.mjs');
const { renderCodexRules } = await import('../../src/engines/codex-home.mjs');

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const CLAUDE = { provider: 'anthropic', model: 'fake-opus', effort: 'high' };
const PACKET_BYTES = Buffer.concat([Buffer.from('packet héllo ✓\n', 'utf8'), Buffer.from([0x00, 0xff, 0x0a]), Buffer.from('end')]);

test('round trip: the fake CLI returns the schema object and the ledger row says tokens_source reported', async () => {
  const { deps } = fakeDeps();
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide this');
  const result = await spawnSession({ cfg: cfgWith(CLAUDE), level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA, slug: 'b9a-round', block: 'B1' }, deps);
  assert.equal(result.status, 'ok');
  assert.deepEqual(result.answer, DEFAULT_ANSWER);
  const rows = await readAllRows('b9a-round');
  assert.equal(rows.length, 1);
  assert.deepEqual(
    [rows[0].event, rows[0].role, rows[0].block, rows[0].tokens_source, rows[0].tokens_in, rows[0].tokens_out],
    ['session', 'reviewer', 'B1', 'reported', 3, 42],
  );
});

test('an answer that does not match the schema is invalid-output, not ok', async () => {
  const { deps } = fakeDeps({ FAKE_ANSWER: JSON.stringify({ decision: 'x' }) });
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide this');
  const result = await spawnSession({ cfg: cfgWith(CLAUDE), level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA }, deps);
  assert.deepEqual([result.status, result.answer], ['invalid-output', null]);
});

test('stdinFile content reaches the fake CLI stdin byte-identical (Claude judge, Codex facts — B32: the Codex stdin role)', async () => {
  const { deps, records } = fakeDeps();
  const packet = writeIn(freshDir('pk'), 'packet.bin', PACKET_BYTES);
  await spawnSession({ cfg: cfgWith(CLAUDE), level: 'L2', role: 'judge', promptPath: packet }, deps);
  await spawnSession({ cfg: cfgWith({ provider: 'openai', model: 'fake-gpt' }), level: 'L2', role: 'facts', promptPath: packet }, deps);
  const got = readRecords(records).map((r) => [r.name, r.stdin_is_pipe, Buffer.from(r.stdin_b64, 'base64').equals(PACKET_BYTES), r.argv.includes(packet)]);
  assert.deepEqual(got.sort(), [
    ['claude', true, true, false],
    ['codex', true, true, false],
  ]);
});

const OUT_ANSWER = { ...DEFAULT_ANSWER, decision: 'only-in-the-o-file' };

/** Run a Codex facts session (B32: no Codex reviewer) whose answer exists ONLY in the `-o` file; returns the result and the fake's record. */
async function codexOutRun() {
  const { deps, records } = fakeDeps({ FAKE_CODEX_QUIET: '1', FAKE_ANSWER: JSON.stringify(OUT_ANSWER) });
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const result = await spawnSession({ cfg: cfgWith({ provider: 'openai', model: 'fake-gpt' }), level: 'L2', role: 'facts', promptPath: packet, schema: S2_SCHEMA }, deps);
  const [rec] = readRecords(records);
  return { result, rec, outPath: rec.argv[rec.argv.indexOf('-o') + 1] };
}

test('Codex outPath is read: the answer that exists only in the -o file is the result', async () => {
  const { result, rec } = await codexOutRun();
  assert.equal(rec.argv.indexOf('-o') > 0, true);
  assert.deepEqual([rec.wrote_out, result.status, result.answer], [true, 'ok', OUT_ANSWER]);
});

test('Codex outPath is deleted after it is read', async () => {
  const { rec, outPath } = await codexOutRun();
  assert.deepEqual([rec.wrote_out, path.isAbsolute(outPath), existsSync(outPath)], [true, true, false]);
});

test('Grok gets --prompt-file <packet> and nothing on stdin', async () => {
  const { deps, records } = fakeDeps();
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const result = await spawnSession({ cfg: cfgWith({ provider: 'xai', model: 'fake-grok' }), level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA }, deps);
  const [rec] = readRecords(records);
  assert.deepEqual(
    [result.status, rec.argv[rec.argv.indexOf('--prompt-file') + 1], rec.stdin_is_pipe, rec.stdin_b64],
    ['ok', packet, false, ''],
  );
});

test('a timeout kills the child and clears its pid-registry entry', async () => {
  const { deps, records } = fakeDeps({ FAKE_SLEEP_MS: '60000' });
  const packet = writeIn(freshDir('pk'), 'packet.md', 'slow');
  const pending = spawnSession({ cfg: cfgWith(CLAUDE), level: 'L2', role: 'reviewer', promptPath: packet, timeoutMs: 1500 }, deps);
  let pid = 0;
  try {
    assert.equal(await waitFor(() => readdirSync(records).length === 1), true);
    pid = readRecords(records)[0].pid;
    const entry = path.join(pidsDir(), `${pid}.json`);
    assert.equal(existsSync(entry), true, 'registered while alive');
    const result = await pending;
    assert.equal(result.status, 'timeout');
    assert.deepEqual([alive(pid), existsSync(entry)], [false, false]);
  } finally {
    if (pid && alive(pid)) process.kill(pid, 'SIGKILL');
  }
});

test('--background writes a pid file under the run root and a log with the child output', async () => {
  const { deps } = fakeDeps();
  const brief = writeIn(freshDir('brief'), 'brief.md', 'code it');
  const root = currentRunRoot();
  const result = await spawnSession({ cfg: cfgWith(CLAUDE), level: 'L2', role: 'coder', promptPath: brief, cwd: freshDir('ws'), background: true, runRoot: root }, deps);
  try {
    const pidRecord = JSON.parse(readFileSync(result.pidFile, 'utf8'));
    assert.deepEqual([result.status, result.pidFile.startsWith(`${root}${path.sep}`), pidRecord.pid], ['started', true, result.pid]);
    assert.equal(await waitFor(() => !alive(result.pid)), true);
    assert.equal(JSON.parse(readFileSync(result.logPath, 'utf8')).type, 'result');
  } finally {
    if (alive(result.pid)) process.kill(-result.pid, 'SIGKILL');
  }
});

test('retry ladder: fallback[1] is spawned after fallback[0] reports 402', async () => {
  const { deps, records, stderr } = fakeDeps({ FAKE_402_MODELS: 'fake-gpt,fake-grok' });
  const cfg = cfgWith({
    provider: 'openai',
    model: 'fake-gpt',
    fallback: [
      { provider: 'xai', model: 'fake-grok' },
      { provider: 'anthropic', model: 'fake-opus' },
    ],
  });
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  // B32: role facts, the one closed-book role Codex may still run
  const result = await spawnSession({ cfg, level: 'L2', role: 'facts', promptPath: packet, schema: S2_SCHEMA }, deps);
  assert.deepEqual(
    [result.status, result.fallback_step, result.attempts.map((a) => `${a.model}:${a.status}:${a.reason}`), readRecords(records).length],
    ['ok', 2, ['fake-gpt:unavailable:http-402', 'fake-grok:unavailable:http-402', 'fake-opus:ok:null'], 3],
  );
  assert.equal(stderr.text().split('\n').filter(Boolean).length, 3);
});

test('a $id-bearing schema validates in two sessions of one process, including a 402 fallback step', async () => {
  const schema = { $id: 'https://code-forge.test/schemas/s2-answer.json', ...S2_SCHEMA };
  const first = fakeDeps();
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const one = await spawnSession({ cfg: cfgWith(CLAUDE), level: 'L2', role: 'reviewer', promptPath: packet, schema }, first.deps);
  const second = fakeDeps({ FAKE_402_MODELS: 'fake-gpt' });
  const cfg = cfgWith({ provider: 'openai', model: 'fake-gpt', fallback: [{ provider: 'anthropic', model: 'fake-opus' }] });
  const two = await spawnSession({ cfg, level: 'L2', role: 'facts', promptPath: packet, schema }, second.deps);
  assert.deepEqual(
    [one.status, one.answer, two.status, two.fallback_step, two.answer, readRecords(first.records).length, readRecords(second.records).length],
    ['ok', DEFAULT_ANSWER, 'ok', 1, DEFAULT_ANSWER, 1, 2],
  );
});

// A source schema with ONE optional field: OpenAI strict mode compiles it to required + nullable.
const OPTIONAL_SCHEMA = { type: 'object', properties: { decision: { type: 'string' }, note: { type: 'string' } }, required: ['decision'], additionalProperties: false };

test('a strict (Codex) answer with null for an optional field is ok with the field dropped; the same answer under the plain schema is invalid-output', async () => {
  const nullNote = JSON.stringify({ decision: 'proceed', note: null });
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const codex = fakeDeps({ FAKE_ANSWER: nullNote });
  const strict = await spawnSession({ cfg: cfgWith({ provider: 'openai', model: 'fake-gpt' }), level: 'L2', role: 'facts', promptPath: packet, schema: OPTIONAL_SCHEMA }, codex.deps);
  const claude = fakeDeps({ FAKE_ANSWER: nullNote });
  const plain = await spawnSession({ cfg: cfgWith(CLAUDE), level: 'L2', role: 'reviewer', promptPath: packet, schema: OPTIONAL_SCHEMA }, claude.deps);
  assert.deepEqual([strict.status, strict.answer, plain.status, plain.answer], ['ok', { decision: 'proceed' }, 'invalid-output', null]);
});

const CODEX_REFUSAL = 'codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author';

test('B32: an openai level is refused for each of reviewer/judge/s2/author with the exact SessionError, and nothing spawns', async () => {
  const { deps, records, stderr } = fakeDeps();
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const cfg = cfgWith({ provider: 'openai', model: 'fake-gpt' });
  for (const role of /** @type {const} */ (['reviewer', 'judge', 's2', 'author'])) {
    await assert.rejects(spawnSession({ cfg, level: 'L2', role, promptPath: packet }, deps), (err) => {
      assert.ok(err instanceof SessionError, role);
      assert.deepEqual([err.code, err.message], ['closed-book', CODEX_REFUSAL], role);
      return true;
    });
  }
  assert.deepEqual([readRecords(records).length, stderr.text()], [0, '']);
});

test('B32: an openai fallback of a reviewer is skipped (1 stderr line); the ladder goes anthropic (402) -> xai, 2 spawns', async () => {
  const { deps, records, stderr } = fakeDeps({ FAKE_402_MODELS: 'fake-opus' });
  const cfg = cfgWith({ provider: 'anthropic', model: 'fake-opus', fallback: [{ provider: 'openai', model: 'fake-gpt' }, { provider: 'xai', model: 'fake-grok' }] });
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const result = await spawnSession({ cfg, level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA }, deps);
  assert.deepEqual(
    [result.status, result.fallback_step, result.attempts.map((a) => `${a.provider}:${a.status}`), readRecords(records).map((r) => r.name)],
    ['ok', 2, ['anthropic:unavailable', 'xai:ok'], ['claude', 'grok']],
  );
  const lines = stderr.text().split('\n').filter(Boolean);
  assert.equal(lines.filter((l) => l === `fallback skipped: 1 openai step(s) — ${CODEX_REFUSAL}`).length, 1);
  assert.equal(lines.length, 3);
});

test('B32 opt-in: review.allow_open_book_codex lets an openai reviewer run with -s read-only, and an openai fallback is NOT skipped', async () => {
  const { deps, records, stderr } = fakeDeps({ FAKE_402_MODELS: 'fake-opus' });
  const cfg = { ...cfgWith({ provider: 'anthropic', model: 'fake-opus', fallback: [{ provider: 'openai', model: 'fake-gpt' }] }), review: { allow_open_book_codex: true } };
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const result = await spawnSession({ cfg, level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA }, deps);
  const recs = readRecords(records);
  assert.deepEqual([result.status, result.fallback_step, recs.map((r) => r.name)], ['ok', 1, ['claude', 'codex']]);
  const codex = recs[1].argv;
  assert.equal(codex[codex.indexOf('-s') + 1], 'read-only');
  assert.equal(stderr.text().includes('fallback skipped'), false);
  const direct = await spawnSession({ cfg: { ...cfgWith({ provider: 'openai', model: 'fake-gpt' }), review: { allow_open_book_codex: true } }, level: 'L3', role: 'judge', promptPath: packet }, fakeDeps().deps);
  assert.equal(direct.status, 'ok');
});

test('B32 fix 1: with the opt-in on, a Codex coder and a Codex facts argv are token-for-token the same as with it off (only -C/-o session paths differ)', async () => {
  /** @param {string[]} argv */
  const norm = (argv) => argv.map((t, i) => (argv[i - 1] === '-o' || argv[i - 1] === '-C' ? '<path>' : t));
  const brief = writeIn(freshDir('brief'), 'brief.md', 'code it');
  const cwd = freshDir('coder-cwd');
  /** @type {Record<string, string[][]>} */
  const seen = { coder: [], facts: [] };
  for (const open of [false, true]) {
    const cfg = { ...cfgWith({ provider: 'openai', model: 'fake-gpt' }), review: { allow_open_book_codex: open } };
    for (const role of /** @type {const} */ (['coder', 'facts'])) {
      const { deps, records } = fakeDeps();
      const result = await spawnSession({ cfg, level: 'L1', role, promptPath: brief, ...(role === 'coder' ? { cwd } : {}) }, deps);
      assert.equal(result.status, 'ok', `${role} open=${open}`);
      const recs = readRecords(records);
      assert.equal(recs.length, 1);
      seen[role].push(norm(recs[0].argv.slice(1)));
    }
  }
  assert.deepEqual(seen.coder[1], seen.coder[0]);
  assert.deepEqual(seen.facts[1], seen.facts[0]);
  assert.equal(seen.facts[0].includes('read-only'), true);
  assert.equal(seen.coder[0].includes('workspace-write'), true);
});

test('B32 fix 1: validate, resolve and the spawner agree on openai fallbacks — validate names fallback[1] and [2], the spawner skips exactly 2 steps', async () => {
  const { validateConfig } = await import('../../src/config/validate.mjs');
  const { resolveLevel } = await import('../../src/config/known-ids.mjs');
  const level = { provider: 'anthropic', model: 'fake-opus', fallback: [{ provider: 'xai', model: 'fake-grok' }, { provider: 'openai', model: 'fake-gpt' }, { provider: 'openai', model: 'fake-gpt-2' }] };
  const cfg = cfgWith(level);
  const keys = validateConfig(cfg).warnings.filter((w) => w.rule === 'closed-book-on-openai').map((w) => w.message.split(' ')[0]);
  const fromResolve = resolveLevel(cfg, 'L2').fallback.flatMap((f, i) => (f.provider === 'openai' ? [`levels.L2.fallback[${i}]`] : []));
  assert.deepEqual(keys.filter((k) => k.startsWith('levels.L2')), ['levels.L2.fallback[1]', 'levels.L2.fallback[2]']);
  assert.deepEqual(fromResolve, ['levels.L2.fallback[1]', 'levels.L2.fallback[2]']);
  const { deps, stderr } = fakeDeps();
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  await spawnSession({ cfg, level: 'L2', role: 'reviewer', promptPath: packet }, deps);
  assert.equal(stderr.text().split('\n').filter((l) => l.startsWith('fallback skipped: 2 openai step(s) — ')).length, 1);
});

test('a failed run whose stdout mentions 402 in normal content is failed, not unavailable (1 attempt)', async () => {
  const { deps, records } = fakeDeps({ FAKE_ANSWER: '"see line 402: rate limit notes, 401 and 429 too"', FAKE_EXIT: '1' });
  const cfg = cfgWith({ provider: 'anthropic', model: 'fake-opus', fallback: [{ provider: 'xai', model: 'fake-grok' }] });
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const result = await spawnSession({ cfg, level: 'L2', role: 'reviewer', promptPath: packet }, deps);
  assert.deepEqual([result.status, result.attempts.length, readRecords(records).length], ['failed', 1, 1]);
});

test('a failure that is not unavailability does not climb the ladder', async () => {
  const { deps, records } = fakeDeps({ FAKE_ANSWER: '"not json object"' });
  const cfg = cfgWith({ provider: 'anthropic', model: 'fake-opus', fallback: [{ provider: 'xai', model: 'fake-grok' }] });
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const result = await spawnSession({ cfg, level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA }, deps);
  assert.deepEqual([result.status, readRecords(records).length], ['invalid-output', 1]);
});

test('a coder argv that isForbidden is refused before anything spawns', async () => {
  const { deps, records } = fakeDeps();
  const brief = writeIn(freshDir('brief'), 'brief.md', 'code it');
  // Codex puts the coder's cwd in argv (`-C <dir>`): a cwd under ~/.code-forge/runs makes the BUILT argv forbidden.
  const cwd = path.join(process.env.HOME ?? '', '.code-forge', 'runs', 'ws');
  await assert.rejects(
    spawnSession({ cfg: cfgWith({ provider: 'openai', model: 'fake-gpt' }), level: 'L2', role: 'coder', promptPath: brief, cwd }, deps),
    (err) => err instanceof SessionError && err.code === 'forbidden' && err.message === 'coder argv refused before spawn: forbidden entry code-forge-runs-access',
  );
  assert.equal(readdirSync(records).length, 0);
});

test('B4.2: the default pre-spawn check is mergeForbidden() — a coder argv naming --no-require-reviews (coder-only) is refused; a closed-book one is not checked', async () => {
  const { deps, records } = fakeDeps();
  const brief = writeIn(freshDir('brief'), 'x--no-require-reviews.md', 'code it');
  await assert.rejects(
    spawnSession({ cfg: cfgWith(CLAUDE), level: 'L2', role: 'coder', promptPath: brief, cwd: freshDir('coder-cwd') }, deps),
    (err) => err instanceof SessionError && err.code === 'forbidden' && err.message === 'coder argv refused before spawn: forbidden entry code-forge-no-require-reviews',
  );
  assert.equal(readdirSync(records).length, 0);
  const ok = await spawnSession({ cfg: cfgWith(CLAUDE), level: 'L2', role: 'reviewer', promptPath: brief }, deps);
  assert.equal(ok.status, 'ok');
});

test('every spawn prints the level line on stderr', async () => {
  const { deps, stderr } = fakeDeps();
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  await spawnSession({ cfg: cfgWith(CLAUDE), level: 'L1', role: 'reviewer', promptPath: packet }, deps);
  assert.equal(stderr.text(), 'level=L1 provider=anthropic model=fake-opus effort=high fallback_step=0\n');
});

test('spawn verb: config from cwd, JSON result on stdout, exit 0; exit 3 when every step is unavailable', async () => {
  const ws = freshDir('verb');
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), path.join(ws, '.code-forge.yml'));
  const packet = writeIn(ws, 'packet.md', 'decide');
  const before = process.cwd();
  process.chdir(ws);
  try {
    const { deps } = fakeDeps();
    const stdout = sink();
    const code = await runSpawn(['--level', 'L2', '--role', 'reviewer', '--brief', packet], { ...deps, stdout });
    const shown = JSON.parse(stdout.text());
    assert.deepEqual([code, shown.status, shown.model, shown.fallback_step], [0, 'ok', 'claude-opus-5-5', 0]);
    const down = fakeDeps({ FAKE_402_MODELS: 'claude-opus-5-5' });
    assert.equal(await runSpawn(['--level', 'L2', '--role', 'reviewer', '--brief', packet], { ...down.deps, stdout: sink() }), 3);
    const bad = fakeDeps();
    const missing = path.join(ws, 'no-such-schema.json');
    assert.equal(await runSpawn(['--level', 'L2', '--role', 'reviewer', '--brief', packet, '--schema', missing], { ...bad.deps, stdout: sink() }), 2);
    assert.equal(bad.stderr.text(), 'spawn: --schema cannot be read\n');
  } finally {
    process.chdir(before);
  }
});

test('B4.1: a Codex coder spawn runs the fake codex with CODEX_HOME = the session home holding the merged coder list\'s rules; the home is removed after', async () => {
  const dir = freshDir('fake-codex-env');
  const out = path.join(dir, 'seen.json');
  const fake = writeIn(dir, 'codex', `#!/usr/bin/env node
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
const home = process.env.CODEX_HOME ?? null;
const rules = home ? path.join(home, 'rules', 'code-forge.rules') : null;
const text = rules && existsSync(rules) ? readFileSync(rules, 'utf8') : '';
writeFileSync(${JSON.stringify(out)}, JSON.stringify({ home, rules: text.split('\\n').filter((l) => l.startsWith('prefix_rule(')).length }));
process.stdout.write(JSON.stringify({ type: 'item.completed', item: { id: 'i', type: 'agent_message', text: 'done' } }) + '\\n');
`);
  chmodSync(fake, 0o755);
  const { deps } = fakeDeps();
  const brief = writeIn(freshDir('brief'), 'brief.md', 'do nothing');
  const result = await spawnSession({ cfg: cfgWith({ provider: 'openai', model: 'fake-gpt' }), level: 'L1', role: 'coder', promptPath: brief, cwd: freshDir('coder-cwd') }, { ...deps, bins: { ...deps.bins, codex: fake } });
  assert.equal(result.status, 'ok');
  const seen = JSON.parse(readFileSync(out, 'utf8'));
  assert.equal(typeof seen.home, 'string');
  assert.equal(path.basename(path.dirname(seen.home)), 'codex-homes');
  assert.equal(path.relative(currentRunRoot(), seen.home).startsWith('..'), false, seen.home);
  assert.equal(seen.rules, renderCodexRules(renderForCodex(mergeForbidden())).count); // B4.2: FORBIDDEN + the coder-only entries
  assert.equal(existsSync(seen.home), false);
});

test('readAnswer: a Grok envelope yields its structuredOutput, not the envelope', async () => {
  const { readAnswer } = await import('../../src/session/spawn.mjs');
  const answer = { passed: true, summary: 'ok', reviewed_hunks: [], findings: [] };
  const stdout = JSON.stringify({ text: JSON.stringify(answer), stopReason: 'end_turn', usage: { input_tokens: 10, output_tokens: 2 }, structuredOutput: answer });
  const read = readAnswer('grok', stdout, null);
  assert.deepEqual(read.answer, answer);
  assert.equal(read.text, JSON.stringify(answer));
  const textOnly = readAnswer('grok', JSON.stringify({ text: JSON.stringify(answer), stopReason: 'end_turn' }), null);
  assert.deepEqual(textOnly.answer, answer);
});
