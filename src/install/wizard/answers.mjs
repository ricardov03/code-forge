/**
 * The wizard's answers (plan §2, block B13a): what each of the nine steps proposes, from the
 * project on disk and the machine, then an existing `.code-forge.yml` on top (so a re-run
 * proposes what is already there — the idempotent diff), then the flags on top of that.
 * `sources[key]` records where each answer came from: `default`, `existing` or `flag`.
 * Pure apart from the reads in {@link gatherContext}.
 */

import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { defaultsForProvider } from '../../config/defaults/index.mjs';
import { detectGates } from '../../gates/detect.mjs';
import { detectAgentEnv } from '../agent-env.mjs';
import { detectAll } from '../detect.mjs';
import { ANSWER_FLAGS, PROVIDERS } from './flags.mjs';

/** Harnesses that expose a subagent tool (§5.1: Claude Code's `Agent`, [A13]). */
export const SUBAGENT_HARNESSES = Object.freeze(['claude']);

/**
 * `copy_untracked` proposed per stack (§2 step 8, C5). Laravel is any PHP project (`composer.json`)
 * with an `artisan` file, whatever test runner it uses; a PHP project without one gets the plain
 * `.env`.
 */
export const COPY_UNTRACKED = Object.freeze({
  laravel: ['.env', '.env.testing'],
  php: ['.env'],
  node: ['.env', '.env.test'],
  python: ['.env'],
});

/** `link_dirs` per stack: the dependency directories an export links instead of copying. */
const LINK_DIRS = Object.freeze({
  laravel: ['vendor', 'node_modules'],
  php: ['vendor'],
  node: ['node_modules'],
  python: ['.venv'],
  rust: ['target'],
  go: [],
  unknown: ['vendor', 'node_modules'],
});

/** High-risk paths per stack (v1 §2 step 8: migrations, policies, money, auth/middleware, webhooks). */
const HIGH_PATHS = Object.freeze({
  laravel: ['database/migrations/**', 'app/Policies/**', 'app/Http/Middleware/**', '**/*Money*', '**/*Webhook*'],
  php: ['**/*Money*', '**/*Webhook*', '**/Auth/**'],
  node: ['**/migrations/**', '**/auth/**', '**/*money*', '**/*webhook*'],
  python: ['**/migrations/**', '**/auth/**', '**/*money*', '**/*webhook*'],
  rust: ['**/auth/**', '**/*money*'],
  go: ['**/auth/**', '**/*money*'],
  unknown: [],
});

/**
 * The proof profile is decided from the project, not from the gate detector's stack name alone:
 * `detectGates` (B5) names only `php-pest`, so a Laravel app on PHPUnit reaches here as `node`
 * (its Vite `package.json`) or `unknown` — `composer.json` + `artisan` is Laravel either way.
 * @param {string} stack - `detectGates().stack`
 * @param {string} cwd
 * @returns {string} the proof profile: `laravel`, `php`, `node`, `python`, `rust`, `go` or `unknown`.
 */
export function proofProfile(stack, cwd) {
  const composer = existsSync(path.join(cwd, 'composer.json'));
  const php = stack.startsWith('php') || composer;
  if (php && existsSync(path.join(cwd, 'artisan'))) return 'laravel';
  if (stack.startsWith('php')) return 'php';
  // a detected non-PHP stack keeps its word; only an undetected project falls back on composer.json
  if (stack !== 'unknown' && Object.hasOwn(LINK_DIRS, stack)) return stack;
  return composer ? 'php' : 'unknown';
}

/**
 * The provider a level runs on: its own `provider` override (R5), else the default provider.
 * @param {{provider: string, levels: Record<string, any>}} values
 * @param {'L0'|'L1'|'L2'|'L3'} level
 * @returns {string}
 */
export function effectiveProvider(values, level) {
  const own = values.levels?.[level]?.provider;
  return typeof own === 'string' && own.length > 0 ? own : values.provider;
}

/**
 * Consensus review (§4.4) needs three families: reviewer 1 (the effective L2), reviewer 2
 * (`second_provider`) and a judge (the effective L3) that shares a provider with neither
 * (validator rule 7, `judge-family-collision`). With three providers in the catalog the judge's
 * provider is therefore determined once both reviewers are known.
 * @param {{provider: string, levels: Record<string, any>, multimodel: boolean, second_provider: string|null}} values
 * @returns {{judge: string, reviewers: string[], third: string|null} | null} the collision, with
 *   the one provider left for the judge; null when multimodel is off, the second reviewer is not
 *   chosen yet, or the judge already differs from both reviewers.
 */
