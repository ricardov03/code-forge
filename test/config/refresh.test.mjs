/**
 * `refresh.mjs` tests — MUST NOT read the real `~/.codex` or `~/.grok` (coder-rules.md: the
 * `models --refresh --from-cli-caches` test uses fixture caches under `test/fixtures/cli-caches/**`
 * and a temp HOME, never the real machine's caches). Every test here builds its own `mkdtemp()`
 * directory and passes it as `{home: tmpHome}` explicitly — `refreshFromCliCaches`/`loadSeenInCache`
 * never fall back to `os.homedir()` when `home` is supplied.
 *
 * The fixtures under `test/fixtures/cli-caches/{codex,grok}/models_cache.json` mirror the REAL
 * cache shapes read once from this machine's own CLI caches on 2026-09-24 (Codex: `models` is an
 * ARRAY of objects, id at `.slug`; Grok: `models` is an OBJECT keyed by id, id at `.info.id`) —
 * every test below that asserts on extracted ids is exercising that real-shape extraction, not a
 * simplified stand-in. `gpt-6-luna`/`gpt-6-nova-preview` (codex) and `grok-4.7`/`grok-5-preview`
 * (grok) are the ids both fixtures carry.
 */
import assert from 'node:assert/strict';
import { cp, mkdir, mkdtemp, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, beforeEach, test } from 'node:test';
import { loadSeenInCache, refreshFromCliCaches, userModelsCachePath } from '../../src/config/refresh.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const FIXTURES_DIR = path.join(__dirname, '..', 'fixtures', 'cli-caches');
const CODEX_FIXTURE = path.join(FIXTURES_DIR, 'codex', 'models_cache.json');
const GROK_FIXTURE = path.join(FIXTURES_DIR, 'grok', 'models_cache.json');

const EXPECTED_OPENAI_IDS = ['gpt-6-luna', 'gpt-6-nova-preview'];
const EXPECTED_XAI_IDS = ['grok-4.7', 'grok-5-preview'];

/** @type {string} */
let tmpRoot;
/** @type {string} */
let tmpHome;

before(async () => {
  tmpRoot = await mkdtemp(path.join(os.tmpdir(), 'code-forge-refresh-test-'));
});

after(async () => {
  await rm(tmpRoot, { recursive: true, force: true });
});

beforeEach(async () => {
  tmpHome = path.join(tmpRoot, `home-${Math.random().toString(36).slice(2)}`);
  await mkdir(tmpHome, { recursive: true });
});

/** Copies the two fixture CLI caches into `<tmpHome>/.codex/` and `<tmpHome>/.grok/`. */
async function installBothFixtureCaches() {
  await mkdir(path.join(tmpHome, '.codex'), { recursive: true });
  await mkdir(path.join(tmpHome, '.grok'), { recursive: true });
  await cp(CODEX_FIXTURE, path.join(tmpHome, '.codex', 'models_cache.json'));
  await cp(GROK_FIXTURE, path.join(tmpHome, '.grok', 'models_cache.json'));
}

/** Every file path under `dir`, recursively (relative to `dir`, POSIX-joined). */
async function listFilesRecursively(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...(await listFilesRecursively(full)));
    } else {
      files.push(full);
    }
  }
  return files;
}

// ── Acceptance: reads 2 fixture caches (REAL shapes), writes models.json, status: seen-in-cache ─

test('refreshFromCliCaches reads exactly the 2 fixture cache paths, nothing else', async () => {
  await installBothFixtureCaches();
  const result = await refreshFromCliCaches({ home: tmpHome });
  assert.deepEqual(
    [...result.sourcesRead].sort(),
    [path.join(tmpHome, '.codex', 'models_cache.json'), path.join(tmpHome, '.grok', 'models_cache.json')].sort(),
  );
  assert.deepEqual(result.sourcesSkipped, []);
});

test('refreshFromCliCaches extracts the exact real-shape ids (codex .slug array, grok .info.id map) and writes status: "seen-in-cache"', async () => {
  await installBothFixtureCaches();
  const result = await refreshFromCliCaches({ home: tmpHome });
  assert.equal(result.cachePath, userModelsCachePath(tmpHome));
  const written = JSON.parse(await readFile(result.cachePath, 'utf8'));
  assert.equal(written.status, 'seen-in-cache');
  assert.deepEqual(Object.keys(written.seen).sort(), ['openai', 'xai']);
  assert.deepEqual(written.seen.openai.slice().sort(), EXPECTED_OPENAI_IDS.slice().sort());
  assert.deepEqual(written.seen.xai.slice().sort(), EXPECTED_XAI_IDS.slice().sort());
});

