// Regenerate with: node scripts/compat-golden.mjs [output-directory]
// Every expected value comes from the production Node implementation.
import { appendFile, mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { canonicalJSON, macFor, signRow, UNSIGNED_FIELDS, verifyRow } from '../src/state/signer.mjs';
import { ticketId } from '../src/worker/ticket.mjs';
import { resolveHashTicket } from '../src/worker/queue.mjs';
import { statePath } from '../src/worker/review-state.mjs';
import { runRecordPath } from '../src/state/paths.mjs';
import { ledgerDir, ledgerPath } from '../src/ledger/paths.mjs';
import { appendRow, readAllRows, readRows } from '../src/ledger/write.mjs';
import { loadProjectConfig, projectRootFor } from '../src/config/load.mjs';
import { PROVIDER_DEFAULTS } from '../src/config/defaults/index.mjs';

const stamp = '2026-01-02T03:04:05.006Z';
const key = Buffer.from('000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f', 'hex');

/**
 * Config inputs are complete filesystem recipes. Paths in oracle output use
 * $ROOT so committed bytes are independent of temp directories and HOME.
 * @typedef {{name: string, group?: string, files?: Record<string, string>, dirs?: string[], links?: Record<string, string>, cwd?: string, home?: string}} ConfigCase
 */

export async function buildConfigGoldens() {
  const repo = fileURLToPath(new URL('../', import.meta.url));
  /** @type {ConfigCase[]} */
  const inputs = [];
  const yaml = (name, text) => inputs.push({ name, files: { 'project/.code-forge.yml': text } });
  // Include every conventional config and all the project's named config
  // fixtures. Discovery is scoped to these two tracked sample trees.
  async function samples(relative) {
    for (const entry of (await readdir(path.join(repo, relative), { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name, 'en'))) {
      if (entry.name === '.code-forge' || entry.name === 'node_modules') continue;
      const name = `${relative}/${entry.name}`;
      if (entry.isDirectory()) await samples(name);
      else if (entry.name === '.code-forge.yml' || /(?:^|\.)code-forge\.yml$/.test(entry.name) || relative === 'test/fixtures/config' && entry.name.endsWith('.yml') || entry.name === 'sample-run-config.yml') {
        yaml(name, await readFile(path.join(repo, name), 'utf8'));
      }
    }
  }
  await samples('examples');
  await samples('test/fixtures');
  const fixtureCount = inputs.length;
  yaml('defaults/empty-mapping', '{}\n');
  yaml('defaults/no-insertion', 'provider: openai\nlevels: {}\n');
  for (const [provider, defaults] of Object.entries(PROVIDER_DEFAULTS)) yaml(`defaults/${provider}`, JSON.stringify(defaults));
  for (const [name, value] of Object.entries({ missing: undefined, null: null, current: 1, future: 2, zero: 0, negative: -1, fractional: 1.5, string: '1', boolean: true, array: [], object: {} })) {
    yaml(`migrate/${name}`, JSON.stringify(value === undefined ? { provider: 'openai' } : { version: value }));
  }
  yaml('migrate/nan', 'version: .nan\n');
  yaml('migrate/infinity', 'version: .inf\n');
  for (const [name, text] of Object.entries({
    string: 'project: {name: "hello é😀"}', integer: 'budget: {block_lines: 42}',
    number: 'calibration: {shadow_rate: 0.25}', boolean: 'native: {daemon: false}',
    null: 'tmp: {root: null}', array: 'production: {names: [one, two]}', object: 'context: {commands: {test: [node, --test]}}',
    'unknown-key': 'unknown: true', 'wrong-type': 'caps: {coders: "two"}', 'schema-violation': 'calibration: {shadow_rate: 2}',
  })) yaml(`schema/${name}`, `${text}\n`);
  for (const [name, text] of Object.entries({
    numbers: 'values: [0, -0, +12, 012, 0o17, 0x1f, 0b11, +0x1f, -0x1f, 1_000, 1.2e+4, 1e-7, 9007199254740993, .nan, .Inf, -.inf, 1e999]',
    booleans: 'values: [true, True, TRUE, false, False, FALSE, yes, no, on, off, y, n, tRuE]',
    nulls: 'values: [null, Null, NULL, ~, "null", "~"]\nempty:',
    timestamps: 'values: [2026-01-02, 2026-01-02T03:04:05Z]',
    strings: 'values: ["12", \'true\', "\\u00e9😀\\nline", "0x10"]',
    anchors: 'first: &one {model: test, options: [true, null, 2]}\nsecond: *one\nthird: [*one, *one]',
    'merge-is-ordinary-key': 'first: &one {model: test}\nsecond: {<<: *one, other: true}',
    literal: 'project:\n  name: |\n    line one\n    line two\n',
    folded: 'project:\n  name: >-\n    line one\n    line two\n',
    crlf: 'project:\r\n  name: hello\r\n',
    bom: '\ufeffprovider: openai',
    'numeric-keys': '1: one\n"1": overwritten\ntrue: a\n"true": b\nnull: c',
    'prototype-keys': '__proto__: {polluted: false}\nconstructor: own\nprototype: own',
    'explicit-tags': 'values: [!!str 12, !!int 012, !!float 1.2, !!bool TRUE, !!null null, !!timestamp 2026-01-02, !!binary SGVsbG8=, !custom 42]',
    'duplicate-root': 'provider: openai\nprovider: xai',
    'duplicate-nested': 'levels:\n  L0: {model: one, model: two}',
    'duplicate-number': '1: one\n01: two',
    'duplicate-quoted': '"a": one\na: two',
    'missing-alias': 'value: *missing',
    'forward-alias': 'first: *later\nsecond: &later 12',
    'masked-alias': 'value: *sk-fake12345678901234567890',
    'unterminated-sequence': 'provider: [unterminated\n',
    'unterminated-map': 'project: {name: hello\n',
    'mismatched-flow': 'project: [1, 2}\n',
    'tab-indent': 'project:\n\tname: hello\n',
    'multiple-documents': 'provider: openai\n---\nprovider: xai\n',
    empty: '', 'root-null': 'null', 'root-string': 'hello', 'root-number': '42', 'root-boolean': 'true', 'root-list': '[one, two]',
  })) yaml(`yaml/${name}`, text);
  inputs.push({ name: 'io/not-found', files: {} }, { name: 'io/directory', dirs: ['project/.code-forge.yml'] });

  const roots = [
    { name: 'git-parent', files: { 'project/.git': '', 'project/.code-forge.yml': 'name: parent' }, cwd: 'project/sub/deep' },
    { name: 'git-nearest', files: { 'project/.git': '', 'project/.code-forge.yml': 'name: parent', 'project/sub/.code-forge.yml': 'name: nearest' }, cwd: 'project/sub/deep' },
    { name: 'git-boundary', files: { '.code-forge.yml': 'name: outside', 'project/.git': '' }, cwd: 'project/sub' },
    { name: 'git-directory', dirs: ['project/.git'], files: { 'project/.code-forge.yml': 'name: parent' }, cwd: 'project/sub' },
    { name: 'home-parent', files: { 'home/.code-forge.yml': 'name: home' }, cwd: 'home/sub/deep' },
    { name: 'home-git-ignored', files: { 'home/.git': '', 'home/.code-forge.yml': 'name: home' }, cwd: 'home/sub' },
    { name: 'home-no-config', files: {}, cwd: 'home/sub' },
    { name: 'outside-home', files: { '.code-forge.yml': 'name: outside' }, cwd: 'project/sub' },
    { name: 'directory-config', files: { 'project/.git': '', 'project/.code-forge.yml': 'name: parent' }, dirs: ['project/sub/.code-forge.yml'], cwd: 'project/sub' },
    { name: 'symlink-config', files: { 'project/.git': '', 'project/shared.yml': 'name: linked' }, links: { 'project/.code-forge.yml': 'project/shared.yml' }, cwd: 'project/sub' },
    { name: 'symlink-start', files: { 'project/.git': '', 'project/.code-forge.yml': 'name: linked' }, links: { alias: 'project' }, cwd: 'alias/sub' },
    { name: 'broken-config-link', files: { 'project/.git': '' }, links: { 'project/.code-forge.yml': 'missing' }, cwd: 'project/sub' },
  ];
  inputs.push(...roots.map((c) => ({ ...c, name: `root/${c.name}`, group: 'root' })));

  const groups = { 'fixtures.json': [], 'valid.json': [], 'errors.json': [], 'roots.json': [] };
  for (const [index, input] of inputs.entries()) {
    const root = await mkdtemp(path.join(os.tmpdir(), 'config-oracle-'));
    const oldHome = process.env.HOME;
    try {
      const home = input.home ?? 'home';
      const cwd = input.cwd ?? 'project';
      for (const dir of new Set([home, ...(input.dirs ?? [])])) await mkdir(path.join(root, dir), { recursive: true });
      for (const [name, text] of Object.entries(input.files ?? {})) {
        await mkdir(path.dirname(path.join(root, name)), { recursive: true });
        await writeFile(path.join(root, name), text);
      }
      for (const [name, target] of Object.entries(input.links ?? {})) await symlink(path.join(root, target), path.join(root, name));
      await mkdir(path.join(root, cwd), { recursive: true });
      process.env.HOME = path.join(root, home);
      const result = await loadProjectConfig(path.join(root, cwd));
      const normalize = (text) => text.split(root).join('$ROOT');
      const expected = result.ok
        ? { ok: true, path: normalize(result.path), canonical: canonicalJSON(result.config) }
        : { ok: false, path: normalize(result.path), error: result.error, message: normalize(result.message) };
      const group = input.group ?? (result.ok ? 'valid' : 'error');
      const entry = { ...input, group, home, cwd, expected, ...(group === 'root' ? { root: normalize(projectRootFor(path.join(root, cwd))) } : {}) };
      groups[group === 'root' ? 'roots.json' : index < fixtureCount ? 'fixtures.json' : result.ok ? 'valid.json' : 'errors.json'].push(entry);
    } finally {
      if (oldHome === undefined) delete process.env.HOME;
      else process.env.HOME = oldHome;
      await rm(root, { recursive: true, force: true });
    }
  }
  return Object.fromEntries(Object.entries(groups).map(([name, value]) => [`config/${name}`, value]));
}

/** Build the kit in a disposable HOME; never consult an existing run or queue. */
export async function buildGoldens() {
  const home = await mkdtemp(path.join(os.tmpdir(), 'compat-home-'));
  const previousHome = process.env.HOME;
  process.env.HOME = home;
  try {
    const values = [
      ['unicode', { text: 'é 😀 𝄞 \u2028\u2029 <>&', escaped: '\u0000\b\t\n\f\r\u001f"\\' }],
      ['utf16-order', { '\ue000': 1, '😀': 2, 'é': 3, a: 4, '𝄞': 5 }],
      ['numeric-keys', { 2: 'two', 10: 'ten', 1: 'one', '01': 'leading' }],
      ['nested', [{ z: [null, true, false, { b: 2, a: 1 }], a: {} }, []]],
      ['zero', 0], ['negative-zero', -0], ['exponent-large', 1e21],
      ['exponent-small', 1e-7], ['decimal-threshold', 1e-6], ['large-decimal', 1e20],
      ['sum', 0.1 + 0.2], ['safe-int', Number.MAX_SAFE_INTEGER],
      ['rounded-int', 9007199254740993], ['rounded-large-int', 1000000000000000128],
      ['subnormal', 5e-324], ['max-finite', Number.MAX_VALUE],
      ['negative-exponent', -1.2345e-7], ['true', true], ['false', false], ['null', null],
    ];
    const canon = values.map(([name, value]) => ({
      name, input: Object.is(value, -0) ? '-0' : JSON.stringify(value), expected: canonicalJSON(value),
    }));

    const inputs = [
      { event: 'review.done', ts: stamp, file: 'src/é😀.mjs', result: { z: '<>&', a: [0.1 + 0.2, 5e-324] } },
      { event: 'session', ts: stamp, tokens_in: 12, cost_usd: 0.03, mac: 'discard me', tokens_source: 'reported', cost_source: 'estimated' },
      { event: 'nested', ts: stamp, state: { mac: 'covered', tokens_source: 'covered', cost_source: 'covered' } },
    ];
    const signed = inputs.map((row) => ({ row, mac: macFor(row, key), signed: signRow(row, key) }));
    const good = signed[0].signed;
    const verificationRows = [
      ['valid', good],
      ['unsigned-edits', { ...good, tokens_source: 'changed', cost_source: 99 }],
      ['covered-edit', { ...good, file: 'src/changed.mjs' }],
      ['timestamp-edit', { ...good, ts: '2026-01-02T03:04:05.007Z' }],
      ['missing-mac', inputs[0]], ['empty-mac', { ...good, mac: '' }],
      ['uppercase-mac', { ...good, mac: good.mac.toUpperCase() }],
      ['short-mac', { ...good, mac: '00' }],
    ];
    const sign = {
      key_hex: key.toString('hex'), unsigned_fields: UNSIGNED_FIELDS, cases: signed,
      verify: verificationRows.map(([name, row]) => ({ name, row, expected: verifyRow(row, key) })),
    };

    const base = { run: 'compat-run', block: 'B1', file: 'src/é😀.mjs', content_hash: '0123456789abcdef'.repeat(4) };
    const ids = [
      { name: 'base', parts: base },
      { name: 'null-tree', parts: { ...base, tree: null } },
      { name: 'tree', parts: { ...base, tree: '/compat/tree' } },
      { name: 'empty-tree', parts: { ...base, tree: '' } },
    ].map(({ name, parts }) => ({ name, parts, expected: ticketId(parts) }));
    const review = [];
    for (const revision of [undefined, '', 'acceptance-v2']) {
      const record = runRecordPath(base.run);
      await mkdir(path.dirname(record), { recursive: true });
      await writeFile(record, JSON.stringify({ blocks: { B1: { acceptance_revision: revision } } }));
      for (const reopened of [undefined, '2026-02-03T04:05:06.007Z']) {
        const parts = { ...base, acceptance_revision: revision, reopened_at: reopened };
        const result = resolveHashTicket({ ...base, repoRoot: home, after: reopened });
        review.push({ parts, expected: result.ticket });
      }
    }
    const ticket = { ids, review, states: ['src/é😀.mjs', 'a.mjs', 'nested/quote".mjs'].map((file) => ({
      file, expected: path.basename(statePath(base.run, base.block, file)),
    })) };

    const ledger = [];
    async function capture(name, slug) {
      const files = {};
      for (const file of (await readdir(ledgerDir())).filter((f) => f.startsWith(slug)).sort()) {
        files[file] = await readFile(path.join(ledgerDir(), file), 'utf8');
      }
      let current, all, error = false;
      try { current = await readRows(slug); all = await readAllRows(slug); }
      catch (err) { if (!(err instanceof SyntaxError)) throw err; error = true; }
      ledger.push({ name, slug, files, current, all, error });
    }
    const slug = 'compat';
    await appendRow(signRow({ event: 'run.start', ts: stamp, label: 'é😀' }, key), { slug });
    await appendRow(signRow({ event: 'session', ts: stamp, tokens_in: 7, cost_usd: 0.03 }, key), { slug, rotateAtBytes: 1 });
    await appendRow(signRow({ event: 'run.end', ts: stamp }, key), { slug });
    await appendFile(ledgerPath(slug), '{"event":"torn é😀');
    await capture('rotation-and-torn-tail', slug);
    for (let i = 0; i < 11; i++) {
      await appendRow({ event: 'row', ts: stamp, i }, { slug: 'ordered', rotateAtBytes: 1 });
    }
    await writeFile(path.join(ledgerDir(), 'ordered-other.99.jsonl'), 'unrelated corrupt data\n');
    await writeFile(path.join(ledgerDir(), 'ordered.3.jsonl.bak'), 'unrelated corrupt data\n');
    await capture('numeric-rotation-order-and-exact-names', 'ordered');
    const texts = [
      ['complete-no-newline', '{"event":"complete"}'],
      ['only-torn', '{"event":'],
      ['blank-lines', '\n{"event":"good"}\n\n'],
      ['invalid-final-with-newline', '{"event":"good"}\nbroken\n'],
      ['interior-corruption', 'broken\n{"event":"good"}\n'],
    ];
    for (const [name, text] of texts) {
      await writeFile(ledgerPath(name), text);
      await capture(name, name);
    }
    await capture('missing', 'missing');
    return { 'canon.json': canon, 'sign.json': sign, 'ticket.json': ticket, 'ledger.json': ledger };
  } finally {
    if (previousHome === undefined) delete process.env.HOME;
    else process.env.HOME = previousHome;
    await rm(home, { recursive: true, force: true });
  }
}

/** @param {string} out */
export async function generateGoldens(out, configOnly = false) {
  const kit = { ...(configOnly ? {} : await buildGoldens()), ...await buildConfigGoldens() };
  await mkdir(out, { recursive: true });
  for (const [name, value] of Object.entries(kit)) {
    await mkdir(path.dirname(path.join(out, name)), { recursive: true });
    await writeFile(path.join(out, name), `${JSON.stringify(value, null, 2)}\n`);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await generateGoldens(process.argv[2] ?? fileURLToPath(new URL('../test/compat/golden/', import.meta.url)), process.argv[3] === '--config');
}
