/**
 * The recommended tools (block B26): ONE table shared by `code-forge tools`, `init` step 1 and
 * `doctor`. Each row says how to detect the tool and how to install it on this OS:
 *  - an npm package installs with `npm install -g <pkg>` when `npm` is on PATH, else it is manual;
 *  - a Homebrew formula/cask installs with `brew install …` on macOS when `brew` is on PATH
 *    ("install Homebrew first" when it is not), and is manual (a vendor link) on other systems;
 *  - Solo is always manual (a desktop app, then its MCP entry).
 * Detection never spawns a process: `commandOnPath` scans PATH, and Solo's app bundle is a plain
 * existence check. Installs are argv arrays only — the caller runs them through B0 `exec`.
 */

import { existsSync } from 'node:fs';
import path from 'node:path';
import { commandOnPath } from './detect.mjs';

/**
 * @typedef {object} ToolRow
 * @property {string} id
 * @property {string} command - the binary looked up on PATH (and asked for `--version`).
 * @property {string|null} npm - the npm package, installed globally.
 * @property {ReadonlyArray<string>|null} brew - the args after `brew install` (macOS only).
 * @property {string|null} manual - the link (or text) when there is no automatic install.
 * @property {string|null} app - a macOS app bundle name that also counts as installed.
 * @property {string|null} note - printed next to the tool when it is installed.
 */

/** @type {ReadonlyArray<Readonly<ToolRow>>} */
export const TOOLS = Object.freeze([
  Object.freeze({ id: 'claude', command: 'claude', npm: '@anthropic-ai/claude-code', brew: null, manual: null, app: null, note: null }),
  Object.freeze({ id: 'codex', command: 'codex', npm: '@openai/codex', brew: null, manual: null, app: null, note: null }),
  Object.freeze({ id: 'gemini', command: 'gemini', npm: '@google/gemini-cli', brew: null, manual: null, app: null, note: null }),
  Object.freeze({ id: 'grok', command: 'grok', npm: null, brew: Object.freeze(['--cask', 'grok-build']), manual: 'https://x.ai/build', app: null, note: null }),
  Object.freeze({
    id: 'op',
    command: 'op',
    npm: null,
    brew: Object.freeze(['1password-cli']),
    manual: 'https://developer.1password.com/docs/cli/get-started/',
    app: null,
    note: 'turn on 1Password app → Settings → Developer → "Integrate with 1Password CLI"',
  }),
  Object.freeze({
    id: 'solo',
    command: 'solo',
    npm: null,
    brew: null,
    manual: 'https://soloterm.com (desktop app, then add its MCP entry)',
    app: 'Solo.app',
    note: null,
  }),
]);

/** Every tool id, in table order. */
export const TOOL_IDS = Object.freeze(TOOLS.map((t) => t.id));

/** The hint when a brew install is due but `brew` is not on PATH. */
export const NO_BREW_HINT = 'install Homebrew first: https://brew.sh';

/** The version timeout (`<cmd> --version`). */
export const VERSION_TIMEOUT_MS = 10_000;

/**
 * @typedef {object} ToolEnv
 * @property {string} [pathEnv] - a PATH-shaped string (default `process.env.PATH`).
 * @property {NodeJS.Platform} [platform] - default `process.platform`.
 * @property {string} [home] - for `~/Applications` (default `process.env.HOME`).
 * @property {(p: string) => boolean} [exists] - default `existsSync` (tests: a fake).
 */

/** @param {string} id @returns {Readonly<ToolRow>|undefined} */
export function toolById(id) {
  return TOOLS.find((t) => t.id === id);
}

/**
 * Whether a tool is installed: its command on PATH, or (macOS) its app bundle in `/Applications`
 * or `~/Applications`.
 * @param {Readonly<ToolRow>} tool @param {ToolEnv} [opts]
 * @returns {Promise<boolean>}
 */
