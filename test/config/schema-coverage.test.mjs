/**
 * `test/config/schema-coverage.test.mjs` (plan §1.3, named explicitly): greps `src/**` and
 * `skill/**` for `config.<path>` / `cfg.<path>` reads and asserts every path found exists in the
 * schema. The reverse (a schema path nothing reads yet) is a WARNING, never a failure — most of
 * the schema exists for blocks that haven't landed yet in Wave 1, so an empty "nothing reads this
 * yet" list would be the normal, expected state for most of the run.
 *
 * "Scaffolded" (per B1's acceptance): this sweeps whatever of `src/**`/`skill/**` exists TODAY
 * (B0's util files + B1's own config files) and will keep working, with no edits, as later blocks
 * land more `cfg.<path>` reads — that is the whole point of it living here instead of being
 * re-derived by every later block.
 *
 * The **37 dotted paths from §1.3's own contract table** are enumerated separately, in
 * `test/config/schema.test.mjs` (`CONFIG_CONTRACT_PATHS`) — THIS file only sweeps actual `cfg.`/
 * `config.` reads found in the codebase, a different (and narrower, code-driven) check.
 */
import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import schema from '../../schema/code-forge.schema.json' with { type: 'json' };
import { resolveSchemaPath } from '../../src/config/schema-paths.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.join(__dirname, '..', '..');

/**
 * A `cfg`/`config` identifier followed by one or more `.prop` / `?.prop` chain segments —
 * matches `cfg.escalation.stop_at`, `cfg?.review?.multimodel`, `config.provider`. Guarded on
 * BOTH sides: `(?<![\w$.])` in front rejects a preceding word character OR DOT (so `loadConfigFile`
 * — a longer identifier — and `this.config.x` / `options.config.x` — `config` as a property of
 * something else — are both excluded, not just a bare-word `\b` which only blocked the first
 * case), and requiring an immediate `.`/`?.` continuation after `cfg`/`config` rejects a bare
 * `cfg`/`config` with no chain at all.
 */
const CONFIG_READ_PATTERN = /(?<![\w$.])(?:cfg|config)((?:\?\.|\.)[A-Za-z_$][\w$]*(?:(?:\?\.|\.)[A-Za-z_$][\w$]*)*)/g;

/**
 * Property/method names from Array/String/Object prototypes that a config READ never legitimately
 * ends in — `cfg.review.models.length` and `cfg.fallback.map(...)` are reading a BUILT-IN off
 * some config value, not a schema key named "length"/"map" (MAJOR fix round 1, §153/154).
 */
const BUILTIN_MEMBER_NAMES = new Set([
  'length',
  'map',
  'filter',
  'forEach',
  'includes',
  'join',
  'slice',
  'some',
  'every',
  'find',
  'findIndex',
  'push',
  'pop',
  'shift',
  'unshift',
  'reduce',
  'reduceRight',
  'sort',
  'reverse',
  'concat',
  'flat',
  'flatMap',
  'toLowerCase',
  'toUpperCase',
  'trim',
  'trimStart',
  'trimEnd',
  'split',
  'replace',
  'replaceAll',
  'startsWith',
  'endsWith',
  'padStart',
  'padEnd',
  'toString',
  'valueOf',
  'hasOwnProperty',
  // NOT 'keys': it is a real §1.3 schema key (`keys.jev`) — stripping it would silently drop a
  // genuine `cfg.keys` read's last segment (round-2 MINOR).
  'values',
  'entries',
  'toJSON',
]);

/**
 * File extensions that follow `config.` in ordinary Markdown prose ("edit your config.yml") —
 * never a schema key. Only applied to `.md` files, and only to a single-segment match.
 */
const PROSE_FILE_EXTENSIONS = new Set(['yml', 'yaml', 'json', 'md', 'mjs', 'js', 'ts', 'toml']);

/**
 * @param {string} dir
 * @param {RegExp} extensionPattern
 * @returns {Promise<string[]>}
 */
async function collectFiles(dir, extensionPattern) {
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch (err) {
    if (/** @type {NodeJS.ErrnoException} */ (err).code === 'ENOENT') {
      return [];
    }
    throw err;
  }
  const files = [];
  for (const entry of entries) {
    if (entry.name === 'node_modules' || entry.name.startsWith('.')) {
      continue;
    }
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...(await collectFiles(full, extensionPattern)));
    } else if (entry.isFile() && extensionPattern.test(entry.name)) {
      files.push(full);
    }
  }
  return files;
}

