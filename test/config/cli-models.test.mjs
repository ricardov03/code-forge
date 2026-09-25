/**
 * `code-forge models` CLI tests. The `--refresh --from-cli-caches` acceptance clause is explicit
 * (coder-rules.md): it MUST use fixture caches under `test/fixtures/cli-caches/**` copied into a
 * temp HOME, and MUST NOT read the real `~/.codex` or `~/.grok`.
 *
 * `tmpHome` (HOME, where the CLI caches and the user-level models.json cache live) and
 * `tmpProjectDir` (cwd, where `.code-forge.yml` would live) are two SEPARATE directories — never
 * the same one — so a test asserting ".code-forge.yml is untouched" is proving something about a
 * REAL project directory, not a coincidence of HOME and cwd being the same path (MAJOR §129/130 fix).
 */
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cp, mkdir, mkdtemp, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, test } from 'node:test';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.join(__dirname, '..', '..');
const BIN = path.join(ROOT, 'bin', 'code-forge.mjs');
const FIXTURES = path.join(__dirname, '..', 'fixtures', 'cli-caches');

const EXPECTED_OPENAI_IDS = ['gpt-6-luna', 'gpt-6-nova-preview'];
const EXPECTED_XAI_IDS = ['grok-4.7', 'grok-5-preview'];

/** @type {string} */
let tmpHome;
/** @type {string} */
let tmpProjectDir;

before(async () => {
  const tmpRoot = await mkdtemp(path.join(os.tmpdir(), 'code-forge-cli-models-test-'));
  tmpHome = path.join(tmpRoot, 'home');
  tmpProjectDir = path.join(tmpRoot, 'project');
  await mkdir(tmpHome, { recursive: true });
  await mkdir(tmpProjectDir, { recursive: true });
  await mkdir(path.join(tmpHome, '.codex'), { recursive: true });
  await mkdir(path.join(tmpHome, '.grok'), { recursive: true });
  await cp(path.join(FIXTURES, 'codex', 'models_cache.json'), path.join(tmpHome, '.codex', 'models_cache.json'));
  await cp(path.join(FIXTURES, 'grok', 'models_cache.json'), path.join(tmpHome, '.grok', 'models_cache.json'));
});

after(async () => {
  await rm(path.dirname(tmpHome), { recursive: true, force: true });
});

/**
 * A MINIMAL env — never `...process.env`, which could let a real CODEX_HOME/GROK_HOME/XDG_* on the
 * host machine leak the real CLI caches into an otherwise-hermetic test. The binary runs through
 * `process.execPath` (absolute), so node needs no PATH; PATH points at a non-existent directory
 * under the temp root instead of the real one.
 * @param {string[]} args
 * @param {{home?: string}} [opts]
 */
function runCli(args, opts = {}) {
  return spawnSync(process.execPath, [BIN, ...args], {
    encoding: 'utf8',
    env: { PATH: path.join(path.dirname(tmpHome), 'no-bin'), HOME: opts.home ?? tmpHome },
    cwd: tmpProjectDir,
  });
}

/**
 * @param {string} text
 * @param {string} needle
 */
function occurrences(text, needle) {
  return text.split(needle).length - 1;
}

/**
 * The `<provider>: \n  <id...>` blocks in `models` stdout, as `{provider: [ids...]}` — used to
 * assert GROUPING (an id lives under the right heading), not just "the substring is somewhere".
 * @param {string} stdout
 * @returns {Record<string, string[]>}
 */
function parseGroupedOutput(stdout) {
  /** @type {Record<string, string[]>} */
  const groups = {};
  let current = null;
  for (const rawLine of stdout.split('\n')) {
    const line = rawLine.replace(/^\[code-forge:info\]\s?/, '');
    const heading = /^(\w[\w.-]*):$/.exec(line);
    if (heading) {
      current = heading[1];
      groups[current] = [];
      continue;
    }
    const idLine = /^ {2}(\S+)/.exec(line);
    if (idLine && current) {
      groups[current].push(idLine[1]);
    }
  }
  return groups;
}

// ── Grouping, not just presence (MINOR §91/92) ───────────────────────────────

test('code-forge models (no flags) exits 0 and lists ids under EXACTLY 3 provider headings, each id under its own provider', () => {
  const { status, stdout, stderr } = runCli(['models']);
  assert.equal(status, 0, stderr);
  const groups = parseGroupedOutput(stdout);
  assert.deepEqual(Object.keys(groups).sort(), ['anthropic', 'openai', 'xai']);
  assert.ok(groups.anthropic.includes('claude-opus-5-5'), `claude-opus-5-5 missing from anthropic group: ${JSON.stringify(groups.anthropic)}`);
  assert.equal(groups.openai.includes('claude-opus-5-5'), false, 'claude-opus-5-5 must not appear under openai');
});

