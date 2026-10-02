/**
 * `code-forge jev ask` (plan §10.3 B3 acceptance: "`forge jev ask lane --state f.json` prints the
 * typed answer using B2's chain and B6's writer"). Most tests inject a fake store/fetch/writer for
 * speed and isolation; ONE test below uses the REAL `createDefaultKeyStore` (B2) and the REAL
 * `appendRow`/`readAllRows` (B6) to prove the wiring is real, not just type-compatible.
 */

import { FAKE_KEY, isolatedEnvFor, memoryStore, path, sink, tempDir, withIsolatedHome, writeTempFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { test } from 'node:test';
import { runJev } from '../../src/cli/jev.mjs';

const NOW = 1_800_000_000_000;

/**
 * @param {object} [opts]
 * @param {ReturnType<typeof memoryStore>} [opts.store]
 * @param {(row: object, opts: {slug: string}) => Promise<object>} [opts.appendRow]
 * @param {(args: {state: any, questions: any, key: string}) => Promise<any>} [opts.askJev]
 */
function harness(opts = {}) {
  const stdout = sink();
  const stderr = sink();
  const store = opts.store ?? memoryStore({ jev: { value: FAKE_KEY, exp: null } });
  const writtenRows = [];
  const appendRow = opts.appendRow ?? (async (row, o) => { writtenRows.push({ row, opts: o }); return row; });
  // No default falls through to the REAL Jev client: a test that reaches this without overriding
  // `askJev` is a test bug, not a reason to hit the real network.
  const askJev = opts.askJev ?? (async () => { throw new Error('unexpected real askJev call in a test'); });
  /** @param {string[]} args @param {object} [extra] */
  const run = (args, extra = {}) =>
    runJev(args, { store, stdout, stderr, appendRow, askJev, env: {}, now: () => NOW, makeId: () => 'decision-1', ...extra });
  return { store, stdout, stderr, writtenRows, run };
}

/**
 * A fake `askJev` that RECORDS every call's arguments — fix round 1 finding: the acceptance
 * clause is "uses B2's key chain", but nothing checked which key actually reached `askJev` (a
 * fake that ignores its `key` argument lets `runJev` pass `undefined` or the wrong value and
 * every prior test would still pass).
 * @param {any} response
 * @returns {{fn: (args: {state: any, questions: any, key: string}) => Promise<any>, calls: any[]}}
 */
function spyAskJev(response) {
  const calls = [];
  return {
    calls,
    fn: async (args) => {
      calls.push(args);
      return response;
    },
  };
}

test('jev ask lane --state f.json: prints the typed answer, writes exactly one decision row through B6\'s writer, and passes B2\'s resolved key to askJev', async () => {
  const dir = await tempDir();
  const statePath = await writeTempFile(dir, 'state.json', JSON.stringify({ block: { task: 'x' } }));
  const spy = spyAskJev({
    ok: true,
    answers: { lane: { type: 'choice', choice: 'L0', probabilities: { L0: 1, L1: 0 } } },
    usage: { input_tokens: 12, output_tokens: 4 },
    attempts: 1,
    ms: 500,
  });
  const h = harness({ askJev: spy.fn });

  const code = await h.run(['ask', 'lane', '--state', statePath, '--cwd', dir, '--slug', 'demo-project']);

  assert.equal(code, 0);
  // Proves the key resolved through B2's chain (memoryStore holds FAKE_KEY under "jev") is the
  // one that actually reached askJev — not undefined, not a wrong value.
  assert.equal(spy.calls.length, 1);
  assert.equal(spy.calls[0].key, FAKE_KEY);

  assert.equal(h.writtenRows.length, 1);
  const [{ row, opts }] = h.writtenRows;
  assert.equal(row.event, 'decision');
  assert.equal(row.decision_id, 'decision-1');
  assert.equal(row.question, 'lane');
  assert.equal(row.answer, 'L0');
  assert.equal(row.source, 'jev');
  assert.equal(row.stage, 'act');
  assert.equal(opts.slug, 'demo-project');

  const printed = JSON.parse(h.stdout.text);
  assert.equal(printed.question, 'lane');
  assert.equal(printed.decision.value, 'L0');
  assert.equal(printed.decision.decided, true);
});

test('jev ask: an unknown question id is a usage error (exit 2), never reaches the network', async () => {
  const dir = await tempDir();
  const statePath = await writeTempFile(dir, 'state.json', '{}');
  let fetchCalled = false;
  const h = harness({ askJev: async () => { fetchCalled = true; } });
  const code = await h.run(['ask', 'made-up-question', '--state', statePath, '--cwd', dir]);
  assert.equal(code, 2);
  assert.equal(fetchCalled, false);
  assert.match(h.stderr.text, /unknown question id/);
});

test('jev ask: missing --state is a usage error', async () => {
  const h = harness();
  const code = await h.run(['ask', 'lane']);
  assert.equal(code, 2);
  assert.match(h.stderr.text, /usage:/);
});

test('jev ask: no key resolvable ⇒ exit 1, no ledger row written', async () => {
  const dir = await tempDir();
  const statePath = await writeTempFile(dir, 'state.json', '{}');
  const h = harness({ store: memoryStore({}) }); // empty store — nothing to resolve
  const code = await h.run(['ask', 'lane', '--state', statePath, '--cwd', dir]);
  assert.equal(code, 1);
  assert.equal(h.writtenRows.length, 0);
  assert.match(h.stderr.text, /no Jev key resolved/);
});

test('jev ask: a Jev request failure (e.g. rate limited) ⇒ exit 1, no ledger row, the fake key never printed', async () => {
  const dir = await tempDir();
  const statePath = await writeTempFile(dir, 'state.json', '{}');
  const h = harness({ askJev: async () => ({ ok: false, kind: 'rate_limited', status: 429, requestId: null, attempts: 4 }) });
  const code = await h.run(['ask', 'lane', '--state', statePath, '--cwd', dir]);
  assert.equal(code, 1);
  assert.equal(h.writtenRows.length, 0);
  const all = h.stdout.text + h.stderr.text;
  assert.equal(all.includes(FAKE_KEY), false);
});

test('B36 jev ask lane --block --plan: the Jev decision row records the block and the plan file name', async () => {
  const dir = await tempDir();
  const statePath = await writeTempFile(dir, 'state.json', JSON.stringify({ block: { task: 'x' } }));
  const h = harness({ askJev: async () => ({ ok: true, answers: { lane: { type: 'choice', probabilities: { L0: 0, L1: 1 } } }, usage: { input_tokens: 1, output_tokens: 1 } }) });
  const code = await h.run(['ask', 'lane', '--state', statePath, '--cwd', dir, '--slug', 'demo', '--block', 'B7', '--plan', 'plans/feature.plan.md']);
  assert.equal(code, 0, h.stderr.text);
  assert.equal(h.writtenRows.length, 1);
  const { row } = h.writtenRows[0];
  assert.deepEqual([row.question, row.answer, row.source, row.block, row.plan], ['lane', 'L1', 'jev', 'B7', 'feature.plan.md']);
});

test('B36 jev ask lane --rules: the fallback rule answers with no key and no request, and the row says source rules', async () => {
  const dir = await tempDir();
  const facts = { filesChanged: 2, linesAdded: 40, touchesMigration: true, touchesPolicyOrMiddleware: false, pathFloorHit: false, keywordsFound: [] };
  const statePath = await writeTempFile(dir, 'state.json', JSON.stringify(facts));
  const h = harness({ store: memoryStore({}) }); // no key, and askJev throws if reached
  const code = await h.run(['ask', 'lane', '--state', statePath, '--cwd', dir, '--slug', 'demo', '--block', 'B2', '--rules']);
  assert.equal(code, 0, h.stderr.text);
  assert.deepEqual(h.writtenRows.map((w) => w.row), [{ event: 'decision', decision_id: 'decision-1', question: 'lane', answer: 'L2', source: 'rules', block: 'B2' }]);
  assert.deepEqual(JSON.parse(h.stdout.text), { decision_id: 'decision-1', question: 'lane', answer: 'L2', source: 'rules', block: 'B2' });
});

test('B36 jev ask: --rules on a question no rule answers, and a bad --block, are usage errors with no row', async () => {
  const dir = await tempDir();
  const statePath = await writeTempFile(dir, 'state.json', '{}');
  const h = harness({ store: memoryStore({}) });
  assert.equal(await h.run(['ask', 'next', '--state', statePath, '--cwd', dir, '--rules']), 2);
  assert.equal(h.stderr.text, 'jev ask: no rule answers "next" (--rules covers lane, risk, security_sensitive)\n');
  const h2 = harness({ store: memoryStore({}) });
  assert.equal(await h2.run(['ask', 'lane', '--state', statePath, '--cwd', dir, '--block', '../B1', '--rules']), 2);
  assert.equal(h2.stderr.text, 'jev ask: --block must be a block id (letters, digits, . _ -; at most 32 characters)\n');
  assert.equal(h.writtenRows.length + h2.writtenRows.length, 0);
  const h3 = harness({ store: memoryStore({}) });
  assert.equal(await h3.run(['ask', 'lane', '--state', statePath, '--cwd', dir, '--rules', '--rulez']), 2);
  assert.match(h3.stderr.text, /^usage: code-forge jev ask/);
});

test('B36 jev ask --rules: missing or mistyped fallback facts are a usage error naming them, nothing recorded', async () => {
  const dir = await tempDir();
  const partial = await writeTempFile(dir, 'partial.json', JSON.stringify({ filesChanged: '3', linesAdded: 10, touchesMigration: false, pathFloorHit: false, keywordsFound: ['Auth', 1] }));
  const list = await writeTempFile(dir, 'list.json', '[]');
  const h = harness({ store: memoryStore({}) });
  assert.equal(await h.run(['ask', 'lane', '--state', partial, '--cwd', dir, '--rules']), 2);
  assert.equal(h.stderr.text, 'jev ask: --rules needs the fallback facts in the --state object; missing or wrong type: filesChanged, touchesPolicyOrMiddleware, keywordsFound\n');
  const h2 = harness({ store: memoryStore({}) });
  assert.equal(await h2.run(['ask', 'risk', '--state', list, '--cwd', dir, '--rules']), 2);
  assert.equal(h2.stderr.text, 'jev ask: --rules needs the fallback facts in the --state object; missing or wrong type: filesChanged, linesAdded, touchesMigration, touchesPolicyOrMiddleware, pathFloorHit, keywordsFound\n');
  assert.equal(h.writtenRows.length + h2.writtenRows.length, 0);
});

test('B36 jev ask --rules respects system1.disable, and with no --slug the row goes to the directory-name slug', async () => {
  const facts = JSON.stringify({ filesChanged: 0, linesAdded: 0, touchesMigration: false, touchesPolicyOrMiddleware: false, pathFloorHit: false, keywordsFound: [] });
  const dir = await tempDir();
  await writeTempFile(
    dir,
    '.code-forge.yml',
    'version: 1\nprovider: anthropic\nlevels:\n  L0: {model: m}\n  L1: {model: m}\n  L2: {model: m}\n  L3: {model: m}\nsystem1:\n  disable: [risk]\n',
  );
  const statePath = await writeTempFile(dir, 'state.json', facts);
  const h = harness({ store: memoryStore({}) });
  assert.equal(await h.run(['ask', 'risk', '--state', statePath, '--cwd', dir, '--rules']), 1);
  assert.equal(h.stderr.text, 'jev ask: question "risk" is disabled by system1.disable\n');
  assert.equal(h.writtenRows.length, 0);
  assert.equal(await h.run(['ask', 'lane', '--state', statePath, '--cwd', dir, '--rules']), 0, h.stderr.text);
  assert.deepEqual(h.writtenRows.map((w) => [w.row.answer, w.opts.slug]), [['L0', path.basename(dir).toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '')]]);
});