export function judgeCollision(values) {
  if (values.multimodel !== true || !values.second_provider) return null;
  const reviewers = [effectiveProvider(values, 'L2'), values.second_provider];
  const judge = effectiveProvider(values, 'L3');
  if (!reviewers.includes(judge)) return null;
  return { judge, reviewers, third: PROVIDERS.find((p) => !reviewers.includes(p)) ?? null };
}

/**
 * The judge the wizard proposes: `provider`'s shipped default L3, pinned to that provider.
 * @param {string} provider
 * @returns {{model: string, effort?: string, provider: string}}
 */
export function proposeJudge(provider) {
  const levels = /** @type {Record<string, any>} */ (defaultsForProvider(provider)?.levels ?? {});
  return { ...levels.L3, provider };
}

/**
 * Step 8's proposal for a stack.
 * @param {string} profile
 * @returns {{high: string[], isolation: 'export', link_dirs: string[], copy_untracked: string[]}}
 */
export function proposeProof(profile) {
  return {
    high: [...(HIGH_PATHS[profile] ?? [])],
    isolation: 'export',
    link_dirs: [...(LINK_DIRS[profile] ?? LINK_DIRS.unknown)],
    copy_untracked: [...(COPY_UNTRACKED[profile] ?? [])],
  };
}

/**
 * Variables the Codex CLI sets in the environment of the commands it runs. Source: the strings of
 * the installed Codex CLI 0.155.1 binary on this machine (read-only `grep`, 2026-09-25), which
 * contains `CODEX_SANDBOX` and `CODEX_SANDBOX_NETWORK_DISABLED`. User-config variables
 * (`CODEX_HOME`, `CODEX_API_KEY`) are deliberately not here.
 */
export const CODEX_SESSION_VARS = Object.freeze(['CODEX_SANDBOX', 'CODEX_SANDBOX_NETWORK_DISABLED']);

/**
 * The harness the wizard runs under, from the agent env vars. Codex is recognised only by the
 * variables it sets for its own sessions ({@link CODEX_SESSION_VARS}), never by `CODEX_HOME` or
 * `CODEX_API_KEY`, which people set in their normal shells.
 * @param {NodeJS.ProcessEnv} env
 * @returns {{agent: boolean, harness: string|null}}
 */
export function currentHarness(env) {
  const { source } = detectAgentEnv(env);
  if (source === 'CLAUDECODE' || source === 'CLAUDE_CODE') return { agent: true, harness: 'claude' };
  if (source === 'CURSOR_AGENT') return { agent: true, harness: 'cursor' };
  if (CODEX_SESSION_VARS.some((k) => typeof env[k] === 'string' && env[k].length > 0)) return { agent: true, harness: 'codex' };
  if (source === 'AI_AGENT') return { agent: true, harness: 'unknown' };
  return { agent: false, harness: null };
}

/**
 * Solo is present when `SOLO_MCP_PATH` is set or `~/.claude.json` declares an MCP server whose
 * name contains `solo`.
 * @param {NodeJS.ProcessEnv} env @param {string} home
 * @returns {boolean}
 */
export function soloPresent(env, home) {
  if (typeof env.SOLO_MCP_PATH === 'string' && env.SOLO_MCP_PATH.length > 0) return true;
  try {
    const parsed = JSON.parse(readFileSync(path.join(home, '.claude.json'), 'utf8'));
    const servers = parsed?.mcpServers && typeof parsed.mcpServers === 'object' ? Object.keys(parsed.mcpServers) : [];
    return servers.some((name) => /solo/i.test(name));
  } catch {
    return false;
  }
}

/** @param {string} dir @returns {boolean} `dir` or an ancestor holds `.git`. */
function insideGitRepo(dir) {
  let cur = path.resolve(dir);
  for (;;) {
    if (existsSync(path.join(cur, '.git'))) return true;
    const parent = path.dirname(cur);
    if (parent === cur) return false;
    cur = parent;
  }
}

/** @param {string} name @returns {string} */
function slugify(name) {
  const slug = name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
  return slug.length > 0 ? slug : 'project';
}

/**
 * Project name: `package.json` name, else the last segment of `composer.json`'s name, else the
 * directory name.
 * @param {string} cwd
 * @returns {{name: string, slug: string}}
 */
