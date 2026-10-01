/**
 * `code-forge init` — the nine-step setup wizard (plan §2; block B13a).
 *
 * Order: answers (defaults ← existing config ← flags; then questions when interactive) → the
 * "Project settings (detected)" summary of gates and proof (B24; interactive: one "Use these /
 * Customize now / Leave for later" choice, and the eight pre-filled questions on Customize) → step 5
 * key source → build + merge + validate the config (nothing is written when it is invalid or a
 * required answer is missing) → write `.code-forge.yml` only when it changes → tools (step 1),
 * model refresh (3), harness links (2), user config → the §5.1 stop line when the harness has no
 * subagent tool and there is no Solo (6) → doctor `--quick` (9) → output.
 *
 * Output: an agent harness (`CLAUDECODE`, `CLAUDE_CODE`, `CURSOR_AGENT`, `CODEX_SANDBOX*`, `AI_AGENT`) or
 * a non-TTY stdin gets exactly ONE JSON line on stdout — `{ok, wrote, harnesses, engine_stop,
 * doctor, settings, blank, log, log_tail}` on success, `{ok: false, error, wrote, log, log_tail}` on any failure
 * after the flags parsed; a person gets the lines (and `init: <error>` on stderr). Either way the
 * lines go to `~/.code-forge/logs/init-<ts>.log`, on failure too. Every line passes through
 * `redact`; a key typed at the prompt goes to the key store and nowhere else — the config holds its
 * reference (`user`) only.
 */

import { existsSync } from 'node:fs';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse as parseYAML } from 'yaml';
import { DEFAULT_CONFIG_FILENAME, loadConfigFile } from '../../config/load.mjs';
import { refreshFromCliCaches } from '../../config/refresh.mjs';
import { validateConfig } from '../../config/validate.mjs';
import { createDefaultKeyStore, resolveKey } from '../../keys/store.mjs';
import { exec } from '../../util/exec.mjs';
import { redact } from '../../util/redact.mjs';
import { writeAgentJson } from '../agent-env.mjs';
import { commandOnPath } from '../detect.mjs';
import { getHarness, globalSkillPath, projectSkillPath } from '../harnesses.mjs';
import { install, installsPath } from '../link.mjs';
import { currentHarness, effectiveProvider, gatherContext, judgeCollision, proposeJudge, resolveAnswers, SUBAGENT_HARNESSES } from './answers.mjs';
import { changedPaths, generatedConfig, mergeConfig, mergeInto, serializeConfig } from './project-file.mjs';
import { parseInitArgs, UsageError } from './flags.mjs';
import { askAnswers, askJevSource, askProjectSettings, askSettingsChoice, CancelledError, HINTS, withHint } from './steps.mjs';
import { summarizeSettings } from './summary.mjs';

/** §5.1's stop text, printed at setup (not at the first run) when there is no engine to use. */
export const NO_ENGINE_STOP_TEXT = [
  'code-forge: no Solo and no subagent tool in this harness.',
  '  To run coders as detached CLI processes (power-user mode) add to .code-forge.yml:',
  '    engine: subprocess',
  '  Then re-run. (R2: this engine is never selected automatically.)',
].join('\n');

/** Step 1's recommended tools: detect, print the install command, install only after a per-tool yes. */
export const TOOLS = Object.freeze([
  { id: 'solo', command: null, install: null, hint: 'install the Solo app (https://soloterm.com) and add its MCP entry' },
  { id: 'codex', command: 'codex', install: ['npm', 'install', '-g', '@openai/codex'], hint: null },
  { id: 'grok', command: 'grok', install: null, hint: 'install the Grok CLI from its vendor page' },
  { id: 'gemini', command: 'gemini', install: ['npm', 'install', '-g', '@google/gemini-cli'], hint: null },
  { id: 'op', command: 'op', install: ['brew', 'install', '1password-cli'], hint: null },
]);