test('jev ask: system1.disable blocks the question before any network call', async () => {
  const dir = await tempDir();
  await writeTempFile(
    dir,
    '.code-forge.yml',
    'version: 1\nprovider: anthropic\nlevels:\n  L0: {model: m}\n  L1: {model: m}\n  L2: {model: m}\n  L3: {model: m}\nsystem1:\n  disable: [defect]\n',
  );
  const statePath = await writeTempFile(dir, 'state.json', '{}');
  let called = false;
  const h = harness({ askJev: async () => { called = true; } });
  const code = await h.run(['ask', 'defect', '--state', statePath, '--cwd', dir]);
  assert.equal(code, 1);
  assert.equal(called, false);
  assert.match(h.stderr.text, /disabled by system1.disable/);
});

// ── real B2 chain + real B6 writer (not fakes) ──────────────────────────────────────────────────
//
// Fix round 1 BLOCKER: the previous version built the real key store from
// `{...process.env, CODE_FORGE_KEY_BACKEND: 'file'}` WITHOUT redirecting `HOME`, then called
// `realStore.put('jev', FAKE_KEY, ...)` — on a machine where this file's own import-time HOME
// override was ever weakened or reordered away, that write would land in the REAL
// `~/.code-forge/store`, overwriting whatever real Jev key was there. B6's ledger has the same
// problem one level deeper: `ledgerDir()`/`appendRow`/`readAllRows` call `os.homedir()` directly
// (no injectable path at all), so `readAllRows('integration-demo')` read from the REAL, shared,
// persistent `~/.code-forge/ledger/integration-demo.jsonl` — rows piled up across runs, and a
// SECOND run would see `decisionRows.length === 2`, not 1.
//
// Fix: `withIsolatedHome` (below) explicitly mutates `process.env` (HOME + USERPROFILE + the XDG
// base dirs) to a FRESH, dedicated temp dir for the whole test, restored in a `finally` — this is
// the ONLY lever that reaches B6's ledger, and it is now visible and self-contained at this one
// call site instead of resting on another file's import order. A random slug (`randomUUID()`)
// adds a second, independent layer against any cross-run collision. Positive `existsSync`
// assertions prove the writes actually landed under the isolated `homeDir`, not merely that the
// row count happened to be right.
//
// Fix round 2 MAJOR: the env object handed to `createDefaultKeyStore`/`runJev` used to be
// `{...process.env, HOME: homeDir, CODE_FORGE_KEY_BACKEND: 'file'}` — a SPREAD of the real
// environment. If the developer's or CI's real environment carries a Jev key under
// `CODE_FORGE_KEY_JEV` (the default env name B2's chain derives for name "jev") or any other
// variable, B2's chain resolves THAT key — ahead of the store — making
// `spy.calls[0].key === FAKE_KEY` pass or fail for reasons that have nothing to do with this
// test's own code, and letting a real key reach the fake `askJev`'s call log. `isolatedEnvFor`
// (helpers.mjs) builds a MINIMAL, explicit env instead — nothing survives from the real
// environment except `PATH` — so no var under any name (`CODE_FORGE_KEY_JEV`, `TYPESAFE_API_KEY`,
// or anything else) can reach `resolveKey`'s `env[envName]` lookup.