export async function detectTool(tool, opts = {}) {
  const platform = opts.platform ?? process.platform;
  if (await commandOnPath(tool.command, { pathEnv: opts.pathEnv ?? process.env.PATH ?? '', platform })) return true;
  if (tool.app === null || platform !== 'darwin') return false;
  const exists = opts.exists ?? existsSync;
  const home = opts.home ?? process.env.HOME ?? '';
  const places = [path.join('/Applications', tool.app), ...(home ? [path.join(home, 'Applications', tool.app)] : [])];
  return places.some((p) => exists(p));
}

/**
 * @typedef {{kind: 'run', argv: string[]} | {kind: 'manual', hint: string}} InstallPlan
 */

/**
 * How to install a tool on this machine: an argv to run, or a manual hint.
 * @param {Readonly<ToolRow>} tool @param {ToolEnv} [opts]
 * @returns {Promise<InstallPlan>}
 */
export async function installPlan(tool, opts = {}) {
  const platform = opts.platform ?? process.platform;
  const onPath = (/** @type {string} */ cmd) => commandOnPath(cmd, { pathEnv: opts.pathEnv ?? process.env.PATH ?? '', platform });
  if (tool.npm !== null) {
    const argv = ['npm', 'install', '-g', tool.npm];
    // Windows: npm is `npm.cmd`, which only a shell runs — never spawned here, the user runs it
    if (platform === 'win32') return { kind: 'manual', hint: `run: ${argv.join(' ')}` };
    return (await onPath('npm')) ? { kind: 'run', argv } : { kind: 'manual', hint: `install Node.js/npm first, then: ${argv.join(' ')}` };
  }
  if (tool.brew !== null && platform === 'darwin') {
    return (await onPath('brew')) ? { kind: 'run', argv: ['brew', 'install', ...tool.brew] } : { kind: 'manual', hint: NO_BREW_HINT };
  }
  return { kind: 'manual', hint: tool.manual ?? 'no install known' };
}

/**
 * `<cmd> --version` with a 10 s timeout: the first non-empty line of stdout (else of stderr), cut
 * to 120 chars. Any failure (missing, non-zero, timeout, empty) → null.
 * @param {Readonly<ToolRow>} tool
 * @param {{exec: typeof import('../util/exec.mjs').exec, env?: NodeJS.ProcessEnv}} deps
 * @returns {Promise<string|null>}
 */
export async function toolVersion(tool, { exec, env }) {
  try {
    const res = await exec([tool.command, '--version'], { timeoutMs: VERSION_TIMEOUT_MS, ...(env ? { env } : {}) });
    if (res.result !== 'ok' || res.timedOut) return null;
    const firstLine = (/** @type {unknown} */ text) => String(text ?? '').split('\n').map((l) => l.trim()).find((l) => l.length > 0);
    const first = firstLine(res.stdout) ?? firstLine(res.stderr);
    return first ? first.slice(0, 120) : null;
  } catch {
    return null;
  }
}

/**
 * Why an install failed, as a fixed phrase — never the child's error text.
 * @param {{code?: number|null, signal?: string|null, timedOut?: boolean}} res
 * @returns {string} `timed out`, `signal S`, `exit n` or `did not start`
 */
export function failureReason(res) {
  if (res.timedOut) return 'timed out';
  if (typeof res.signal === 'string' && res.signal.length > 0) return `signal ${res.signal}`;
  if (typeof res.code === 'number') return `exit ${res.code}`;
  return 'did not start';
}

/**
 * Never rejects: a tool whose check throws counts as missing.
 * @param {ToolEnv} [opts]
 * @returns {Promise<string[]>} the ids of the tools that are not installed, in table order.
 */
export async function missingTools(opts = {}) {
  const out = [];
  for (const tool of TOOLS) {
    let present = false;
    try {
      present = await detectTool(tool, opts);
    } catch {
      present = false;
    }
    if (!present) out.push(tool.id);
  }
  return out;
}