test('the CLI-level acceptance ("changes .code-forge.yml 0 bytes") lives in test/config/cli-models.test.mjs, spawning the real verb with a project dir separate from HOME — this file only proves the library function never even names that file', async () => {
  await installBothFixtureCaches();
  const before = await listFilesRecursively(tmpHome);
  assert.equal(before.some((f) => f.endsWith('.code-forge.yml')), false, 'precondition: no such file exists yet');

  await refreshFromCliCaches({ home: tmpHome });

  const after = await listFilesRecursively(tmpHome);
  assert.equal(after.some((f) => f.endsWith('.code-forge.yml')), false, 'refreshFromCliCaches must never create a .code-forge.yml anywhere');
});

test('loadSeenInCache round-trips the EXACT id sets refreshFromCliCaches wrote, for use as validate.mjs opts.seenInCache', async () => {
  await installBothFixtureCaches();
  await refreshFromCliCaches({ home: tmpHome });
  const seen = await loadSeenInCache({ home: tmpHome });
  assert.deepEqual(Object.keys(seen).sort(), ['openai', 'xai']);
  assert.deepEqual(seen.openai.slice().sort(), EXPECTED_OPENAI_IDS.slice().sort());
  assert.deepEqual(seen.xai.slice().sort(), EXPECTED_XAI_IDS.slice().sort());
});

// ── Missing-source and merge behavior ────────────────────────────────────────

test('refreshFromCliCaches with NEITHER fixture cache present reads 0 sources, skips 0, and does not throw', async () => {
  const result = await refreshFromCliCaches({ home: tmpHome });
  assert.deepEqual(result.sourcesRead, []);
  assert.deepEqual(result.sourcesSkipped, []);
  assert.deepEqual(result.seenThisRun, {});
});

test('refreshFromCliCaches with only ONE cache present (codex) reads exactly 1 source with its exact ids', async () => {
  await mkdir(path.join(tmpHome, '.codex'), { recursive: true });
  await cp(CODEX_FIXTURE, path.join(tmpHome, '.codex', 'models_cache.json'));
  const result = await refreshFromCliCaches({ home: tmpHome });
  assert.deepEqual(result.sourcesRead, [path.join(tmpHome, '.codex', 'models_cache.json')]);
  assert.deepEqual(Object.keys(result.seenThisRun), ['openai']);
  assert.deepEqual(result.seenThisRun.openai.slice().sort(), EXPECTED_OPENAI_IDS.slice().sort());
});

test('running refreshFromCliCaches twice merges (union), never duplicates, ids across runs — and keeps the OTHER provider intact', async () => {
  await installBothFixtureCaches();
  await refreshFromCliCaches({ home: tmpHome });

  // Second run's codex fixture now has one NEW id in addition to the original two; grok's cache
  // is untouched on disk (still present from the first run's install).
  await writeFile(
    path.join(tmpHome, '.codex', 'models_cache.json'),
    JSON.stringify({ models: [{ slug: 'gpt-6-luna' }, { slug: 'gpt-6-brand-new' }] }),
    'utf8',
  );
  const result = await refreshFromCliCaches({ home: tmpHome });
  const written = JSON.parse(await readFile(result.cachePath, 'utf8'));

  assert.deepEqual(
    written.seen.openai.slice().sort(),
    ['gpt-6-brand-new', 'gpt-6-luna', 'gpt-6-nova-preview'].sort(),
    'the union across both runs, with no duplicate entries',
  );
  assert.deepEqual(
    written.seen.xai.slice().sort(),
    EXPECTED_XAI_IDS.slice().sort(),
    'xai ids from the first run must survive a second run that only touches codex',
  );
  assert.equal(written.status, 'seen-in-cache', 'status must still read seen-in-cache after a merge, not be dropped/renamed');
});

test('loadSeenInCache returns {} for a HOME that has never been refreshed', async () => {
  const seen = await loadSeenInCache({ home: tmpHome });
  assert.deepEqual(seen, {});
});

