import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { mock, test } from 'node:test';
import * as realRun from '../../src/state/run.mjs';

let stateReads = 0;
mock.module('../../src/state/run.mjs', { namedExports: {
  ...realRun,
  readRun: async () => { stateReads += 1; throw new Error('help must not access run state'); },
} });
const { runBlock } = await import('../../src/cli/block.mjs');

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const BIN = path.join(REPO, 'bin/code-forge.mjs');

// Audit effects in the actual CLI process, including attempts its best-effort paths swallow.
// Module/package reads are allowed; workspace, HOME and TMPDIR reads are not help work.
const HELP_AUDIT = 'data:text/javascript,' + encodeURIComponent([
  'import fs from "node:fs";',
  'import fsp from "node:fs/promises";',
  'import cp from "node:child_process";',
  'import net from "node:net";',
  'import readline from "node:readline";',
  'import { syncBuiltinESMExports } from "node:module";',
  'const effects = [];',
  'const deny = (name) => { effects.push(name); throw new Error("help attempted " + name); };',
  'process.on("exit", () => { if (effects.length) { process.stderr.write(effects.join("\\n")); process.exitCode = 1; } });',
  'for (const name of ["spawn", "spawnSync", "exec", "execSync", "execFile", "execFileSync", "fork"]) cp[name] = () => deny(name);',
  'net.Socket.prototype.connect = () => deny("network");',
  'globalThis.fetch = () => deny("fetch");',
  'readline.createInterface = () => deny("stdin/prompt");',
  'for (const api of [fs, fsp]) {',
  '  for (const name of ["writeFile", "writeFileSync", "appendFile", "appendFileSync", "mkdir", "mkdirSync", "mkdtemp", "mkdtempSync", "rename", "renameSync", "rm", "rmSync", "unlink", "unlinkSync", "createWriteStream"]) {',
  '    if (typeof api[name] === "function") api[name] = () => deny(name);',
  '  }',
  '  for (const name of ["readFile", "readFileSync", "open", "openSync", "stat", "statSync", "lstat", "lstatSync", "access", "accessSync", "existsSync", "readdir", "readdirSync"]) {',
  '    const original = api[name]; if (typeof original !== "function") continue;',
  '    api[name] = (...args) => {',
  '      const file = String(args[0]);',
  '      if ([process.env.HOME, process.env.TMPDIR, process.cwd()].some((dir) => file === dir || file.startsWith(dir + "/"))) deny(name + ": " + file);',
  '      return original.apply(api, args);',
  '    };',
  '  }',
  '}',
  'syncBuiltinESMExports();',
].join('\n'));

function isolatedCli() {
  const root = mkdtempSync(path.join(tmpdir(), 'all-verb-help-'));
  const home = path.join(root, 'home');
  const cwd = path.join(root, 'cwd');
  const tmp = path.join(root, 'tmp');
  for (const dir of [home, cwd, tmp]) mkdirSync(dir);
  const env = {
    PATH: path.dirname(process.execPath), HOME: home, TMPDIR: tmp,
    CODE_FORGE_NATIVE: '0', NO_COLOR: '1', LANG: 'C', LC_ALL: 'C', TZ: 'UTC',
  };
  return {
    run: (args) => {
      const discovering = args.length === 1 && args[0] === 'help';
      const response = spawnSync(process.execPath, [...(discovering ? [] : ['--import', HELP_AUDIT]), BIN, ...args], {
        cwd, env: { ...env, CODE_FORGE_NO_ERROR_LOG: discovering ? '1' : '0' },
        encoding: 'utf8', timeout: 15000,
      });
      assert.equal(response.error, undefined, args.join(' '));
      assert.equal(response.status, 0, `${args.join(' ')}: ${response.stderr}`);
      assert.equal(response.stderr, '', `${args.join(' ')} must print only help`);
      for (const dir of [home, cwd, tmp]) {
        assert.deepEqual(readdirSync(dir, { recursive: true }), [], `${args.join(' ')} must leave ${path.basename(dir)} empty`);
      }
      return response.stdout;
    },
    close: () => rmSync(root, { recursive: true, force: true }),
  };
}

test('every discovered verb prints its usage for either leading help flag without effects', async () => {
  const cli = isolatedCli();
  try {
    const verbs = [...cli.run(['help']).matchAll(/^ {2}([a-z][a-z0-9-]*)$/gm)].map((m) => m[1]);
    assert.ok(verbs.length > 0);
    for (const verb of verbs) {
      for (const flag of ['--help', '-h']) {
        assert.match(cli.run([verb, flag]), new RegExp(`^usage: code-forge ${verb}(?: |\\n|$)`, 'm'));
      }
    }
  } finally { cli.close(); }
});

test('every exported subcommand prints only its own help without effects', async () => {
  const cli = isolatedCli();
  try {
    const verbs = [...cli.run(['help']).matchAll(/^ {2}([a-z][a-z0-9-]*)$/gm)].map((m) => m[1]);
    for (const verb of verbs) {
      const mod = await import(pathToFileURL(path.join(REPO, 'src/cli', `${verb}.mjs`)).href);
      for (const [sub, usage] of Object.entries(mod.SUB_USAGE ?? {})) {
        for (const flag of ['--help', '-h']) {
          assert.equal(cli.run([verb, sub, flag]), usage, `${verb} ${sub} ${flag}`);
        }
      }
    }
  } finally { cli.close(); }
});