export const USAGE = [
  'usage: code-forge init [--no-interaction] [--tools recommended|current] [--yes-tool <tool>]…',
  '  [--harness a,b] [-g|-p] [--copy] [--provider P] [--level Ln=model[:effort][@provider]]…',
  '  [--refresh-models] [--multimodel on|off] [--second-provider P]',
  '  [--jev-ref op://… | --jev-env NAME | --no-jev] [--engine auto|solo|harness] [--solo-project N]',
  '  [--gate name=cmd]… [--proof isolation=export|lock | high=a,b | link_dirs=a,b | copy_untracked=a,b]… [--skip-doctor]',
].join('\n');

const __dirname = path.dirname(fileURLToPath(import.meta.url));
/** This package's `skill/` directory (`src/install/wizard` → package root). */
export const DEFAULT_SKILL_SOURCE = path.join(__dirname, '..', '..', '..', 'skill');

/**
 * The user-level config file under `~/.code-forge/` (plan §2: user `tools`, `keys.jev.source`,
 * `solo.project_id`). Spelled in two parts so the schema-coverage sweep does not read the file
 * name as a `config.<path>` access.
 */
export const USER_CONFIG_FILE = ['config', 'yml'].join('.');

/** The environment variable the Jev key is read from when it comes from the environment. */
const JEV_ENV_NAME = 'CODE_FORGE_KEY_JEV';

/** A failure after the flags parsed: carries the exit code; the message names key paths, never values. */
class InitFailure extends Error {
  /** @param {number} code @param {string} message */
  constructor(code, message) {
    super(message);
    this.code = code;
  }
}

/** @typedef {{write: (s: string) => unknown}} Out */

/**
 * @typedef {object} InitDeps
 * @property {string} [cwd]
 * @property {string} [home]
 * @property {NodeJS.ProcessEnv} [env]
 * @property {Out} [stdout]
 * @property {Out} [stderr]
 * @property {boolean} [isTTY] - whether stdin is a terminal (default `process.stdin.isTTY`).
 * @property {import('./steps.mjs').Ui} [ui] - default `@clack/prompts`.
 * @property {() => Promise<any>} [getStore] - the B2 key store (default: the production chain).
 * @property {(opts: {cwd: string, env: NodeJS.ProcessEnv}) => Promise<Array<{status: string, label: string, detail: string}>>} [doctor]
 * @property {(argv: string[]) => Promise<{result: string}>} [installTool]
 * @property {string} [skillSource]
 * @property {() => Date} [now]
 */