// ── Partial-failure isolation: one bad source never aborts the other (MINOR §53/54) ─

test('a codex cache that is not valid JSON is skipped with a reason naming ITS OWN path — the grok cache is still read', async () => {
  await installBothFixtureCaches();
  await writeFile(path.join(tmpHome, '.codex', 'models_cache.json'), 'not json{{{', 'utf8');

  const result = await refreshFromCliCaches({ home: tmpHome });

  assert.deepEqual(result.sourcesRead, [path.join(tmpHome, '.grok', 'models_cache.json')]);
  assert.equal(result.sourcesSkipped.length, 1);
  assert.equal(result.sourcesSkipped[0].path, path.join(tmpHome, '.codex', 'models_cache.json'));
  assert.match(result.sourcesSkipped[0].reason, /not valid JSON/);
  // The .codex path must NOT appear in the grok skip/read entries — proves the isolation is
  // per-source, not a global "something failed" flag.
  assert.equal(result.sourcesSkipped[0].path.includes('.grok'), false);
});

test('a cache that parses but yields 0 ids (unrecognised shape) is skipped with a reason, not recorded as a successful empty read', async () => {
  await mkdir(path.join(tmpHome, '.codex'), { recursive: true });
  await writeFile(path.join(tmpHome, '.codex', 'models_cache.json'), JSON.stringify({ totally: 'unrelated' }), 'utf8');

  const result = await refreshFromCliCaches({ home: tmpHome });

  assert.deepEqual(result.sourcesRead, []);
  assert.equal(result.sourcesSkipped.length, 1);
  assert.equal(result.sourcesSkipped[0].path, path.join(tmpHome, '.codex', 'models_cache.json'));
  assert.match(result.sourcesSkipped[0].reason, /no model ids could be read/);
});

test('a TRUNCATED grok cache holding an api_key is skipped with a reason that never quotes the source — the fake key appears 0 times', async () => {
  const fakeKey = 'xai-FAKESECRET0123456789abcdefghij';
  await mkdir(path.join(tmpHome, '.grok'), { recursive: true });
  await writeFile(
    path.join(tmpHome, '.grok', 'models_cache.json'),
    `{"models":{"grok-4.7":{"info":{"id":"grok-4.7"},"api_key":"${fakeKey}`,
    'utf8',
  );
  const result = await refreshFromCliCaches({ home: tmpHome });
  assert.equal(result.sourcesSkipped.length, 1);
  assert.equal(result.sourcesSkipped[0].path, path.join(tmpHome, '.grok', 'models_cache.json'));
  assert.match(result.sourcesSkipped[0].reason, /^not valid JSON( \(at position \d+\))?$/);
  assert.equal(JSON.stringify(result).includes('FAKESECRET'), false);
});

test('a source that exists but can\'t be READ (a directory, EISDIR) is skipped as "unreadable (EISDIR)" — the OTHER provider\'s valid cache is still read', async () => {
  await mkdir(path.join(tmpHome, '.codex', 'models_cache.json'), { recursive: true }); // a DIRECTORY at the cache path
  await mkdir(path.join(tmpHome, '.grok'), { recursive: true });
  await cp(GROK_FIXTURE, path.join(tmpHome, '.grok', 'models_cache.json'));

  const result = await refreshFromCliCaches({ home: tmpHome });

  assert.deepEqual(result.sourcesRead, [path.join(tmpHome, '.grok', 'models_cache.json')]);
  assert.deepEqual(result.sourcesSkipped, [
    { path: path.join(tmpHome, '.codex', 'models_cache.json'), reason: 'unreadable (EISDIR)' },
  ]);
});

// ── Malformed EXISTING user-level cache is discarded, never crashes a refresh (MINOR §49) ─

test('a corrupt (non-JSON) existing user cache is discarded, not thrown', async () => {
  await installBothFixtureCaches();
  const cachePath = userModelsCachePath(tmpHome);
  await mkdir(path.dirname(cachePath), { recursive: true });
  await writeFile(cachePath, 'not json at all{{{', 'utf8');

  const result = await refreshFromCliCaches({ home: tmpHome });
  const written = JSON.parse(await readFile(result.cachePath, 'utf8'));
  assertExactRebuild(written);
});

/**
 * The rebuilt file holds EXACTLY this run's two providers and ids, with the documented status —
 * a partial or garbled rebuild fails here.
 * @param {any} written
 */
