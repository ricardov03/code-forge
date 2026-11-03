// Regenerate with: node scripts/compat-golden.mjs [output-directory]
// With no output-directory, also refresh the Go embedded config schema.
// Every expected value comes from the production Node implementation.
import { appendFile, copyFile, mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
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
import { PROVIDER_DEFAULTS, defaultsForProvider } from '../src/config/defaults/index.mjs';
import { validateConfig } from '../src/config/validate.mjs';
import { loadSeenInCache, refreshFromCliCaches, userModelsCachePath } from '../src/config/refresh.mjs';
import { Ajv2020 } from 'ajv/dist/2020.js';

const stamp = '2026-01-02T03:04:05.006Z';
const key = Buffer.from('000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f', 'hex');

/**
 * Config inputs are complete filesystem recipes. Paths in oracle output use
 * $ROOT so committed bytes are independent of temp directories and HOME.
 * @typedef {{name: string, group?: string, operation?: string, provider?: string, pattern?: string, value?: string, unsupported?: boolean, config?: Record<string, any>, files?: Record<string, string>, dirs?: string[], links?: Record<string, string>, cwd?: string, home?: string}} ConfigCase
 */

// Keep Date values in the config oracle without changing ledger canonicalization.
/** @param {any} value @returns {any} */
function configJSONValue(value) {
  if (value instanceof Date) return value.toISOString();
  if (Array.isArray(value)) return value.map(configJSONValue);
  if (value !== null && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([key, child]) => [key, configJSONValue(child)]));
  }
  return value;
}

