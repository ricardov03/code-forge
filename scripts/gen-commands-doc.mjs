#!/usr/bin/env node
/** Generate the command reference from the CLI's exported help metadata. */
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const OUT = path.join(REPO, 'docs/reference/commands.md');
// One-line summaries from the Go help table (`code-forge help --json`); a Go test keeps the file current.
const SUMMARIES = path.join(REPO, 'docs/reference/command-summaries.json');

/** @returns {Map<string, string>} command name ("block open") to its one-line summary */
export function readCommandSummaries(file = SUMMARIES) {
  /** @type {{name: string, summary: string}[]} */
  const rows = JSON.parse(readFileSync(file, 'utf8'));
  return new Map(rows.map((row) => [row.name, row.summary]));
}

/** @typedef {{verb: string, purpose: string, usage: string, subcommands: {name: string, usage: string}[]}} Command */

/** Import metadata in an isolated process without invoking any command handlers.
 * @returns {{commands: Command[], missing: string[]}}
 */
export function collectCommandHelp() {
  const root = mkdtempSync(path.join(os.tmpdir(), 'code-forge-command-help-'));
  const home = path.join(root, 'home');
  const tmp = path.join(root, 'tmp');
  mkdirSync(home);
  mkdirSync(tmp);
  const env = {
    PATH: path.dirname(process.execPath), HOME: home, TMPDIR: tmp,
    CODE_FORGE_NATIVE: '0', NO_COLOR: '1',
    LANG: 'C', LC_ALL: 'C', TZ: 'UTC', COLUMNS: '80',
  };
  const router = pathToFileURL(path.join(REPO, 'bin/code-forge.mjs')).href;
  const cli = pathToFileURL(path.join(REPO, 'src/cli/')).href;
  const source = [
    'const { listVerbs } = await import(' + JSON.stringify(router) + ');',
    'const commands = []; const missing = [];',
    'for (const verb of await listVerbs()) {',
    '  const mod = await import(new URL(verb + ".mjs", ' + JSON.stringify(cli) + '));',
    '  for (const field of ["USAGE", "SUMMARY"]) {',
    '    if (typeof mod[field] !== "string" || !mod[field].trim()) missing.push(verb + "." + field);',
    '  }',
    '  commands.push({ verb, purpose: mod.SUMMARY, usage: mod.USAGE,',
    '    subcommands: Object.entries(mod.SUB_USAGE ?? {}).map(([name, usage]) => ({ name, usage })) });',
    '}',
    'process.stdout.write(JSON.stringify({ commands, missing }));',
  ].join('\n');
  try {
    const result = spawnSync(process.execPath, ['--input-type=module', '-e', source], {
      cwd: root, env, encoding: 'utf8', timeout: 15000, maxBuffer: 4 * 1024 * 1024,
    });
    if (result.status !== 0) throw new Error(result.stderr || 'could not collect CLI help metadata');
    return JSON.parse(result.stdout);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

/** Pure renderer: the same collected help always produces the same Markdown.
 * @param {Command[]} commands @param {Map<string, string>} [summaries] @returns {string}
 */
export function renderCommandsDoc(commands, summaries = readCommandSummaries()) {
  const lines = [
    '# Command reference', '',
    'This page lists the CLI commands and their usage. It is generated from the CLI. ' +
      'Regenerate it with `node scripts/gen-commands-doc.mjs`.', '',
    ...commands.map(({ verb }) => '- [' + verb + '](#' + verb + ')'), '',
  ];
  /** @param {string} usage */
  const addUsage = (usage) => {
    lines.push('```text', usage.endsWith('\n') ? usage.slice(0, -1) : usage, '```', '');
  };
  for (const command of commands) {
    if (!command.usage?.trim() || !command.purpose?.trim()) {
      throw new Error('missing USAGE or SUMMARY: ' + command.verb);
    }
    lines.push('## ' + command.verb, '', summaries.get(command.verb) ?? command.purpose, '');
    addUsage(command.usage);
    for (const sub of command.subcommands) {
      const name = command.verb + ' ' + sub.name;
      lines.push('### ' + name, '');
      if (summaries.has(name)) lines.push(/** @type {string} */ (summaries.get(name)), '');
      addUsage(sub.usage);
    }
  }
  return lines.join('\n');
}

function main() {
  const args = process.argv.slice(2);
  const check = args[0] === '--check';
  // A temporary copy can be checked without changing the committed page.
  const out = check && args[1] ? path.resolve(args[1]) : OUT;
  const { commands, missing } = collectCommandHelp();
  if (missing.length > 0) {
    process.stderr.write('Missing CLI help metadata: ' + missing.join(', ') + '\n');
    return 1;
  }
  const generated = renderCommandsDoc(commands);
  if (!check) {
    writeFileSync(out, generated);
    process.stdout.write('wrote docs/reference/commands.md\n');
    return 0;
  }
  let onDisk;
  try {
    onDisk = readFileSync(out, 'utf8');
  } catch {
    process.stderr.write('gen-commands-doc --check: command reference does not exist\n');
    return 1;
  }
  if (onDisk === generated) {
    process.stdout.write('gen-commands-doc --check: command reference is up to date\n');
    return 0;
  }
  const actual = onDisk.split('\n');
  const expected = generated.split('\n');
  let index = 0;
  while (index < actual.length && index < expected.length && actual[index] === expected[index]) index += 1;
  process.stderr.write(
    'gen-commands-doc --check: command reference is stale (first difference at line ' + (index + 1) + ')\n' +
    '  on disk:    ' + JSON.stringify(actual[index] ?? '<eof>') + '\n' +
    '  generated:  ' + JSON.stringify(expected[index] ?? '<eof>') + '\n',
  );
  return 1;
}

if (process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) {
  process.exitCode = main();
}