/**
 * @param {string} text
 * @param {{prose?: boolean}} [opts] - `prose: true` for Markdown: a single-segment match that is a
 *   file extension (`config.yml`) is prose, not a read.
 * @returns {Set<string>} dotted paths, `?` stripped, leading `.` stripped, a trailing method-call
 *   (`(` or optional `?.(`) or known-builtin-member segment dropped.
 */
export function extractConfigReadPaths(text, opts = {}) {
  const paths = new Set();
  let match;
  CONFIG_READ_PATTERN.lastIndex = 0;
  while ((match = CONFIG_READ_PATTERN.exec(text))) {
    const rawPath = match[1].replace(/\?/g, '').replace(/^\./, '');
    let segments = rawPath.split('.');
    const after = CONFIG_READ_PATTERN.lastIndex;
    if (opts.prose && segments.length === 1 && PROSE_FILE_EXTENSIONS.has(segments[0])) {
      continue;
    }
    if (text[after] === '(' || text.startsWith('?.(', after)) {
      // `cfg.fallback.map(...)` / `cfg.fallback.map?.(...)` — the last segment is a METHOD CALL.
      segments = segments.slice(0, -1);
    } else if (segments.length > 1 && BUILTIN_MEMBER_NAMES.has(segments[segments.length - 1])) {
      // `cfg.review.models.length` — the last segment is a known Array/String/Object builtin
      // property, not a schema key (only stripped when there's a segment left underneath it).
      segments = segments.slice(0, -1);
    }
    if (segments.length > 0) {
      paths.add(segments.join('.'));
    }
  }
  return paths;
}

/**
 * @returns {Promise<{files: string[], readPaths: Set<string>}>} every scanned file path AND every
 *   `config.<path>`/`cfg.<path>` read found under `src/**` and `skill/**` (`.mjs` and `.md`, since
 *   `skill/references/*.md` is prose that names config keys too).
 */
async function collectAllReadPaths() {
  const files = [
    ...(await collectFiles(path.join(ROOT, 'src'), /\.mjs$/)),
    ...(await collectFiles(path.join(ROOT, 'skill'), /\.(mjs|md)$/)),
  ];
  const readPaths = new Set();
  for (const file of files) {
    const text = await readFile(file, 'utf8');
    for (const p of extractConfigReadPaths(text, { prose: file.endsWith('.md') })) {
      readPaths.add(p);
    }
  }
  return { files, readPaths };
}

/**
 * The real sweep both the "hard assertion" test and its own mutation-control test run — pulled
 * into one function so the mutation test proves something about the ACTUAL sweep, not a
 * hand-copied duplicate of its filter logic (MINOR fix round 1, §155/156).
 * @param {Record<string, any>} rootSchema
 * @param {Iterable<string>} readPaths
 * @returns {string[]} paths present in `readPaths` but missing from `rootSchema`.
 */
function findMissingPaths(rootSchema, readPaths) {
  return [...readPaths].filter((p) => resolveSchemaPath(rootSchema, p) === undefined);
}

// ── Unit: the extractor itself, on fixture strings (no filesystem) ──────────

test('extractConfigReadPaths finds dotted and optional-chained reads, ignores bare/unrelated identifiers', () => {
  const text = [
    'const a = cfg.escalation.stop_at;',
    'const b = cfg?.review?.multimodel;',
    'const c = config.provider;',
    'function loadConfigFile(cfg) { return cfg; }', // bare `cfg` (no chain) and a lookalike function name
    'const DEFAULT_CONFIG_FILENAME = ".code-forge.yml";',
  ].join('\n');
  const found = extractConfigReadPaths(text);
  assert.deepEqual([...found].sort(), ['escalation.stop_at', 'provider', 'review.multimodel'].sort());
});

test('extractConfigReadPaths ignores "config" preceded by a DOT — it is a property of something else, not the top-level config (MAJOR §153/154 fix)', () => {
  const text = ['this.config.apiKey;', 'options.config.retries;', 'foo.config.yml is a filename in prose.'].join('\n');
  assert.deepEqual([...extractConfigReadPaths(text)], []);
});

test('extractConfigReadPaths drops a trailing known-builtin property access (cfg.a.b.length -> "a.b")', () => {
  assert.deepEqual([...extractConfigReadPaths('const n = cfg.a.b.length;')], ['a.b']);
});

test('extractConfigReadPaths drops a trailing METHOD CALL (cfg.a.map(...) -> "a")', () => {
  assert.deepEqual([...extractConfigReadPaths('cfg.a.map((x) => x.id);')], ['a']);
});