export function projectIdentity(cwd) {
  for (const [file, pick] of /** @type {Array<[string, (v: any) => unknown]>} */ ([
    ['package.json', (v) => v?.name],
    ['composer.json', (v) => (typeof v?.name === 'string' ? v.name.split('/').pop() : undefined)],
  ])) {
    try {
      const name = pick(JSON.parse(readFileSync(path.join(cwd, file), 'utf8')));
      if (typeof name === 'string' && name.length > 0) {
        const bare = name.startsWith('@') ? name.split('/').pop() : name;
        return { name: bare, slug: slugify(bare) };
      }
    } catch {
      // absent or not JSON: next source
    }
  }
  const base = path.basename(path.resolve(cwd));
  return { name: base, slug: slugify(base) };
}

/**
 * @typedef {object} WizardContext
 * @property {string} cwd
 * @property {string} home
 * @property {NodeJS.ProcessEnv} env
 * @property {ReturnType<typeof detectGates>} detected
 * @property {string} profile
 * @property {boolean} gitRepo
 * @property {string[]} detectedHarnesses
 * @property {{agent: boolean, harness: string|null}} current
 * @property {boolean} solo
 * @property {{name: string, slug: string}} project
 */

/**
 * @param {{cwd: string, home: string, env: NodeJS.ProcessEnv}} opts
 * @returns {Promise<WizardContext>}
 */
export async function gatherContext({ cwd, home, env }) {
  const detected = detectGates(cwd);
  const found = await detectAll({ home, pathEnv: env.PATH ?? '' });
  return {
    cwd,
    home,
    env,
    detected,
    profile: proofProfile(detected.stack, cwd),
    gitRepo: insideGitRepo(cwd),
    detectedHarnesses: found.filter((d) => d.detected).map((d) => d.id),
    current: currentHarness(env),
    solo: soloPresent(env, home),
    project: projectIdentity(cwd),
  };
}

/** @param {unknown} v @returns {any} a deep plain copy */
function clone(v) {
  return v === undefined ? undefined : JSON.parse(JSON.stringify(v));
}

/** @param {unknown} gate @returns {string[]|null} a schema-valid argv (a `{argv}` descriptor keeps its argv). */
function gateArgv(gate) {
  if (Array.isArray(gate)) return [...gate];
  if (gate && typeof gate === 'object' && Array.isArray(/** @type {any} */ (gate).argv)) return [.../** @type {any} */ (gate).argv];
  return null;
}

/**
 * @typedef {object} Answers
 * @property {'current'|'recommended'} tools
 * @property {string[]} yes_tools
 * @property {string[]} harnesses
 * @property {'project'|'global'} scope
 * @property {'symlink'|'copy'} method
 * @property {string} provider
 * @property {Record<string, any>} levels
 * @property {boolean} refresh_models
 * @property {boolean} multimodel
 * @property {string|null} second_provider
 * @property {{mode: 'auto'} | {mode: 'ref', ref: string} | {mode: 'none'}} jev
 * @property {string} engine
 * @property {number|null} solo_project
 * @property {{test: string[]|null, lint: string[]|null, types: string[]|null, format: string[]|null}} gates
 * @property {{high: string[], isolation: string, link_dirs: string[], copy_untracked: string[]}} proof
 * @property {boolean} doctor
 */

/** The answer keys in step order. */
export const ANSWER_KEYS = Object.freeze([
  'tools', 'yes_tools', 'harnesses', 'scope', 'method', 'provider', 'levels', 'refresh_models',
  'multimodel', 'second_provider', 'jev', 'engine', 'solo_project', 'gates', 'proof', 'doctor',
]);

/**
 * @param {WizardContext} ctx
 * @param {Record<string, any>|null} existing - the current `.code-forge.yml`, when there is one.
 * @param {Record<string, any>|null} user - the current user-level config (`~/.code-forge/`).
 * @param {Array<{flag: string, value: any}>} given - parsed answer flags.
 * @returns {{values: Answers, sources: Record<string, 'default'|'existing'|'flag'>}}
 */