test('withIsolatedHome: process.env.HOME is the isolated dir DURING fn, and exactly the prior value after', async () => {
  const before = process.env.HOME;
  let observedDuring;
  await withIsolatedHome(async (dir) => {
    observedDuring = process.env.HOME;
    assert.equal(observedDuring, dir);
    assert.notEqual(observedDuring, before);
  });
  assert.equal(process.env.HOME, before);
});

// Fix round 2 MAJOR: HOME alone isn't enough — os.homedir() reads USERPROFILE on Windows (not
// HOME at all), and XDG_CONFIG_HOME/XDG_DATA_HOME are the vars a future or third-party path
// resolver could follow instead. All four must move together and restore together.
test('withIsolatedHome: also redirects USERPROFILE and the XDG base dirs, and restores every one of them', async () => {
  const VARS = ['HOME', 'USERPROFILE', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME'];
  const before = Object.fromEntries(VARS.map((k) => [k, process.env[k]]));
  await withIsolatedHome(async (dir) => {
    assert.equal(process.env.HOME, dir);
    assert.equal(process.env.USERPROFILE, dir);
    assert.equal(process.env.XDG_CONFIG_HOME, path.join(dir, '.config'));
    assert.equal(process.env.XDG_DATA_HOME, path.join(dir, '.local', 'share'));
    for (const key of VARS) {
      assert.notEqual(process.env[key], before[key]);
    }
  });
  for (const key of VARS) {
    assert.equal(process.env[key], before[key]);
  }
});

test('jev ask, end to end, with the REAL createDefaultKeyStore (B2) and the REAL appendRow (B6) — fully isolated under a temp HOME', async () => {
  await withIsolatedHome(async (homeDir) => {
    const { createDefaultKeyStore } = await import('../../src/keys/store.mjs');
    const { readAllRows } = await import('../../src/ledger/write.mjs');

    const dir = await tempDir();
    const statePath = await writeTempFile(dir, 'state.json', JSON.stringify({ block: { task: 'x' } }));
    const isolatedEnv = isolatedEnvFor(homeDir);
    const slug = `integration-demo-${randomUUID().slice(0, 8)}`;

    // Proves by construction that NOTHING in isolatedEnv can override the Jev key — the only
    // remaining place `resolveKey`'s chain (env → store → 1Password → ask) can find one is the
    // store, which holds nothing but FAKE_KEY.
    assert.equal(Object.hasOwn(isolatedEnv, 'CODE_FORGE_KEY_JEV'), false);
    assert.equal(Object.hasOwn(isolatedEnv, 'TYPESAFE_API_KEY'), false);
    assert.equal(Object.keys(isolatedEnv).filter((k) => /KEY|SECRET|TOKEN/i.test(k) && k !== 'CODE_FORGE_KEY_BACKEND').length, 0);

    const realStore = await createDefaultKeyStore(isolatedEnv);
    await realStore.put('jev', FAKE_KEY, { source: 'user', exp: null });
    // Proves the key really landed under the isolated home, not somewhere ambient.
    assert.equal(existsSync(path.join(homeDir, '.code-forge', 'store')), true);

    const stdout = sink();
    const stderr = sink();
    const spy = spyAskJev({
      ok: true,
      answers: { lane: { type: 'choice', choice: 'L1', probabilities: { L0: 0, L1: 1 } } },
      usage: { input_tokens: 1, output_tokens: 1 },
      attempts: 1,
      ms: 1,
    });
    const code = await runJev(['ask', 'lane', '--state', statePath, '--cwd', dir, '--slug', slug], {
      store: realStore,
      stdout,
      stderr,
      askJev: spy.fn,
      env: isolatedEnv,
    });

    assert.equal(code, 0);
    assert.equal(spy.calls.length, 1);
    assert.equal(spy.calls[0].key, FAKE_KEY); // the REAL chain resolved the key from the STORE — no env var could have supplied it (asserted above)

    // Proves the ledger row really landed under the isolated home (B6 has no injectable path —
    // this is the only way to prove `readAllRows` below is reading OUR write, not shared state).
    assert.equal(existsSync(path.join(homeDir, '.code-forge', 'ledger', `${slug}.jsonl`)), true);

    const rows = await readAllRows(slug);
    const decisionRows = rows.filter((r) => r.event === 'decision' && r.question === 'lane');
    assert.equal(decisionRows.length, 1);
    assert.equal(decisionRows[0].answer, 'L1');
    assert.equal(stdout.text.includes(FAKE_KEY), false);
    assert.equal(stderr.text.includes(FAKE_KEY), false);
  });
  // `withIsolatedHome` has now restored `process.env.HOME` (verified by its own tests in
  // helpers coverage below) — the manual `[ -e ~/.code-forge ]` check the report calls for runs
  // OUTSIDE this process, against the real machine, after the whole suite exits.
});