test('extractConfigReadPaths drops a trailing OPTIONAL method call (cfg.a.map?.(...) -> "a")', () => {
  assert.deepEqual([...extractConfigReadPaths('cfg.a.map?.((x) => x.id);')], ['a']);
});

test('extractConfigReadPaths keeps a real "keys" segment (cfg.keys.jev -> "keys.jev", cfg.x.keys -> "x.keys") — "keys" is a schema key, not a stripped builtin', () => {
  assert.deepEqual([...extractConfigReadPaths('const a = cfg.keys.jev; const b = cfg.x.keys;')].sort(), ['keys.jev', 'x.keys']);
});

test('in Markdown prose mode, "config.yml"/"config.json" are file names, not reads — 0 paths; a real dotted read in the same prose is still found', () => {
  const prose = 'Edit your config.yml (or config.json), then check `cfg.escalation.stop_at`.';
  assert.deepEqual([...extractConfigReadPaths(prose, { prose: true })], ['escalation.stop_at']);
  assert.deepEqual([...extractConfigReadPaths(prose)].sort(), ['escalation.stop_at', 'json', 'yml'], 'control: without prose mode the extensions ARE extracted');
});

test('extractConfigReadPaths keeps a single-segment builtin-NAMED path when there is nothing left underneath it (cfg.length alone stays "length" — cfg itself is not a segment)', () => {
  // cfg.length has only ONE segment ("length") — stripping it would leave an empty path, which
  // is never useful; the strip only applies when there is a segment left beneath it.
  assert.deepEqual([...extractConfigReadPaths('cfg.length;')], ['length']);
});

// ── The sweep: every read found resolves against the schema (the hard assertion) ─

test('the sweep actually scans files (MAJOR §151/152 fix: a broken scan finding 0 files must fail, not pass vacuously)', async () => {
  const { files, readPaths } = await collectAllReadPaths();
  assert.ok(files.length >= 5, `expected to scan at least the known B0/B1 .mjs files, got ${files.length}`);
  assert.ok(
    files.some((f) => f.endsWith(path.join('src', 'config', 'validate.mjs'))),
    'expected src/config/validate.mjs to be among the scanned files',
  );
  // A path B1's own code is KNOWN to read — if the scan or extractor silently breaks, this goes
  // missing and the assertion below fails instead of the whole test passing on an empty set.
  assert.ok(readPaths.has('escalation.stop_at'), `expected "escalation.stop_at" among the reads found, got: ${[...readPaths].join(', ')}`);
});

test('every config.<path>/cfg.<path> read under src/** and skill/** resolves against the schema', async () => {
  const { readPaths } = await collectAllReadPaths();
  const missing = findMissingPaths(schema, readPaths);
  assert.deepEqual(missing, [], `config paths read in code but absent from the schema: ${missing.join(', ')}`);
});

test('mutation: findMissingPaths (the SAME function the sweep test calls) catches a near-miss typo path, and only the typo', () => {
  const nearMissAndValid = new Set(['escalation.stop_att', 'escalation.stop_at']); // typo + the real thing
  const missing = findMissingPaths(schema, nearMissAndValid);
  assert.deepEqual(missing, ['escalation.stop_att']);
});

// ── The reverse direction (a schema path nothing reads yet) is advisory — but provably wired up ─

/**
 * Every dotted path reachable through `properties` (dereferencing `$ref` first) or `items` — both
 * LEAF and INTERMEDIATE nodes are included (an intermediate object like `review` itself is a
 * legitimate thing code could read as a whole, e.g. `cfg.review`), so this is `collectSchemaPaths`,
 * not "leaf paths" (MINOR fix round 1, §159/160 — renamed, and now dereferences `$ref`).
 * @param {Record<string, any>} rootSchema
 * @param {Record<string, any>} node
 * @param {string} prefix
 * @param {Set<string>} out
 * @param {Set<object>} [onPath]
 */