test('code-forge models --refresh (without --from-cli-caches) exits 2 and writes NO cache file (no side effect from a refused refresh)', async () => {
  const { status, stderr } = runCli(['models', '--refresh']);
  assert.equal(status, 2);
  assert.match(stderr, /--from-cli-caches/);
  await assert.rejects(readFile(path.join(tmpHome, '.code-forge', 'cache', 'models.json'), 'utf8'), { code: 'ENOENT' });
});

// ── MAJOR fix: exact written content, not existence-only checks ─────────────

test('code-forge models --refresh --from-cli-caches reads the 2 fixture caches and writes the EXACT id sets, nothing extra, status: seen-in-cache', async () => {
  const { status, stdout, stderr } = runCli(['models', '--refresh', '--from-cli-caches']);
  assert.equal(status, 0, stderr);
  assert.match(stdout, /refreshed from 2 CLI cache\(s\)/);
  assert.match(stdout, /seen-in-cache/);

  const cacheFile = path.join(tmpHome, '.code-forge', 'cache', 'models.json');
  const written = JSON.parse(await readFile(cacheFile, 'utf8'));
  assert.equal(written.status, 'seen-in-cache');
  assert.deepEqual(Object.keys(written.seen).sort(), ['openai', 'xai']);
  assert.deepEqual(written.seen.openai.slice().sort(), EXPECTED_OPENAI_IDS.slice().sort());
  assert.deepEqual(written.seen.xai.slice().sort(), EXPECTED_XAI_IDS.slice().sort());
});

test('after --refresh --from-cli-caches, "models" tags ONLY the genuinely-new ids as (seen-in-cache) — an id ALSO shipped in the catalog is deduped, printed once, untagged', () => {
  const first = runCli(['models', '--refresh', '--from-cli-caches']);
  assert.equal(first.status, 0, first.stderr);

  const { status, stdout } = runCli(['models']);
  assert.equal(status, 0);
  const seenInCacheLines = stdout.split('\n').filter((l) => l.includes('(seen-in-cache)'));
  const seenInCacheIds = seenInCacheLines.map((l) => l.replace(/^\[code-forge:info\]\s?/, '').trim().split(' ')[0]);
  // gpt-6-luna and grok-4.7 are ALSO in the shipped catalog (KNOWN_IDS) — the dedup fix (MINOR
  // §19/20) prints each id once, tagged by its highest-precedence source, so only the two ids
  // that are genuinely NOT in the catalog carry the seen-in-cache tag.
  assert.deepEqual(seenInCacheIds.sort(), ['gpt-6-nova-preview', 'grok-5-preview'].sort());

  const groups = parseGroupedOutput(stdout);
  assert.ok(groups.anthropic.includes('claude-opus-5-5'), 'a shipped catalog id must still be listed');
  assert.equal(stdout.includes('claude-opus-5-5 (seen-in-cache)'), false, 'a shipped catalog id must never carry the seen-in-cache tag');
  assert.equal(stdout.includes('gpt-6-luna (seen-in-cache)'), false, 'gpt-6-luna is ALSO shipped in the catalog — must be deduped, not double-tagged');
  assert.equal((stdout.match(/\bgpt-6-luna\b/g) ?? []).length, 1, 'gpt-6-luna must appear exactly once in the whole listing, not twice');
});

// ── The real CLI-level "0 bytes changed" acceptance clause (MAJOR §129/130 fix) ─

test('--refresh --from-cli-caches leaves the PROJECT dir (separate from HOME) with EXACTLY the same entries — here none, so no .code-forge.yml or stray file appears', async () => {
  const before = (await readdir(tmpProjectDir)).sort();
  assert.deepEqual(before, [], 'precondition: the project dir starts empty');

  const { status } = runCli(['models', '--refresh', '--from-cli-caches']);
  assert.equal(status, 0);

  assert.deepEqual((await readdir(tmpProjectDir)).sort(), before);
});

test('a TRUNCATED Grok cache holding an api_key: --refresh reports the skip, and the fake key appears 0 times in stdout+stderr', async () => {
  const fakeKey = 'xai-FAKESECRET0123456789abcdefghij';
  const home = path.join(path.dirname(tmpHome), 'home-truncated-grok');
  await mkdir(path.join(home, '.grok'), { recursive: true });
  // Cut off mid-entry, exactly where a CLI crash mid-write would leave it — V8's SyntaxError text
  // quotes a slice of THIS source, which is how a key used to reach the skip reason.
  await writeFile(
    path.join(home, '.grok', 'models_cache.json'),
    `{"models":{"grok-4.7":{"info":{"id":"grok-4.7"},"api_key":"${fakeKey}`,
    'utf8',
  );
  const { status, stdout, stderr } = runCli(['models', '--refresh', '--from-cli-caches'], { home });
  assert.equal(status, 0, stderr);
  const skipLines = stderr.split('\n').filter((l) => l.includes('skipped '));
  assert.equal(skipLines.length, 1, stderr);
  assert.match(skipLines[0], /models_cache\.json: not valid JSON( \(at position \d+\))?$/);
  assert.equal(occurrences(stdout + stderr, fakeKey), 0);
  assert.equal(occurrences(stdout + stderr, 'FAKESECRET'), 0);
});