/** @param {string} file @returns {Promise<Record<string, any>|null>} a missing or unreadable user config reads as null */
async function readUserConfig(file) {
  try {
    const parsed = parseYAML(await readFile(file, 'utf8'), { prettyErrors: false });
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

/** @param {string} file @param {string} text */
async function writeText(file, text) {
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, text);
}

/**
 * @param {{cwd: string, env: NodeJS.ProcessEnv}} opts
 * @returns {Promise<Array<{status: string, label: string, detail: string}>>}
 */
async function quickDoctor({ cwd, env }) {
  const { runDoctor } = await import('../../doctor/index.mjs');
  return runDoctor({ cwd, quick: true }, { env });
}

/**
 * @param {string[]} args
 * @param {InitDeps} [deps]
 * @returns {Promise<number>}
 */
export async function runInit(args, deps = {}) {
  const env = deps.env ?? process.env;
  const cwd = path.resolve(deps.cwd ?? process.cwd());
  const home = deps.home ?? env.HOME ?? '';
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  const now = (deps.now ?? (() => new Date()))();
  const lines = /** @type {string[]} */ ([]);
  const say = (/** @type {string} */ line) => lines.push(redact(line));

  let parsed;
  try {
    parsed = parseInitArgs(args);
  } catch (err) {
    if (!(err instanceof UsageError)) throw err;
    stderr.write(redact(`init: ${err.message}\n${USAGE}\n`));
    return 2;
  }

  // Everything below the flags reaches stdout through `finish`, once: the log is written on
  // every exit, and agent mode always gets exactly one JSON line.
  const jsonMode = currentHarness(env).agent || !(deps.isTTY ?? process.stdin.isTTY === true);
  const interactive = !parsed.noInteraction && !jsonMode;
  const cfDir = path.join(home, '.code-forge');
  const logFile = home === '' ? null : path.join(cfDir, 'logs', `init-${now.toISOString().replace(/[:.]/g, '-')}.log`);
  /** @type {string[]} */
  const wrote = [];
  /** @type {string[]} */
  const linked = [];
  let engineStop = false;
  /** @type {{ok: boolean, counts: Record<string, number>} | null} */
  let doctor = null;
  /** @type {import('./summary.mjs').SettingsSummary | null} */
  let summary = null;
  // lines already on stdout (the settings summary): `finish` prints only the rest
  let printed = 0;

  /**
   * The one exit: writes the log, then either the success shape or `{ok: false, error, wrote, …}`.
   * @param {number} code @param {string|null} error
   * @returns {Promise<number>}
   */
  const finish = async (code, error) => {
    if (error !== null) {
      say(`error: ${error}`);
      stderr.write(redact(`init: ${error}\n`));
    }
    let log = logFile;
    if (logFile !== null) {
      try {
        await writeText(logFile, `${lines.join('\n')}\n`);
      } catch {
        log = null;
      }
    }
    const tail = lines.slice(-10);
    if (jsonMode) {
      const body = error === null
        ? { ok: code === 0, wrote, harnesses: linked, engine_stop: engineStop, doctor, settings: summary?.settings ?? null, blank: summary?.blank ?? [], log, log_tail: tail }
        : { ok: false, error, wrote, log, log_tail: tail };
      writeAgentJson(JSON.parse(redact(JSON.stringify(body))), { stdout });
    } else if (error === null) {
      const rest = lines.slice(printed);
      stdout.write(`${rest.length > 0 ? `${rest.join('\n')}\n` : ''}log: ${log}\n`);
    }
    return code;
  };

  /** @type {import('./steps.mjs').Ui|undefined} */
  let ui;
  /** @type {any} */
  let store;
  const getStore = async () => (store ??= await (deps.getStore ?? (() => createDefaultKeyStore(env)))());
  try {
    if (home === '') throw new InitFailure(1, 'HOME is not set');
    const ctx = await gatherContext({ cwd, home, env });

    const configFile = path.join(cwd, DEFAULT_CONFIG_FILENAME);
    /** @type {Record<string, any>|null} */
    let existing = null;
    if (existsSync(configFile)) {
      const loaded = await loadConfigFile(configFile);
      if (!loaded.ok) throw new InitFailure(1, `${DEFAULT_CONFIG_FILENAME} exists but cannot be loaded (${loaded.error}); fix or move it, then re-run`);
      existing = loaded.config;
    }
    const userFile = path.join(cfDir, USER_CONFIG_FILE);
    const userCfg = await readUserConfig(userFile);

    const { values, sources, origins } = resolveAnswers(ctx, existing, userCfg, parsed.given);
    if (interactive) {
      ui = deps.ui ?? /** @type {any} */ (await import('@clack/prompts'));
      await askAnswers(values, sources, ctx, ui);
    }

    // Gates and proof are never asked (B24): the summary says what was found and what is blank.
    // A person sees it now, before anything is written; agent mode gets it in the JSON line.
    summary = summarizeSettings(values, origins, ctx);
    /** @param {string[]} shown - logged, and printed at once for a person */
    const showNow = (shown) => {
      for (const line of shown) say(line);
      if (jsonMode) return;
      stdout.write(`${lines.slice(printed).join('\n')}\n`);
      printed = lines.length;
    };
    showNow(summary.lines);
    if (interactive) {
      const choice = await askSettingsChoice(ui);
      if (choice === 'customize') {
        const changedKeys = await askProjectSettings(values, origins, ui);
        say(`settings: customized (${changedKeys.length > 0 ? changedKeys.join(', ') : 'no change'})`);
      } else if (choice === 'later') {
        const keys = summary.blank.length > 0 ? summary.blank : Object.keys(summary.settings);
        showNow([`settings: edit .code-forge.yml later: ${keys.join(', ')} — then run \`code-forge validate\``]);
      }
    }

    // Step 5 — where the Jev key comes from. Only a reference is ever kept: `env:NAME` when the
    // environment holds it, else `user` (the key store holds it, whatever backend the store
    // reports — keychain, file, a cached 1Password read…); the backend name is recorded as the
    // user-level `keys.jev.source`.
    /** @type {string[]} */
    const missing = [];
    let jevRef = null;
    let jevSource = 'none';
    if (values.jev.mode === 'ref') {
      jevRef = values.jev.ref;
      jevSource = jevRef.startsWith('env:') ? 'env' : jevRef.startsWith('op://') ? 'op' : 'keychain';
    } else if (values.jev.mode === 'auto') {
      const res = await resolveKey('jev', { store: await getStore(), env });
      if (res.value !== null && typeof res.source === 'string') {
        jevRef = res.source === 'env' ? `env:${JEV_ENV_NAME}` : 'user';
        jevSource = res.source;
      } else if (interactive) {
        ({ ref: jevRef, source: jevSource } = await askJevSource(ui, getStore));
      } else {
        missing.push('keys.jev (pass --jev-ref, --jev-env or --no-jev)');
      }
    }
    if (values.multimodel && !values.second_provider) missing.push('review.second_provider (pass --second-provider)');
    if (missing.length > 0) throw new InitFailure(2, `missing required answers, nothing written:\n${missing.map((m) => `  ${m}`).join('\n')}`);
    say(`keys: jev from ${jevSource}`);

    // Step 4 — consensus needs three families (§4.4; validator rules 6 and 7). A flag-supplied or
    // kept second provider equal to the effective L2 provider is refused; a judge that shares a
    // provider with a reviewer moves to the third provider's default L3 unless `--level L3` pinned it.
    if (values.multimodel) {
      const reviewer1 = effectiveProvider(values, 'L2');
      if (values.second_provider === reviewer1) {
        throw new InitFailure(2, `review.second_provider equals the effective L2 provider (${reviewer1}) — multimodel needs two providers; pass --second-provider with another one. Nothing written.`);
      }
      const collision = judgeCollision(values);
      if (collision) {
        const l3Flagged = parsed.given.some((g) => g.flag === '--level' && /** @type {any} */ (g.value).level === 'L3');
        if (l3Flagged || collision.third === null) {
          throw new InitFailure(2, `--level L3 pins the judge to ${collision.judge}, a reviewer's provider — the consensus judge must come from a third provider${collision.third ? ` (${collision.third})` : ''}; change or drop --level L3. Nothing written.`);
        }
        values.levels.L3 = proposeJudge(collision.third);
        say(`levels.L3: judge proposed from ${collision.third} (consensus needs a third provider; reviewers ${collision.reviewers.join(', ')})`);
      }
    }

    const merged = mergeConfig(existing, generatedConfig(values, ctx, jevRef), { dropJev: jevRef === null });
    const checked = validateConfig(merged);
    if (!checked.valid) {
      throw new InitFailure(1, `the resulting config is invalid, nothing written:\n${checked.errors.map((e) => `  ${e.rule}: ${e.message}`).join('\n')}`);
    }
    for (const w of checked.warnings) say(`config warning: ${w.rule}`);

    const changed = existing ? changedPaths(existing, merged) : [];
    if (existing && changed.length === 0) {
      say(`${DEFAULT_CONFIG_FILENAME}: unchanged`);
    } else {
      if (existing) say(`${DEFAULT_CONFIG_FILENAME}: changes ${changed.join(', ')}`);
      const go = existing && interactive ? await ui.confirm({ message: withHint(`Apply ${changed.length} change(s) to ${DEFAULT_CONFIG_FILENAME}?`, HINTS.apply), initialValue: true }) : true;
      if (ui?.isCancel(go) || go !== true) throw new CancelledError('cancelled — nothing written');
      await writeText(configFile, serializeConfig(merged));
      wrote.push(configFile);
      say(`${DEFAULT_CONFIG_FILENAME}: written`);
    }

    // From here on the config is on disk: a throw below still reaches `finish` with `wrote` filled.

    // Step 1 — tools.
    if (values.tools === 'recommended') {
      for (const tool of TOOLS) {
        const present = tool.command === null ? ctx.solo : await commandOnPath(tool.command, { pathEnv: env.PATH ?? '' });
        if (present) {
          say(`tool ${tool.id}: present`);
        } else if (tool.install && values.yes_tools.includes(tool.id)) {
          const res = await (deps.installTool ?? ((argv) => exec(argv, { timeoutMs: 600_000, env })))(tool.install);
          say(`tool ${tool.id}: ${res.result === 'ok' ? 'installed' : 'install failed'} (${tool.install.join(' ')})`);
        } else {
          say(`tool ${tool.id}: missing — ${tool.install ? `install with: ${tool.install.join(' ')}` : tool.hint} (doctor WARNs)`);
        }
      }
    }

    // Step 3 — optional model-id refresh from the CLI caches (C15).
    if (values.refresh_models) {
      const res = await refreshFromCliCaches({ home });
      say(`models: ${res.sourcesRead.length} CLI cache(s) read`);
    }

    // Step 2 — harness links.
    const source = deps.skillSource ?? DEFAULT_SKILL_SOURCE;
    if (values.harnesses.length > 0 && !existsSync(source)) {
      say(`links: skill source missing (${source}); no harness linked`);
    } else {
      for (const id of values.harnesses) {
        const harness = getHarness(id);
        const target = values.scope === 'project' ? projectSkillPath(harness, cwd) : globalSkillPath(harness, home);
        await install({ installsFile: installsPath(home), harness: id, scope: values.scope, method: values.method, source, target });
        linked.push(id);
        wrote.push(target);
        say(`link ${id}: ${values.method} ${target}`);
      }
    }

    // User-level answers: tools, where the Jev key lives, the Solo project.
    /** @type {Record<string, any>} */
    const userOver = { tools: values.tools, keys: { jev: { source: jevSource } } };
    if (values.solo_project !== null) userOver.solo = { project_id: values.solo_project };
    const userNext = mergeInto(userCfg ?? {}, userOver);
    if (changedPaths(userCfg ?? {}, userNext).length > 0) {
      await writeText(userFile, serializeConfig(userNext));
      wrote.push(userFile);
    }

    // Step 6 — no engine at all: say so now, at setup (§5.1).
    if (ctx.current.agent && !SUBAGENT_HARNESSES.includes(ctx.current.harness) && !ctx.solo && (merged.engine === 'auto' || merged.engine === 'harness')) {
      engineStop = true;
      stderr.write(`${NO_ENGINE_STOP_TEXT}\n`);
      say('engine: no Solo and no subagent tool in this harness (see the stop text)');
    } else {
      say(`engine: ${merged.engine}`);
    }

    // Step 9 — doctor.
    if (values.doctor) {
      const rows = await (deps.doctor ?? quickDoctor)({ cwd, env });
      const counts = { OK: 0, WARN: 0, FAIL: 0, INFO: 0 };
      for (const r of rows) {
        counts[r.status] = (counts[r.status] ?? 0) + 1;
        say(`doctor ${r.status} ${r.label}: ${r.detail}`);
      }
      doctor = { ok: counts.FAIL === 0, counts };
    }

    return await finish(doctor === null || doctor.ok ? 0 : 1, null);
  } catch (err) {
    if (err instanceof InitFailure) return finish(err.code, err.message);
    if (err instanceof UsageError) return finish(2, err.message);
    if (err instanceof CancelledError) return finish(1, err.message);
    // an unexpected throw (an installer, a link, the doctor…): the log keeps the stack, redacted
    const reason = err instanceof Error ? err.message : String(err);
    if (err instanceof Error && err.stack) say(redact(err.stack));
    return finish(1, `unexpected error: ${reason}`);
  }
}
