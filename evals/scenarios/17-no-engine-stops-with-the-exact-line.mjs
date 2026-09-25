/**
 * Eval 17 `no-engine-stops-with-the-exact-line` (plan §5.1, §9.3; C6). Under a harness that is
 * neither a subagent host nor has Solo configured (`CODEX_SANDBOX` set, no `SOLO_MCP_PATH`),
 * `code-forge init --no-interaction` prints the exact §5.1 stop text on stderr and reports
 * `engine_stop: true` in its JSON — the wizard never guesses `engine: subprocess` (R2). Reuses
 * B13a's Laravel+Vue fixture builder (a stand-in stack; no real project named, R13). No real
 * `codex`/`claude` on PATH; `runInit` never spawns a provider CLI during this check.
 */
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse as parseYAML } from 'yaml';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, '..', '..');

const STOP_TEXT =
  'code-forge: no Solo and no subagent tool in this harness.\n' +
  '  To run coders as detached CLI processes (power-user mode) add to .code-forge.yml:\n' +
  '    engine: subprocess\n' +
  '  Then re-run. (R2: this engine is never selected automatically.)\n';

export async function run() {
  const { runInit } = await import(path.join(REPO, 'src', 'install', 'wizard', 'run.mjs'));
  const { build: buildLaravelVue } = await import(path.join(REPO, 'test', 'fixtures', 'repos', 'laravel-vue', 'build.mjs'));
  const parent = mkdtempSync(path.join(os.tmpdir(), 'cf-eval17-'));
  try {
    const home = path.join(parent, 'home');
    mkdirSync(home, { recursive: true });
    const cwd = path.join(parent, 'project');
    mkdirSync(cwd, { recursive: true });
    await buildLaravelVue(cwd);
    mkdirSync(path.join(cwd, '.git'), { recursive: true });
    const skillSource = path.join(parent, 'skill');
    mkdirSync(skillSource, { recursive: true });
    writeFileSync(path.join(skillSource, 'SKILL.md'), '---\nname: code-forge\n---\n');

    const env = { HOME: home, PATH: path.join(parent, 'emptybin'), CODE_FORGE_KEY_BACKEND: 'file', CODEX_SANDBOX: 'seatbelt', CODE_FORGE_KEY_JEV: 'sk-FAKE-jev-eval17-0123456789' };
    mkdirSync(env.PATH, { recursive: true });
    const stdoutChunks = [];
    const stderrChunks = [];
    const stdout = { write: (s) => stdoutChunks.push(String(s)) };
    const stderr = { write: (s) => stderrChunks.push(String(s)) };
    const okDoctor = async () => [{ status: 'OK', label: 'config', detail: 'valid' }];

    const code = await runInit(['--no-interaction'], { cwd, home, env, isTTY: false, doctor: okDoctor, stdout, stderr, skillSource });
    assert.equal(code, 0, stderrChunks.join(''));
    assert.equal(stderrChunks.join(''), STOP_TEXT);
    const printed = JSON.parse(stdoutChunks.join(''));
    assert.equal(printed.engine_stop, true);
    assert.notEqual(printed.engine, 'subprocess');

    // The wizard still writes `.code-forge.yml` before the stop check runs (R2: `engine:
    // subprocess` is never chosen automatically, even here) — parsed YAML, never a text/regex
    // search of the file.
    const configText = readFileSync(path.join(cwd, '.code-forge.yml'), 'utf8');
    const config = parseYAML(configText);
    assert.notEqual(config.engine, 'subprocess');
    assert.equal(config.engine, 'auto');

    return { pass: true, detail: 'init --no-interaction with no Solo/subagent tool prints the exact §5.1 stop line, engine_stop:true, and never writes engine: subprocess' };
  } finally {
    rmSync(parent, { recursive: true, force: true });
  }
}
