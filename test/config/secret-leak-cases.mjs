/**
 * One case per validator rule (plus the schema layer), each planting a DIFFERENT fake secret in a
 * config that triggers that rule — shared by `validate.test.mjs` (in-process: the messages and
 * the whole result object) and `cli-validate.test.mjs` (the `validate` verb's stdout + stderr), so
 * both layers are held to "the secret appears 0 times" on the same inputs.
 *
 * Counts assume `hasCliOnPath` answers false for every provider (the CLI test runs the verb with a
 * PATH that contains no provider CLI) and an empty seen-in-cache.
 *
 * Every secret here is fake and built at runtime from a repeated letter, so no real-looking token
 * is committed.
 */

/** @param {string} prefix @param {string} letter @param {number} n */
const fake = (prefix, letter, n) => `${prefix}${letter.repeat(n)}`;

/** @returns {Record<string, any>} a fresh, valid, 0-warning config. */
export function baseConfig() {
  return {
    version: 1,
    provider: 'anthropic',
    levels: {
      L0: { model: 'claude-haiku-4-5-20251001' },
      L1: { model: 'claude-sonnet-5' },
      L2: { model: 'claude-opus-5-5' },
      L3: { model: 'claude-fable-5-1' },
    },
  };
}

/**
 * @typedef {object} SecretLeakCase
 * @property {string} name
 * @property {string} rule - the rule the case triggers.
 * @property {number} count - exact number of `rule` issues expected.
 * @property {number} secretHits - exact number of `secret-looking-value` issues expected.
 * @property {string} secret
 * @property {(secret: string) => Record<string, any>} build
 */