test('--refresh --from-cli-caches changes an EXISTING project .code-forge.yml (in the PROJECT dir, separate from HOME) by exactly 0 bytes', async () => {
  const configPath = path.join(tmpProjectDir, '.code-forge.yml');
  const originalBytes = Buffer.from('version: 1\nprovider: anthropic\nlevels:\n  L0:\n    model: claude-haiku-4-5-20251001\n');
  await writeFile(configPath, originalBytes);
  try {
    const { status } = runCli(['models', '--refresh', '--from-cli-caches']);
    assert.equal(status, 0);

    const afterBytes = await readFile(configPath);
    assert.equal(afterBytes.length, originalBytes.length, 'byte length must be unchanged');
    assert.ok(afterBytes.equals(originalBytes), '.code-forge.yml must be byte-for-byte identical after --refresh');
  } finally {
    await rm(configPath, { force: true });
  }
});

// ── known_extra union + load-failure warning (MINOR §15/16, §17/18) ─────────

test('a project .code-forge.yml with known_extra contributes ids to the listing, tagged (known_extra), grouped under the right provider', async () => {
  const configPath = path.join(tmpProjectDir, '.code-forge.yml');
  await writeFile(
    configPath,
    'version: 1\nprovider: anthropic\nlevels:\n  L0:\n    model: claude-haiku-4-5-20251001\nknown_extra:\n  anthropic:\n    - claude-custom-internal\n',
  );
  try {
    const { status, stdout, stderr } = runCli(['models']);
    assert.equal(status, 0, stderr);
    const groups = parseGroupedOutput(stdout);
    assert.equal(groups.anthropic.filter((id) => id === 'claude-custom-internal').length, 1, 'listed exactly once under anthropic');
    assert.equal(groups.openai.includes('claude-custom-internal'), false);
    assert.equal(groups.xai.includes('claude-custom-internal'), false);
    assert.equal(occurrences(stdout, 'claude-custom-internal'), 1, 'exactly one line in the whole listing');
    assert.equal(occurrences(stdout, 'claude-custom-internal (known_extra)'), 1);
    assert.doesNotMatch(stderr, /could not read known_extra/);
  } finally {
    await rm(configPath, { force: true });
  }
});

test('a project known_extra whose provider value is NOT a list (a number) does not crash the listing — exit 0, the catalog still grouped under exactly 3 providers', async () => {
  const configPath = path.join(tmpProjectDir, '.code-forge.yml');
  await writeFile(configPath, 'version: 1\nprovider: anthropic\nlevels:\n  L0:\n    model: claude-haiku-4-5-20251001\nknown_extra:\n  anthropic: 42\n');
  try {
    const { status, stdout, stderr } = runCli(['models']);
    assert.equal(status, 0, stderr);
    assert.deepEqual(Object.keys(parseGroupedOutput(stdout)).sort(), ['anthropic', 'openai', 'xai']);
  } finally {
    await rm(configPath, { force: true });
  }
});

test('a project .code-forge.yml that fails to load (malformed YAML, a fake key on the bad line) warns with the location, never echoes the key, and still lists exactly the 3 catalog providers', async () => {
  const fakeKey = 'sk-ant-FAKE0123456789';
  const configPath = path.join(tmpProjectDir, '.code-forge.yml');
  await writeFile(configPath, `version: 1\napi_key: [${fakeKey}\n`);
  try {
    const { status, stdout, stderr } = runCli(['models']);
    assert.equal(status, 0);
    const warnLines = stderr.split('\n').filter((l) => l.includes('could not read known_extra'));
    assert.equal(warnLines.length, 1, stderr);
    assert.match(warnLines[0], /parse-error: [A-Z_]+: .+ \(line \d+, column \d+\)$/);
    assert.equal(occurrences(stdout + stderr, fakeKey), 0, `leaked:\n${stdout}\n${stderr}`);
    assert.deepEqual(Object.keys(parseGroupedOutput(stdout)).sort(), ['anthropic', 'openai', 'xai']);
  } finally {
    await rm(configPath, { force: true });
  }
});