export function resolveAnswers(ctx, existing, user, given) {
  /** @type {Record<string, 'default'|'existing'|'flag'>} */
  const sources = {};
  const ex = existing ?? {};
  /** @type {any} */
  const v = {};
  /** @param {string} key @param {unknown} fromExisting @param {() => unknown} fallback */
  const set = (key, fromExisting, fallback) => {
    if (fromExisting !== undefined && fromExisting !== null) {
      v[key] = clone(fromExisting);
      sources[key] = 'existing';
    } else {
      v[key] = fallback();
      sources[key] = 'default';
    }
  };
  const flagsFor = (/** @type {string} */ key) => given.filter((g) => ANSWER_FLAGS[g.flag] === key);
  const lastFlag = (/** @type {string} */ key) => flagsFor(key).at(-1);
  const override = (/** @type {string} */ key, /** @type {unknown} */ value) => {
    v[key] = value;
    sources[key] = 'flag';
  };

  set('tools', user?.tools, () => 'current');
  if (lastFlag('tools')) override('tools', lastFlag('tools').value);
  set('yes_tools', undefined, () => []);
  if (flagsFor('yes_tools').length > 0) override('yes_tools', [...new Set(flagsFor('yes_tools').map((g) => g.value))]);

  set('harnesses', ex.harnesses, () =>
    v.tools === 'current' && ctx.current.harness && ctx.current.harness !== 'unknown' ? [ctx.current.harness] : [...ctx.detectedHarnesses],
  );
  if (lastFlag('harnesses')) override('harnesses', lastFlag('harnesses').value);
  set('scope', undefined, () => (ctx.gitRepo ? 'project' : 'global'));
  if (lastFlag('scope')) override('scope', lastFlag('scope').value);
  set('method', undefined, () => 'symlink');
  if (lastFlag('method')) override('method', lastFlag('method').value);

  set('provider', ex.provider, () => 'anthropic');
  if (lastFlag('provider')) override('provider', lastFlag('provider').value);
  const providerDefaults = defaultsForProvider(v.provider);
  set('levels', ex.provider === v.provider ? ex.levels : undefined, () => clone(providerDefaults.levels));
  if (flagsFor('levels').length > 0) {
    const levels = clone(v.levels);
    for (const g of flagsFor('levels')) levels[g.value.level] = { ...g.value.spec };
    override('levels', levels);
  }
  set('refresh_models', undefined, () => false);
  if (lastFlag('refresh_models')) override('refresh_models', true);

  set('multimodel', ex.review?.multimodel, () => false);
  if (lastFlag('multimodel')) override('multimodel', lastFlag('multimodel').value);
  set('second_provider', ex.review?.second_provider, () => null);
  if (lastFlag('second_provider')) override('second_provider', lastFlag('second_provider').value);

  const exJev = typeof ex.keys?.jev === 'string' ? { mode: 'ref', ref: ex.keys.jev } : existing ? { mode: 'none' } : undefined;
  set('jev', exJev, () => ({ mode: 'auto' }));
  const jevFlag = lastFlag('jev');
  if (jevFlag) {
    if (jevFlag.flag === '--no-jev') override('jev', { mode: 'none' });
    else override('jev', { mode: 'ref', ref: jevFlag.flag === '--jev-env' ? `env:${jevFlag.value}` : jevFlag.value });
  }

  set('engine', ex.engine, () => 'auto');
  if (lastFlag('engine')) override('engine', lastFlag('engine').value);
  set('solo_project', user?.solo?.project_id, () => null);
  if (lastFlag('solo_project')) override('solo_project', lastFlag('solo_project').value);

  const detectedGates = {
    test: gateArgv(ctx.detected.test),
    lint: gateArgv(ctx.detected.lint),
    types: gateArgv(ctx.detected.types),
    format: gateArgv(ctx.detected.format),
  };
  const exGates = ex.gates && typeof ex.gates === 'object' ? ex.gates : undefined;
  set('gates', exGates ? { ...detectedGates, ...exGates } : undefined, () => detectedGates);
  if (flagsFor('gates').length > 0) {
    const gates = clone(v.gates);
    for (const g of flagsFor('gates')) gates[g.value.name] = g.value.argv;
    override('gates', gates);
  }

  const proposal = proposeProof(ctx.profile);
  const exProof = ex.proof && typeof ex.proof === 'object'
    ? {
        high: ex.proof.tiers?.high?.paths ?? proposal.high,
        isolation: ex.proof.isolation ?? proposal.isolation,
        link_dirs: ex.proof.export?.link_dirs ?? proposal.link_dirs,
        copy_untracked: ex.proof.export?.copy_untracked ?? proposal.copy_untracked,
      }
    : undefined;
  set('proof', exProof, () => proposal);
  if (flagsFor('proof').length > 0) {
    const proof = clone(v.proof);
    for (const g of flagsFor('proof')) proof[g.value.key] = g.value.value;
    override('proof', proof);
  }

  set('doctor', undefined, () => true);
  if (lastFlag('doctor')) override('doctor', false);

  return { values: v, sources };
}