/** @type {SecretLeakCase[]} */
export const SECRET_LEAK_CASES = [
  {
    name: 'level-missing (no model)',
    rule: 'level-missing',
    count: 1,
    secretHits: 1,
    secret: fake('sk-', 'A', 40),
    build: (s) => {
      const c = baseConfig();
      c.levels.L1 = { effort: s };
      return c;
    },
  },
  {
    name: 'level-missing (no provider)',
    rule: 'level-missing',
    count: 1,
    secretHits: 1,
    secret: fake('sk-ant-', 'B', 30),
    build: (s) => {
      const c = baseConfig();
      delete c.provider;
      c.levels.L0.provider = 'anthropic';
      c.levels.L1.provider = 'anthropic';
      c.levels.L3.provider = 'anthropic';
      c.levels.L2.effort = s;
      return c;
    },
  },
  {
    name: 'unknown-model-id',
    rule: 'unknown-model-id',
    count: 1,
    secretHits: 1,
    secret: fake('sk-', 'C', 40),
    build: (s) => {
      const c = baseConfig();
      c.levels.L0.model = s;
      return c;
    },
  },
  {
    name: 'duplicate-level-tuple',
    rule: 'duplicate-level-tuple',
    count: 1,
    secretHits: 2,
    secret: fake('xai-', 'D', 40),
    build: (s) => {
      const c = baseConfig();
      c.levels.L0 = { model: s };
      c.levels.L1 = { model: s };
      return c;
    },
  },
  {
    name: 'stop-at-not-l3',
    rule: 'stop-at-not-l3',
    count: 1,
    secretHits: 1,
    secret: fake('ghp_', 'E', 36),
    build: (s) => {
      const c = baseConfig();
      c.escalation = { stop_at: s };
      return c;
    },
  },
  {
    name: 'multimodel-missing-second-provider',
    rule: 'multimodel-missing-second-provider',
    count: 1,
    secretHits: 1,
    secret: fake('sk-', 'F', 40),
    build: (s) => {
      const c = baseConfig();
      c.levels.L3 = { model: 'grok-4.7', provider: 'xai' };
      c.levels.L2.effort = s;
      c.review = { multimodel: true };
      return c;
    },
  },
  {
    name: 'multimodel-effective-l2-collision',
    rule: 'multimodel-effective-l2-collision',
    count: 1,
    secretHits: 1,
    secret: fake('sk-', 'G', 40),
    build: (s) => {
      const c = baseConfig();
      c.levels.L3 = { model: 'grok-4.7', provider: 'xai' };
      c.review = { multimodel: true, second_provider: 'openai', second_levels: { L2: { model: s, provider: 'anthropic' } } };
      return c;
    },
  },
  {
    name: 'judge-family-collision',
    rule: 'judge-family-collision',
    count: 1,
    secretHits: 1,
    secret: fake('xoxb-', 'H', 30),
    build: (s) => {
      const c = baseConfig();
      c.levels.L3.model = s;
      c.review = { multimodel: true, second_provider: 'openai' };
      return c;
    },
  },
  {
    name: 'orchestrator-l3-multimodel-off',
    rule: 'orchestrator-l3-multimodel-off',
    count: 1,
    secretHits: 1,
    secret: fake('AKIA', 'J', 16),
    build: (s) => {
      const c = baseConfig();
      c.orchestrator = 'L3';
      c.levels.L3.effort = s;
      return c;
    },
  },
  {
    name: 'gates-not-argv-array',
    rule: 'gates-not-argv-array',
    count: 1,
    secretHits: 1,
    secret: fake('sk-', 'K', 40),
    build: (s) => {
      const c = baseConfig();
      c.gates = { test: s };
      return c;
    },
  },
  {
    name: 'secret-looking-value (plain value)',
    rule: 'secret-looking-value',
    count: 1,
    secretHits: 1,
    secret: fake('AIza', 'L', 35),
    build: (s) => {
      const c = baseConfig();
      c.project = { name: s };
      return c;
    },
  },
  {
    name: 'secret-looking-value (secret as a map KEY)',
    rule: 'secret-looking-value',
    count: 1,
    secretHits: 1,
    secret: fake('sk-', 'M', 40),
    build: (s) => {
      const c = baseConfig();
      c.keys = { [s]: 'env:SOME_VAR' };
      return c;
    },
  },
  {
    name: 'secret-looking-value (embedded bearer token in a gate argv)',
    rule: 'secret-looking-value',
    count: 1,
    secretHits: 1,
    secret: fake('sk-', 'N', 40),
    build: (s) => {
      const c = baseConfig();
      c.gates = { test: ['curl', '-H', `Authorization: Bearer ${s}`] };
      return c;
    },
  },
  {
    name: 'fallback-unknown-or-cli-absent',
    rule: 'fallback-unknown-or-cli-absent',
    count: 1,
    secretHits: 1,
    secret: fake('github_pat_', 'P', 30),
    build: (s) => {
      const c = baseConfig();
      c.levels.L3.fallback = [{ provider: 'openai', model: s }];
      return c;
    },
  },
  {
    name: 'engine-subprocess-no-cli',
    rule: 'engine-subprocess-no-cli',
    count: 1,
    secretHits: 1,
    secret: fake('sk-', 'Q', 40),
    build: (s) => {
      const c = baseConfig();
      c.engine = 'subprocess';
      c.levels.L0.provider = s;
      return c;
    },
  },
  {
    name: 'shadow-rate-range',
    rule: 'shadow-rate-range',
    count: 1,
    secretHits: 1,
    secret: fake('xai-', 'S', 40),
    build: (s) => {
      const c = baseConfig();
      c.calibration = { shadow_rate: 0.9 };
      c.project = { slug: s };
      return c;
    },
  },
  {
    name: 'schema (unknown key holding a secret value)',
    rule: 'schema',
    count: 1,
    secretHits: 1,
    secret: fake('sk-', 'T', 40),
    build: (s) => {
      const c = baseConfig();
      c.bogus_key = s;
      return c;
    },
  },
  {
    name: 'schema (unknown key that IS a secret)',
    rule: 'schema',
    count: 1,
    secretHits: 1,
    secret: fake('sk-', 'U', 40),
    build: (s) => {
      const c = baseConfig();
      c[s] = 'x';
      return c;
    },
  },
];