test('command reference generation refuses missing usage or summary without overwriting the page', () => {
  const root = mkdtempSync(path.join(tmpdir(), 'missing-verb-help-'));
  try {
    for (const dir of ['scripts', 'bin', 'src/cli', 'docs/reference', 'home', 'tmp']) {
      mkdirSync(path.join(root, dir), { recursive: true });
    }
    const script = path.join(root, 'scripts/gen-commands-doc.mjs');
    const doc = path.join(root, 'docs/reference/commands.md');
    writeFileSync(script, readFileSync(path.join(REPO, 'scripts/gen-commands-doc.mjs')));
    writeFileSync(path.join(root, 'bin/code-forge.mjs'), 'export async function listVerbs() { return ["example"]; }\n');
    writeFileSync(doc, 'existing page\n');
    for (const missing of ['USAGE', 'SUMMARY']) {
      writeFileSync(path.join(root, 'src/cli/example.mjs'), missing === 'USAGE'
        ? 'export const SUMMARY = "Print an example.";\n'
        : 'export const USAGE = "usage: code-forge example\\n";\n');
      const response = spawnSync(process.execPath, [script], {
        cwd: root, encoding: 'utf8', timeout: 15000,
        env: { PATH: path.dirname(process.execPath), HOME: path.join(root, 'home'), TMPDIR: path.join(root, 'tmp') },
      });
      assert.equal(response.status, 1, missing);
      assert.match(response.stderr, new RegExp(`Missing CLI help metadata: example\\.${missing}`));
      assert.equal(response.stdout, '');
      assert.equal(readFileSync(doc, 'utf8'), 'existing page\n');
    }
  } finally { rmSync(root, { recursive: true, force: true }); }
});

const VERBS = ['open', 'attempt', 'rebase', 'claim', 'close', 'stop', 'waive', 'reopen', 'accept-hit', 'unwaive', 'ack-waivers', 'human-pass', 'amend-acceptance'];

async function help(args) {
  const dir = mkdtempSync(path.join(tmpdir(), 'verb-help-'));
  const previousHome = process.env.HOME;
  const previousCwd = process.cwd();
  try {
    process.env.HOME = dir;
    process.chdir(dir);
    let out = ''; let err = ''; let effects = 0;
    const code = await runBlock(args, {
      stdout: { write: (s) => { out += s; } },
      stderr: { write: (s) => { err += s; } },
      isTTY: true,
      ui: { confirm: async () => { effects += 1; return true; }, isCancel: () => false },
      removeBlockTree: async () => { effects += 1; throw new Error('help must not remove a worktree'); },
    });
    assert.deepEqual(readdirSync(dir, { recursive: true }), [], `${args.join(' ')} must leave HOME and cwd empty`);
    return { code, out, err, effects };
  } finally {
    process.chdir(previousCwd);
    if (previousHome === undefined) delete process.env.HOME;
    else process.env.HOME = previousHome;
    rmSync(dir, { recursive: true, force: true });
  }
}

function result(verb, flag, response) {
  return {
    verb, flag, code: response.code, err: response.err, effects: response.effects,
    usage: response.out.split('\n')[0].startsWith(`usage: code-forge block ${verb} `),
    usageCount: (response.out.match(/^usage:/gm) ?? []).length,
    helpFlags: response.out.includes('--help') && response.out.includes('-h'),
    jsonFlag: response.out.includes('--json'),
  };
}

test('every block verb accepts both help flags, prints its own usage and accesses no state', async () => {
  stateReads = 0;
  const actual = []; const expected = [];
  for (const verb of VERBS) {
    for (const flag of ['--help', '-h']) {
      actual.push(result(verb, flag, await help([verb, flag])));
      expected.push({ verb, flag, code: 0, err: '', effects: 0, usage: true, usageCount: 1, helpFlags: true, jsonFlag: true });
    }
  }
  assert.deepEqual(actual, expected, 'all 26 verb/flag pairs must print exactly one matching usage and exit 0');
  assert.equal(stateReads, 0, 'all 26 help requests must perform zero run reads');
});

test('help takes precedence over supplied operation arguments, JSON output and missing flag values', async () => {
  stateReads = 0;
  const actual = []; const expected = [];
  for (const verb of VERBS) {
    for (const flag of ['--help', '-h']) {
      const response = await help([verb, 'B1', '--run', 'r-help', '--json', flag, '--reason']);
      actual.push(result(verb, flag, response));
      expected.push({ verb, flag, code: 0, err: '', effects: 0, usage: true, usageCount: 1, helpFlags: true, jsonFlag: true });
    }
  }
  assert.deepEqual(actual, expected, 'all 26 argument-bearing requests must bypass validation, prompts and operations');
  assert.equal(stateReads, 0, 'argument-bearing help must perform zero run reads');
});