function assertExactRebuild(written) {
  assert.equal(written.status, 'seen-in-cache');
  assert.deepEqual(Object.keys(written.seen).sort(), ['openai', 'xai']);
  assert.deepEqual(written.seen.openai.slice().sort(), EXPECTED_OPENAI_IDS.slice().sort());
  assert.deepEqual(written.seen.xai.slice().sort(), EXPECTED_XAI_IDS.slice().sort());
}

test('an existing user cache whose seen.<provider> is a STRING (not an array) is discarded for that provider, never split into characters', async () => {
  await installBothFixtureCaches();
  const cachePath = userModelsCachePath(tmpHome);
  await mkdir(path.dirname(cachePath), { recursive: true });
  await writeFile(cachePath, JSON.stringify({ status: 'seen-in-cache', seen: { openai: 'gpt-6-luna' } }), 'utf8');

  const result = await refreshFromCliCaches({ home: tmpHome });
  const written = JSON.parse(await readFile(result.cachePath, 'utf8'));
  // If the string had been treated as an array-like, `new Set('gpt-6-luna')` would inject single
  // characters ('g', 'p', 't', '-', …) as bogus "ids" alongside the real ones.
  assert.deepEqual(written.seen.openai.slice().sort(), EXPECTED_OPENAI_IDS.slice().sort());
  assert.equal(written.seen.openai.includes('g'), false);
});

test('an existing user cache whose top-level document is null is discarded, not crashed on', async () => {
  await installBothFixtureCaches();
  const cachePath = userModelsCachePath(tmpHome);
  await mkdir(path.dirname(cachePath), { recursive: true });
  await writeFile(cachePath, 'null', 'utf8');

  const result = await refreshFromCliCaches({ home: tmpHome });
  const written = JSON.parse(await readFile(result.cachePath, 'utf8'));
  assertExactRebuild(written);
});

test('loadSeenInCache on a CORRUPT (non-JSON) cache returns {} — never throws', async () => {
  const cachePath = userModelsCachePath(tmpHome);
  await mkdir(path.dirname(cachePath), { recursive: true });
  await writeFile(cachePath, 'not json{{{', 'utf8');
  assert.deepEqual(await loadSeenInCache({ home: tmpHome }), {});
});

test('loadSeenInCache when the cache path is a DIRECTORY (EISDIR) returns {} — never throws', async () => {
  await mkdir(userModelsCachePath(tmpHome), { recursive: true });
  assert.deepEqual(await loadSeenInCache({ home: tmpHome }), {});
});

test('a "__proto__" provider key in the cache is dropped — the result keeps Object.prototype and only the real provider', async () => {
  const cachePath = userModelsCachePath(tmpHome);
  await mkdir(path.dirname(cachePath), { recursive: true });
  await writeFile(cachePath, '{"seen":{"__proto__":["x"],"openai":["gpt-6-luna"]}}', 'utf8');

  const seen = await loadSeenInCache({ home: tmpHome });
  assert.equal(Object.getPrototypeOf(seen), Object.prototype, 'the id array must not have become the prototype');
  assert.deepEqual(Object.keys(seen), ['openai']);
  assert.deepEqual(seen, { openai: ['gpt-6-luna'] });
  assert.equal(/** @type {any} */ (seen).length, undefined, 'an inherited array member must not resolve');
});

test('loadSeenInCache on a malformed cache (seen.<provider> not an array of strings) returns {} for that provider, not the raw garbage', async () => {
  const cachePath = userModelsCachePath(tmpHome);
  await mkdir(path.dirname(cachePath), { recursive: true });
  await writeFile(cachePath, JSON.stringify({ seen: { openai: 'not-an-array', xai: ['grok-4.7'] } }), 'utf8');

  const seen = await loadSeenInCache({ home: tmpHome });
  assert.deepEqual(seen, { xai: ['grok-4.7'] });
});

// ── Atomic write: no stray temp file survives a successful run (MINOR §55/56) ─

test('a successful refresh leaves EXACTLY models.json in the cache directory — no temp file of any name', async () => {
  await installBothFixtureCaches();
  const result = await refreshFromCliCaches({ home: tmpHome });
  assert.deepEqual(await readdir(path.dirname(result.cachePath)), ['models.json']);
});