function collectSchemaPaths(rootSchema, node, prefix, out, onPath = new Set()) {
  if (!node || typeof node !== 'object' || onPath.has(node)) return;
  const deref = node.$ref
    ? node.$ref
        .replace(/^#\//, '')
        .split('/')
        .reduce((acc, part) => acc?.[part], rootSchema)
    : node;
  if (!deref || typeof deref !== 'object' || onPath.has(deref)) return;
  onPath.add(deref);
  try {
    if (deref.properties && typeof deref.properties === 'object') {
      for (const [key, child] of Object.entries(deref.properties)) {
        const dotted = prefix ? `${prefix}.${key}` : key;
        out.add(dotted);
        collectSchemaPaths(rootSchema, child, dotted, out, onPath);
      }
    }
    if (deref.items) {
      collectSchemaPaths(rootSchema, deref.items, prefix, out, onPath);
    }
  } finally {
    onPath.delete(deref);
  }
}

/**
 * @param {Record<string, any>} rootSchema
 * @returns {Set<string>} every dotted schema path `collectSchemaPaths` finds from the root.
 */
function allSchemaPaths(rootSchema) {
  const schemaPaths = new Set();
  collectSchemaPaths(rootSchema, rootSchema, '', schemaPaths);
  return schemaPaths;
}

/**
 * @returns {Promise<{schemaPaths: Set<string>, unread: string[]}>} the collector's full output AND
 *   the schema paths nothing under `src/**`/`skill/**` reads yet — the latter informational only
 *   (plan §1.3: "the reverse is a warning"), never a failure.
 */
async function unreadSchemaPaths() {
  const schemaPaths = allSchemaPaths(schema);
  const { readPaths } = await collectAllReadPaths();
  return { schemaPaths, unread: [...schemaPaths].filter((p) => !readPaths.has(p)) };
}

/**
 * Paths the collector MUST produce — spread over `properties` depth 1-3, a `$ref`-dereferenced
 * level (`levels.L0.model`), and an array's `items` (`levels.L0.fallback.provider`).
 */
const KNOWN_SCHEMA_PATHS = Object.freeze([
  'version',
  'escalation',
  'escalation.stop_at',
  'review.budgets.quick_in',
  'levels.L0.model',
  'levels.L3.fallback',
  'levels.L3.fallback.provider',
  'proof.tiers.high.paths',
  'gates.extra.secret_scan',
  'telemetry',
  // v1.3 (B1.1): the six new collector paths behind the 142 -> 148 re-pin.
  'review.max_rounds_per_file',
  'review.recheck_scope',
  'review.late_findings',
  'budget.block_cases',
  'tmp',
  'tmp.root',
]);

/**
 * Exact collector output size for the current schema (measured 2026-09-24, round 3; B1.1 2026-09-25:
 * +6 = review.{max_rounds_per_file,recheck_scope,late_findings}, budget.block_cases, tmp, tmp.root;
 * B1.2 2026-09-25 (Q16 cut): -8 = proof.tiers.high.{tool,min_msi} (2 leaves) and the whole
 * proof.js{,.tool,.min_msi} / proof.nightly{,.enabled,.report_only} subtrees (3 + 3 paths) — the
 * mutation-tool-only keys leave the schema per plan §10.4 Wave 6. 148 - 8 = 140. The depth-3
 * KNOWN_SCHEMA_PATHS sample under `proof.tiers.high` is re-pointed from the now-gone `.min_msi` to
 * the surviving `.paths`) — re-pin, and say why in the commit, whenever the schema gains or loses
 * a property.
 */
const EXPECTED_SCHEMA_PATH_COUNT = 140;

test('collectSchemaPaths produces EXACTLY the pinned number of paths and every KNOWN path — so an empty or broken collector fails here', () => {
  const schemaPaths = allSchemaPaths(schema);
  const missing = KNOWN_SCHEMA_PATHS.filter((p) => !schemaPaths.has(p));
  assert.deepEqual(missing, []);
  assert.equal(schemaPaths.size, EXPECTED_SCHEMA_PATH_COUNT);
});

test('mutation: a collector run on a schema with one property removed loses exactly that path', () => {
  const mutated = structuredClone(schema);
  delete mutated.properties.escalation.properties.stop_at;
  const schemaPaths = allSchemaPaths(mutated);
  assert.equal(schemaPaths.has('escalation.stop_at'), false);
  assert.equal(schemaPaths.size, EXPECTED_SCHEMA_PATH_COUNT - 1);
});

test('unreadSchemaPaths(): escalation.stop_at IS a schema path AND is excluded from the unread list because validate.mjs reads it — the reverse direction is wired on both sides', async (t) => {
  const { schemaPaths, unread } = await unreadSchemaPaths();
  t.diagnostic(`${unread.length} schema path(s) not yet read by any landed code: ${unread.slice(0, 20).join(', ')}${unread.length > 20 ? ', …' : ''}`);
  assert.equal(schemaPaths.has('escalation.stop_at'), true, 'the collector side must produce the path first');
  assert.equal(schemaPaths.size, EXPECTED_SCHEMA_PATH_COUNT);
  assert.equal(unread.includes('escalation.stop_at'), false, 'escalation.stop_at IS read by validate.mjs — it must not appear in the unread list');
  assert.ok(unread.length < schemaPaths.size, 'at least one schema path is read by landed code');
});
