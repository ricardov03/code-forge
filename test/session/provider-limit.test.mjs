import { BINS, cfgWith, fakeDeps, freshDir, readRecords, writeIn } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

/**
 * B40: per-provider session slots (`review.provider_concurrency`) and the rate-limit backoff in
 * `spawnSession`. Fake CLIs and exec doubles only; the backoff sleep and jitter are injected, so no
 * test waits for real.
 */

const { spawnSession, providerSemaphore, providerLimit, backoffMs, DEFAULT_PROVIDER_CONCURRENCY } = await import('../../src/session/spawn.mjs');
const { exec } = await import('../../src/util/exec.mjs');
const { validateConfig, configSchema } = await import('../../src/config/validate.mjs');

const CLAUDE = { provider: 'anthropic', model: 'fake-opus' };
const GROK = { provider: 'xai', model: 'fake-grok' };

/** @param {string} stdout */
const okRes = (stdout) => ({ result: 'ok', code: 0, signal: null, timedOut: false, stderr: '', stdout });
const CLAUDE_OK = okRes(`${JSON.stringify({ type: 'result', result: 'fine' })}\n`);
const GROK_OK = okRes(`${JSON.stringify({ text: 'fine' })}\n`);
const RATE_LIMITED = { result: 'failed', code: 1, signal: null, timedOut: false, stderr: '', stdout: `${JSON.stringify({ type: 'result', is_error: true, api_error_status: 429, result: 'API Error: 429' })}\n` };

/** @param {Record<string, any>} level @param {Record<string, number>} limits */
const cfgLimited = (level, limits) => ({ ...cfgWith(/** @type {any} */ (level)), review: { provider_concurrency: limits } });

/** A judge session (no schema) on `cfg`. @param {Record<string, any>} cfg @param {Record<string, any>} [extra] */
function judge(cfg, extra = {}) {
  return { cfg, level: /** @type {const} */ ('L2'), role: /** @type {const} */ ('judge'), promptPath: writeIn(freshDir('pk'), 'packet.md', 'decide'), ...extra };
}

test('providerLimit: defaults anthropic 4, openai 2, xai 2; a set integer >= 1 wins; 0 / "3" fall back to the default', () => {
  assert.deepEqual({ ...DEFAULT_PROVIDER_CONCURRENCY }, { anthropic: 4, openai: 2, xai: 2 });
  assert.deepEqual(['anthropic', 'openai', 'xai'].map((p) => providerLimit({}, p)), [4, 2, 2]);
  const cfg = { review: { provider_concurrency: { anthropic: 7, openai: 0, xai: '3' } } };
  assert.deepEqual(['anthropic', 'openai', 'xai'].map((p) => providerLimit(cfg, p)), [7, 2, 2]);
});

test('schema: review.provider_concurrency has defaults 4/2/2; integers >= 1 validate, 0 and an unknown provider are 1 error each', () => {
  const node = configSchema.properties.review.properties.provider_concurrency;
  assert.deepEqual(
    Object.fromEntries(Object.entries(node.properties).map(([k, v]) => [k, /** @type {any} */ (v).default])),
    { anthropic: 4, openai: 2, xai: 2 },
  );
  const base = { version: 1, provider: 'anthropic', levels: { L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' }, L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' } } };
  const good = validateConfig({ ...base, review: { provider_concurrency: { anthropic: 3, openai: 1, xai: 2 } } });
  assert.deepEqual([good.valid, good.errors.length], [true, 0]);
  const zero = validateConfig({ ...base, review: { provider_concurrency: { anthropic: 0 } } });
  assert.deepEqual([zero.valid, zero.errors.length], [false, 1]);
  const unknown = validateConfig({ ...base, review: { provider_concurrency: { mistral: 1 } } });
  assert.deepEqual([unknown.valid, unknown.errors.length], [false, 1]);
});