export async function buildConfigGoldens() {
  const repo = fileURLToPath(new URL('../', import.meta.url));
  const { version } = JSON.parse(await readFile(path.join(repo, 'package.json'), 'utf8'));
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
  yaml('migrate/large-version', 'version: 1e21\n');
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
    'explicit-timestamps': 'values: [!!timestamp 2026-01-02, !!timestamp 2026-01-02T03:04:05.6789Z, !!timestamp 2026-1-2t3:4:5-5, !!timestamp 2026-01-02 03:04:05 +02:30]',
    'plain-hash': 'url: a#b\n',
    'plain-apostrophe-bracket': "msg: it's [\n",
    'plain-reserved-middle': 'note: a @b\n',
    'plain-hash-before-flow-error': 'url: a#b [\nbad: [1, 2}\n',
    'flow-hash-before-mismatch': 'items: [a#b, 2}\n',
    'plain-apostrophe-before-flow-error': "msg: it's [\nbad: [1, 2}\n",
    'plain-reserved-before-flow-error': 'note: a @b\nbad: [1, 2}\n',
    'block-tab-before-flow-error': 'note: |\n  first line\n  \tlegal tab [ @x\nbad: [1, 2}\n',
    'quoted-tab-before-flow-error': 'note: "text\n  \tlegal tab"\nbad: [1, 2}\n',
    'sequence-before-implicit-key': 'items:\n  - item\nvalid: yes\nmissing value\nnext: ok\n',
    'sequence-before-missing-colon': 'items:\n  - item\nvalid: yes\nmissing value\n',
    'block-before-implicit-key': 'note: |\n  content\nvalid: yes\nmissing value\nnext: ok\n',
    'sequence-reserved': 'items:\n  - @x\n',
    'root-sequence-reserved': '- @x\n',
    'flow-reserved': 'items: [ok,@x]\n',
    'flow-first-reserved': 'items: [@x]\n',
    'indented-reserved': 'items:\n  `x\n',
    'comment-before-flow-error': 'url: a # [\nbad: [1, 2}\n',
    'quoted-before-flow-error': 'msg: "it\'s ["\nbad: [1, 2}\n',
    'unresolved-scalar-tags': 'values: [!!float 1, !!int 1.2, !!int true, !!null false, !!bool 1, !!str null]',
    'lenient-binary-tag': 'values: [!!binary invalid, !!binary "SGVsbG8_***", !!binary "SG=ignored"]',
    'invalid-timestamp-tag': 'value: !!timestamp not-a-date',
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
    'invalid-escape': 'a: "bad\\q"\n',
    'unterminated-quote': 'a: "unterminated\n',
    'missing-colon': 'a: 1\nb 2\n',
    'nested-compact-mapping': 'a:\n b: 1\n  c: 2\n',
    'reserved-scalar': 'a: @bad\n',
    'unmasked-alias-prefix': 'value: *task-sk-fake12345678901234567890',
    'unicode-duplicate': 'value: {emoji: "😀", model: a, model: b}',
    'multiple-documents': 'provider: openai\n---\nprovider: xai\n',
    empty: '', 'root-null': 'null', 'root-string': 'hello', 'root-number': '42', 'root-boolean': 'true', 'root-list': '[one, two]',
  })) yaml(`yaml/${name}`, text);
  for (const count of [99, 100]) yaml(`yaml/alias-limit-${count}`, `value: &one scalar\naliases: [${Array(count).fill('*one').join(', ')}]`);
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

  for (const provider of ['anthropic', 'openai', 'xai', 'constructor', '__proto__', '', 'unknown']) {
    inputs.push({ name: `provider-defaults/${provider || 'empty'}`, operation: 'defaults', provider });
  }
  const base = { version: 1, ...PROVIDER_DEFAULTS.openai };
  for (const [name, extra] of Object.entries({
    valid: {}, string: { project: { name: 'hello' } }, integer: { budget: { block_lines: 42 } },
    number: { calibration: { shadow_rate: 0.25 } }, boolean: { native: { daemon: false } },
    null: { tmp: { root: null } }, array: { production: { names: ['one', 'two'] } }, object: { context: { commands: { test: ['node', '--test'] } } },
    'rerun-default': { gates: {} }, 'rerun-enabled': { gates: { rerun_failed: 1 } },
    'rerun-disabled': { gates: { rerun_failed: 0 } },
    'rerun-negative': { gates: { rerun_failed: -1 } }, 'rerun-too-many': { gates: { rerun_failed: 2 } },
    'rerun-fractional': { gates: { rerun_failed: 0.5 } }, 'rerun-string': { gates: { rerun_failed: '1' } },
    'rerun-null': { gates: { rerun_failed: null } },
    'unknown-key': { unknown: true }, 'nested-unknown': { caps: { nope: 1 } },
    'wrong-string': { project: { name: 42 } }, 'wrong-integer': { budget: { block_lines: 1.5 } },
    'wrong-number': { calibration: { shadow_rate: '0.1' } }, 'wrong-boolean': { native: { daemon: 1 } },
    'wrong-null-union': { tmp: { root: 2 } }, 'wrong-array': { production: { names: 'one' } }, 'wrong-object': { context: [] },
    'missing-required': { levels: {} }, enum: { provider: 'other' }, const: { telemetry: 'on' },
    'enum-wrong-type': { provider: 42 }, 'const-wrong-type': { telemetry: true },
    minimum: { budget: { block_lines: 0 } }, maximum: { calibration: { shadow_rate: 2 } },
    'exclusive-minimum': { budget: { usd: 0 } }, 'min-length': { levels: { ...base.levels, L0: { model: '' } } },
    'min-items': { context: { commands: { test: [] } } }, 'max-items': { gates: { custom: Array.from({ length: 21 }, (_, i) => ({ name: `gate${i}`, argv: ['true'] })) } },
    unique: { context: { rules: ['same', 'same'] } }, pattern: { coder: { sandbox: { add_dirs: ['relative'] } } },
    'property-names': { review: { second_levels: { L9: { model: 'x' } } } },
    'pattern-properties': { proof: { runners: [{ adapter: 'junit', match: ['*'], argv: ['test', '{report}', '{test_file}'], env: { 'bad-key': 'value' } }] } },
    'conditional-required': { proof: { runners: [{ adapter: 'xcodebuild', match: ['*'], argv: ['xcodebuild'] }] } },
    'conditional-else': { proof: { runners: [{ adapter: 'junit', match: ['*'], argv: ['test'], xcode: { scheme: 's', destination: 'd', test_target: 't' } }] } },
    'dependent-schema': { proof: { runners: [{ adapter: 'xcodebuild', match: ['*'], argv: ['xcodebuild'], xcode: { workspace: 'a', project: 'b', scheme: 'a', destination: 'b', test_target: 'a' } }] } },
    contains: { gates: { custom: [{ name: 'build', argv: ['node', '{test_files}'] }] } },
    not: { gates: { custom: [{ name: 'test', argv: ['node'] }] } },
    'evidence-pattern-valid': { gates: { custom: [{ name: 'build', argv: ['node'], evidence: ['reports/**/*.json'] }] } },
    'evidence-pattern-invalid': { gates: { custom: [{ name: 'build', argv: ['node'], evidence: ['../outside'] }] } },
    'nul-pattern': { proof: { worktree: { setup: [['node', '\u0000']] } } },
  })) inputs.push({ name: `validate/${name}`, operation: 'schema', files: { 'project/.code-forge.yml': JSON.stringify({ ...base, ...extra }) } });
  inputs.push(...inputs.slice(0, fixtureCount).map((sample) => ({ ...sample, name: `validate-sample/${sample.name}`, operation: 'schema' })));
  const sourceSchema = JSON.parse(await readFile(path.join(repo, 'schema/code-forge.schema.json'), 'utf8'));
  const evidencePattern = sourceSchema.properties.gates.properties.custom.items.properties.evidence.items.pattern;
  for (const [name, pattern, value, unsupported] of [
    ['evidence-valid', evidencePattern, 'reports/**/*.json', false],
    ['evidence-parent', evidencePattern, '../outside', false],
    ['changed-evidence-suffix', `${evidencePattern}extra`, 'reports/result.json', true],
    ['same-prefix-other-rule', '^(?!/)allowed$', 'other', true],
    ['other-unsupported', '(?<=prefix)value', 'prefixvalue', true],
    ['re2-supported', '^allowed$', 'other', false],
  ]) inputs.push({ name: `pattern/${name}`, group: 'pattern', operation: 'pattern', pattern, value, unsupported });
  const refreshInputs = {
    missing: {},
    'bare-arrays': { 'home/.codex/models_cache.json': '["b","a","b",7]', 'home/.grok/models_cache.json': '["z","z",false]' },
    'wrapped-arrays': { 'home/.codex/models_cache.json': '{"models":["b","a"]}', 'home/.grok/models_cache.json': '{"models":["y","x"]}' },
    'real-shapes': { 'home/.codex/models_cache.json': '{"models":[{"slug":"gpt-test"},{"slug":""},{"other":1},null]}', 'home/.grok/models_cache.json': '{"models":{"one":{"info":{"id":"grok-test"}},"fallback":{},"empty":{"info":{"id":""}}}}' },
    malformed: { 'home/.codex/models_cache.json': 'not json', 'home/.grok/models_cache.json': '["good"]' },
    'malformed-position': { 'home/.codex/models_cache.json': '{"models":[] ,}', 'home/.grok/models_cache.json': '["good"]' },
    'json-numbers-and-key-order': { 'home/.codex/models_cache.json': '{"models":[1e999,"good"]}', 'home/.grok/models_cache.json': '{"models":{"2":{},"1":{},"z":{},"z":{"info":{"id":"last"}}}}' },
    'unrecognized-shapes': { 'home/.codex/models_cache.json': '{"models":{}}', 'home/.grok/models_cache.json': '42' },
    'merge-existing': { 'home/.codex/models_cache.json': '["new","a"]', 'home/.code-forge/cache/models.json': '{"seen":{"openai":["old","a"],"other":["preserve"],"bad":1,"__proto__":["no"],"constructor":["no"],"prototype":["no"]}}' },
    'corrupt-existing': { 'home/.codex/models_cache.json': '["good"]', 'home/.code-forge/cache/models.json': 'not json' },
    'wrong-existing-shape': { 'home/.codex/models_cache.json': '["good"]', 'home/.code-forge/cache/models.json': '{"seen":["wrong"]}' },
  };
  for (const [name, files] of Object.entries(refreshInputs)) inputs.push({ name: `refresh/${name}`, operation: 'refresh', files });
  inputs.push({ name: 'refresh/unreadable-source', operation: 'refresh', dirs: ['home/.codex/models_cache.json'], files: { 'home/.grok/models_cache.json': '["good"]' } });

  const groups = { 'fixtures.json': [], 'valid.json': [], 'errors.json': [], 'roots.json': [], 'operations.json': [] };
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
      const normalize = (text) => text.split(root).join('$ROOT');
      let expected;
      if (input.operation === 'pattern') {
        const validate = new Ajv2020().compile({ type: 'string', pattern: input.pattern });
        expected = { ok: true, canonical: canonicalJSON(validate(input.value)) };
      } else if (input.operation === 'defaults') {
        expected = { ok: true, canonical: canonicalJSON(defaultsForProvider(input.provider) ?? null) };
      } else if (input.operation === 'schema') {
        const loaded = await loadProjectConfig(path.join(root, cwd));
        if (!loaded.ok) throw new Error(`${input.name}: schema input must load before validation`);
        const errors = validateConfig(loaded.config, { hasCliOnPath: () => true }).errors.filter((issue) => issue.rule === 'schema');
        expected = errors.length ? { ok: false, error: 'schema', message: errors.map((issue) => issue.message).join('\n') }
          : { ok: true, canonical: canonicalJSON(configJSONValue(loaded.config)) };
      } else if (input.operation === 'refresh') {
        const result = await refreshFromCliCaches({ home: process.env.HOME });
        const { updated_at, ...cache } = JSON.parse(await readFile(userModelsCachePath(process.env.HOME), 'utf8'));
        expected = { ok: true, canonical: normalize(canonicalJSON({ result, cache, seen: await loadSeenInCache({ home: process.env.HOME }) })) };
      } else {
        const result = await loadProjectConfig(path.join(root, cwd));
        expected = result.ok
          ? { ok: true, path: normalize(result.path), canonical: canonicalJSON(configJSONValue(result.config)) }
          : { ok: false, path: normalize(result.path), error: result.error, message: normalize(result.message) };
      }
      // Package releases must not change golden messages; readers expand this token before comparing.
      if (expected.message) expected.message = expected.message.replaceAll(version, '{{version}}');
      const group = input.group ?? (expected.ok ? 'valid' : 'error');
      const entry = { ...input, group, home, cwd, expected, ...(group === 'root' ? { root: normalize(projectRootFor(path.join(root, cwd))) } : {}) };
      groups[input.operation ? 'operations.json' : group === 'root' ? 'roots.json' : index < fixtureCount ? 'fixtures.json' : expected.ok ? 'valid.json' : 'errors.json'].push(entry);
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
  if (!process.argv[2]) {
    await copyFile(new URL('../schema/code-forge.schema.json', import.meta.url), new URL('../native/config/schema.json', import.meta.url));
  }
}
