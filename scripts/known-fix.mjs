#!/usr/bin/env node
/**
 * Record a known fix (B38) in `src/util/known-fixes.json`, for maintainers. Not shipped.
 *
 *   npm run known-fix -- add <fp> <x.y.z> "<summary>" [--issue N]
 *   npm run known-fix -- check
 *
 * `add` validates the new entry (12 lowercase hex characters, a version x.y.z, one plain line of
 * summary, a positive issue number or none), refuses a fingerprint already in the table, and
 * appends it; the file is written back as `JSON.stringify(table, null, 2)` plus a newline. `check`
 * validates the whole table. Both refuse a table that does not validate.
 *
 * Works on `src/util/known-fixes.json` under the current directory. Exit 0 on success, 1 on a
 * refusal, 2 on usage.
 */

import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { entryProblems, validateKnownFixes } from '../src/util/known-fixes.mjs';

const USAGE = 'usage: npm run known-fix -- add <fp> <x.y.z> "<summary>" [--issue N]\n       npm run known-fix -- check';

/** The table, relative to the repository root. */
export const TABLE = path.join('src', 'util', 'known-fixes.json');

class KnownFixError extends Error {
  /** @param {string} message @param {number} [code] */
  constructor(message, code = 1) {
    super(message);
    this.code = code;
  }
}

/** @param {string} file @returns {any[]} the table, validated */
function readTable(file) {
  if (!existsSync(file)) throw new KnownFixError(`${TABLE} not found`);
  let data;
  try {
    data = JSON.parse(readFileSync(file, 'utf8'));
  } catch {
    throw new KnownFixError(`${TABLE} is not valid JSON`);
  }
  const problems = validateKnownFixes(data);
  if (problems.length > 0) throw new KnownFixError(`${TABLE} is invalid:\n  ${problems.join('\n  ')}`);
  return data;
}

/**
 * `add` arguments: three positionals and an optional `--issue N`.
 * @param {string[]} args
 * @returns {{fp: string, fixed_in: string, summary: string, issue: number|null}}
 */
function parseAdd(args) {
  const pos = [];
  /** @type {number|null} */
  let issue = null;
  for (let i = 0; i < args.length; i += 1) {
    const a = args[i];
    if (a === '--issue' || a.startsWith('--issue=')) {
      const v = a === '--issue' ? args[(i += 1)] : a.slice('--issue='.length);
      if (v === undefined || !/^[1-9]\d{0,9}$/.test(v)) throw new KnownFixError('--issue must be a positive issue number', 2);
      issue = Number(v);
    } else if (a.startsWith('--')) {
      throw new KnownFixError(`unknown flag ${a}`, 2);
    } else {
      pos.push(a);
    }
  }
  if (pos.length !== 3) throw new KnownFixError(USAGE, 2);
  return { fp: pos[0], fixed_in: pos[1], summary: pos[2], issue };
}

/** @param {string[]} argv @param {string} [root] @returns {number} */
export function main(argv, root = process.cwd()) {
  const file = path.join(root, TABLE);
  try {
    if (argv.length === 1 && argv[0] === 'check') {
      const table = readTable(file);
      process.stdout.write(`${TABLE}: ${table.length} ${table.length === 1 ? 'entry' : 'entries'}, valid\n`);
      return 0;
    }
    if (argv[0] !== 'add') throw new KnownFixError(USAGE, 2);
    const entry = parseAdd(argv.slice(1));
    const problems = entryProblems(entry);
    if (problems.length > 0) throw new KnownFixError(`the new entry is invalid:\n  ${problems.join('\n  ')}`);
    const table = readTable(file);
    if (table.some((e) => e.fp === entry.fp)) throw new KnownFixError(`${entry.fp} is already in ${TABLE}`);
    table.push(entry);
    writeFileSync(file, `${JSON.stringify(table, null, 2)}\n`);
    process.stdout.write(`added ${entry.fp} (fixed in ${entry.fixed_in}) to ${TABLE}\n`);
    return 0;
  } catch (e) {
    if (!(e instanceof KnownFixError)) throw e;
    process.stderr.write(`REFUSED: ${e.message}\n${e.code === 2 && e.message !== USAGE ? `${USAGE}\n` : ''}`);
    return e.code;
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv.slice(2));
}