test('8 concurrent spawns with a sleeping fake CLI: max active anthropic == 2 and xai == 1 (their limits), both providers overlap, all 8 ok, slots 0 after', async () => {
  const { deps, records } = fakeDeps({ FAKE_SLEEP_MS: '300' });
  const limits = { anthropic: 2, xai: 1 };
  /** @type {Record<string, number>} */
  const active = { claude: 0, grok: 0 };
  /** @type {Record<string, number>} */
  const maxActive = { claude: 0, grok: 0 };
  let maxTotal = 0;
  /** @type {typeof exec} */
  const counting = async (argv, opts) => {
    const name = argv[0] === BINS.claude ? 'claude' : 'grok';
    active[name] += 1;
    maxActive[name] = Math.max(maxActive[name], active[name]);
    maxTotal = Math.max(maxTotal, active.claude + active.grok);
    try {
      return await exec(argv, opts);
    } finally {
      active[name] -= 1;
    }
  };
  const runs = [
    ...Array.from({ length: 5 }, () => spawnSession(judge(cfgLimited(CLAUDE, limits)), { ...deps, exec: counting })),
    ...Array.from({ length: 3 }, () => spawnSession(judge(cfgLimited(GROK, limits)), { ...deps, exec: counting })),
  ];
  const results = await Promise.all(runs);
  assert.deepEqual(results.map((r) => r.status), ['ok', 'ok', 'ok', 'ok', 'ok', 'ok', 'ok', 'ok']);
  assert.deepEqual([maxActive.claude, maxActive.grok, maxTotal], [2, 1, 3]);
  assert.equal(readRecords(records).length, 8);
  assert.deepEqual([providerSemaphore('anthropic', 2).active, providerSemaphore('xai', 1).active], [0, 0]);
});

test('a slot is held during the attempt and freed on each exit path: ok, unavailable (402), timeout, exec throw', async () => {
  const limits = { anthropic: 3 };
  const sem = providerSemaphore('anthropic', 3);
  const http402 = { result: 'failed', code: 1, signal: null, timedOut: false, stderr: '', stdout: `${JSON.stringify({ type: 'result', is_error: true, api_error_status: 402, result: 'API Error: 402' })}\n` };
  const timedOut = { result: 'timeout', code: null, signal: 'SIGTERM', timedOut: true, stderr: '', stdout: '' };
  /** @type {Array<[string, () => Promise<any>]>} */
  const cases = [
    ['ok', async () => CLAUDE_OK],
    ['unavailable', async () => http402],
    ['timeout', async () => timedOut],
  ];
  /** @type {Array<[string, number, number]>} */
  const seen = [];
  for (const [status, res] of cases) {
    const { deps } = fakeDeps();
    let during = -1;
    const result = await spawnSession(judge(cfgLimited(CLAUDE, limits)), {
      ...deps,
      exec: /** @type {any} */ (
        async () => {
          during = sem.active;
          return res();
        }
      ),
    });
    seen.push([result.status, during, sem.active]);
    assert.equal(result.status, status);
  }
  const { deps } = fakeDeps();
  let during = -1;
  await assert.rejects(
    spawnSession(judge(cfgLimited(CLAUDE, limits)), {
      ...deps,
      exec: /** @type {any} */ (
        async () => {
          during = sem.active;
          throw new Error('exec exploded');
        }
      ),
    }),
    { message: 'exec exploded' },
  );
  seen.push(['throw', during, sem.active]);
  assert.deepEqual(seen, [
    ['ok', 1, 0],
    ['unavailable', 1, 0],
    ['timeout', 1, 0],
    ['throw', 1, 0],
  ]);
});

test('budget refusal takes no slot: with all 2 slots held elsewhere, a refused session still returns at once, 0 exec calls, slots stay 2', async () => {
  const sem = providerSemaphore('anthropic', 2);
  const releases = [await sem.acquire(), await sem.acquire()];
  try {
    const { deps } = fakeDeps();
    let execCalls = 0;
    const cfg = { ...cfgLimited(CLAUDE, { anthropic: 2 }), budget: { usd: 20 } };
    const refused = spawnSession(judge(cfg, { run: 'r-full-slots' }), {
      ...deps,
      exec: /** @type {any} */ (
        async () => {
          execCalls += 1;
          return CLAUDE_OK;
        }
      ),
      readRows: async () => [{ event: 'session', run: 'r-full-slots', usd: 25 }],
      writeRow: async () => {},
    });
    /** @type {NodeJS.Timeout | undefined} */
    let timer;
    const hung = new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error('refused session waited for a slot')), 2000);
    });
    try {
      const result = await Promise.race([refused, hung]);
      assert.deepEqual([result.status, result.reason, execCalls, sem.active, sem.pending], ['unavailable', 'budget', 0, 2, 0]);
    } finally {
      clearTimeout(timer);
    }
  } finally {
    for (const release of releases) release();
  }
  assert.equal(sem.active, 0);
});

