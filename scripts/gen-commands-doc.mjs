#!/usr/bin/env node
/** Generate the command reference from isolated Node CLI invocations. */
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const OUT = path.join(REPO, 'docs/reference/commands.md');

/** @typedef {{verb: string, purpose: string, usage: string | null, subcommands: {name: string, usage: string | null}[]}} Command */

/** Keep usage, without an error message that may precede it. @param {string} text */
function usageText(text) {
  const start = text.search(/^usage:/im);
  return start === -1 ? '' : text.slice(start);
}

/** Discover subcommands in usage order, including compact a|b and bare | b forms.
 * @param {string} verb @param {string} usage
 */
function subcommands(verb, usage) {
  const names = new Set();
  const pattern = new RegExp(`(?:^|\\n[ \\t]*|[·|][ \\t]*)(?:usage:[ \\t]*)?(?:code-forge[ \\t]+)?${verb}[ \\t]+([a-z][a-z0-9-]*(?:\\|[a-z][a-z0-9-]*)*)|[ \\t]+\\|[ \\t]+(?!${verb}\\b)([a-z][a-z0-9-]*)`, 'g');
  for (const match of usage.matchAll(pattern)) {
    const prefix = usage.slice(0, match.index);
    const depth = [...prefix].reduce((n, c) => n + (c === '[' ? 1 : c === ']' ? -1 : 0), 0);
    if (depth > 0) continue; // An option value such as [--mode a | b] is not a subcommand.
    for (const name of (match[1] ?? match[2]).split('|')) names.add(name);
  }
  return [...names];
}

/** @param {string} text */
function purpose(text) {
  const lines = text.split('\n').filter((line) => line.trim());
  return lines.find((line) => /^[A-Z][a-z].*[.!]$/.test(line)) ?? lines[0] ?? '';
}

/** Collect help independently of the renderer. No user config, credentials or native binary.
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
    CODE_FORGE_NATIVE: '0', CODE_FORGE_NO_ERROR_LOG: '1', NO_COLOR: '1',
    LANG: 'C', LC_ALL: 'C', TZ: 'UTC', COLUMNS: '80',
  };
  /** @param {string[]} args */
  const run = (args) => {
    const result = spawnSync(process.execPath, [path.join(REPO, 'bin/code-forge.mjs'), ...args], {
      cwd: root, env, encoding: 'utf8', timeout: 15000, maxBuffer: 4 * 1024 * 1024,
    });
    return { status: result.status, text: result.stdout || result.stderr || '' };
  };
  try {
    const help = run(['help']);
    const verbs = [...help.text.matchAll(/^ {2}([a-z][a-z0-9-]*)$/gm)].map((m) => m[1]);
    if (help.status !== 0 || verbs.length === 0) throw new Error('code-forge help did not list any verbs');
    const missing = [];
    const commands = verbs.map((verb) => {
      const response = run([verb, '--help']);
      const usage = response.status === 0 && response.text.trim() ? response.text : null;
      if (usage === null) missing.push(verb);
      // Older verbs reject --help before printing their usage. With no operands,
      // their argument validation supplies that usage in this empty workspace.
      const parentUsage = usageText(response.text) || (usage === null ? usageText(run([verb]).text) : '');
      return {
        verb, purpose: purpose(parentUsage || usage || ''), usage,
        subcommands: subcommands(verb, parentUsage).map((name) => {
          const sub = run([verb, name, '--help']);
          const ownUsage = sub.status === 0 && usageText(sub.text) ? sub.text : null;
          if (ownUsage === null) missing.push(`${verb} ${name}`);
          return { name, usage: ownUsage || parentUsage || null };
        }),
      };
    });
    return { commands, missing };
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

/** Pure renderer: the same collected help always produces the same Markdown.
 * @param {Command[]} commands @returns {string}
 */
export function renderCommandsDoc(commands) {
  const lines = [
    '# Command reference', '',
    'This page lists the CLI commands and their usage. It is generated from the CLI. ' +
      'Regenerate it with `node scripts/gen-commands-doc.mjs`.', '',
    ...commands.map(({ verb }) => `- [${verb}](#${verb})`), '',
  ];
  /** @param {string | null} usage */
  const addUsage = (usage) => {
    if (usage === null) lines.push('No usage text yet.', '');
    else lines.push('```text', usage.endsWith('\n') ? usage.slice(0, -1) : usage, '```', '');
  };
  for (const command of commands) {
    lines.push(`## ${command.verb}`, '');
    if (command.purpose) lines.push(command.purpose, '');
    addUsage(command.usage);
    for (const sub of command.subcommands) {
      lines.push(`### ${command.verb} ${sub.name}`, '');
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
  for (const name of missing) process.stderr.write(`No usage text yet: ${name}\n`);
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
    `gen-commands-doc --check: command reference is stale (first difference at line ${index + 1})\n` +
    `  on disk:    ${JSON.stringify(actual[index] ?? '<eof>')}\n` +
    `  generated:  ${JSON.stringify(expected[index] ?? '<eof>')}\n`,
  );
  return 1;
}

if (process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) {
  process.exitCode = main();
}