test('rate-limited on every try: waits exactly [2000, 6000] (random 0.5), 3 anthropic attempts, then the xai fallback runs; the slot is free during each wait', async () => {
  const { deps, stderr } = fakeDeps();
  const sem = providerSemaphore('anthropic', 4);
  /** @type {Array<[number, number]>} */
  const waits = [];
  const calls = /** @type {string[]} */ ([]);
  const cfg = cfgLimited({ ...CLAUDE, fallback: [GROK] }, { anthropic: 4 });
  const result = await spawnSession(judge(cfg), {
    ...deps,
    exec: /** @type {any} */ (
      async (/** @type {string[]} */ argv) => {
        const name = argv[0] === BINS.claude ? 'claude' : 'grok';
        calls.push(name);
        return name === 'claude' ? RATE_LIMITED : GROK_OK;
      }
    ),
    sleep: async (/** @type {number} */ ms) => void waits.push([ms, sem.active]),
    random: () => 0.5,
  });
  assert.deepEqual(waits, [
    [2000, 0],
    [6000, 0],
  ]);
  assert.deepEqual(calls, ['claude', 'claude', 'claude', 'grok']);
  assert.deepEqual(
    [result.status, result.fallback_step, result.attempts.map((/** @type {any} */ a) => `${a.provider}:${a.status}:${a.reason}`)],
    ['ok', 1, ['anthropic:unavailable:rate-limited', 'anthropic:unavailable:rate-limited', 'anthropic:unavailable:rate-limited', 'xai:ok:null']],
  );
  const waitLines = stderr.text().split('\n').filter((l) => l.startsWith('rate-limited: '));
  assert.deepEqual(waitLines, ['rate-limited: provider=anthropic model=fake-opus retry in 2.0 s', 'rate-limited: provider=anthropic model=fake-opus retry in 6.0 s']);
  assert.equal(sem.active, 0);
});

test('rate-limited once at limit 1: during the 2000 ms wait another session on the same provider runs to ok; then the retry succeeds on step 0', { timeout: 10_000 }, async () => {
  const { deps } = fakeDeps();
  const cfg = cfgLimited(CLAUDE, { anthropic: 1 });
  let first = true;
  const execDouble = /** @type {any} */ (
    async () => {
      if (first) {
        first = false;
        return RATE_LIMITED;
      }
      return CLAUDE_OK;
    }
  );
  /** @type {number[]} */
  const waits = [];
  /** @type {string[]} */
  const during = [];
  const result = await spawnSession(judge(cfg), {
    ...deps,
    exec: execDouble,
    random: () => 0.5,
    sleep: async (/** @type {number} */ ms) => {
      waits.push(ms);
      // would deadlock if the waiting session still held the only slot
      const other = await spawnSession(judge(cfg), { ...deps, exec: execDouble });
      during.push(other.status);
    },
  });
  assert.deepEqual([waits, during, result.status, result.fallback_step, result.attempts.length], [[2000], ['ok'], 'ok', 0, 2]);
  assert.equal(providerSemaphore('anthropic', 1).active, 0);
});

test('rate-limited on the last step: after waits [2000, 6000] the third rate limit is the result (unavailable, rate-limited, 3 attempts)', async () => {
  const { deps } = fakeDeps();
  /** @type {number[]} */
  const waits = [];
  const result = await spawnSession(judge(cfgWith(CLAUDE)), {
    ...deps,
    exec: /** @type {any} */ (async () => RATE_LIMITED),
    sleep: async (/** @type {number} */ ms) => void waits.push(ms),
    random: () => 0.5,
  });
  assert.deepEqual([waits, result.status, result.reason, result.attempts.length], [[2000, 6000], 'unavailable', 'rate-limited', 3]);
});

test('backoffMs: jitter is ±30 % — random 0 gives 1400 / 4200, random 0.5 gives 2000 / 6000, random 0.99999 gives 2600 / 7800', () => {
  assert.deepEqual([backoffMs(0, () => 0), backoffMs(1, () => 0)], [1400, 4200]);
  assert.deepEqual([backoffMs(0, () => 0.5), backoffMs(1, () => 0.5)], [2000, 6000]);
  assert.deepEqual([backoffMs(0, () => 0.99999), backoffMs(1, () => 0.99999)], [2600, 7800]);
});
