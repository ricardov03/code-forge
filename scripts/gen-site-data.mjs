#!/usr/bin/env node
/** Maintainer-only site snapshot: tagged documentation and aggregate local proof. */
import { spawnSync } from 'node:child_process';
import { readdirSync, readFileSync, realpathSync, statSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { buildSpend } from '../src/ledger/report.mjs';
import { findSection } from './changelog.mjs';

const USAGE = 'usage: node scripts/gen-site-data.mjs --tag <vX.Y.Z> [--out <file>] [--ledger-dir <dir>]';

/** @param {string[]} args */
function parseArgs(args) {
  const opts = { tag: '', out: '', ledgerDir: path.join(os.homedir(), '.code-forge', 'ledger') };
  for (let i = 0; i < args.length; i += 1) {
    const flag = args[i];
    if (!['--tag', '--out', '--ledger-dir'].includes(flag)) throw new Error(USAGE);
    const value = args[++i];
    if (!value || value.startsWith('--')) throw new Error(`${flag} needs a value\n${USAGE}`);
    if (flag === '--tag') opts.tag = value;
    else if (flag === '--out') opts.out = value;
    else opts.ledgerDir = value;
  }
  if (!/^v\d+\.\d+\.\d+$/.test(opts.tag)) throw new Error(`invalid or missing tag ${JSON.stringify(opts.tag)}\n${USAGE}`);
  return opts;
}

/** @param {string} tag @param {string} file */
function atTag(tag, file) {
  const env = { ...process.env };
  for (const key of ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_PREFIX',
    'GIT_OBJECT_DIRECTORY', 'GIT_COMMON_DIR', 'GIT_ALTERNATE_OBJECT_DIRECTORIES', 'GIT_IMPLICIT_WORK_TREE']) delete env[key];
  const r = spawnSync('git', ['show', `refs/tags/${tag}:${file}`], {
    encoding: 'utf8', env, timeout: 15000, maxBuffer: 16 * 1024 * 1024,
  });
  if (r.status !== 0) throw new Error(`cannot read ${file} at tag ${tag} (unknown tag or missing tagged file)`);
  return r.stdout;
}

/** Keep entry Markdown, including continuation lines and nested lists.
 * @param {string[]} lines
 */
function sectionsOf(lines) {
  /** @type {{name: string, entries: string[]}[]} */
  const sections = [];
  let section = null;
  let entry = [];
  const flush = () => {
    if (section && entry.length) section.entries.push(entry.join('\n').trimEnd());
    entry = [];
  };
  for (const line of lines) {
    const heading = /^###\s+(.+?)\s*$/.exec(line);
    if (heading) {
      flush();
      section = { name: heading[1], entries: [] };
      sections.push(section);
    } else if (/^[-*]\s+\S/.test(line)) {
      flush();
      if (!section) {
        section = { name: '', entries: [] };
        sections.push(section);
      }
      entry.push(line.replace(/^[-*]\s+/, ''));
    } else if (entry.length) entry.push(line);
  }
  flush();
  return sections;
}

/** @param {string} text */
function changelogOf(text) {
  const lines = text.split(/\r?\n/);
  const releases = [];
  for (const line of lines) {
    const heading = /^## \[(\d+\.\d+\.\d+)\]\s*[—–-]\s*(\d{4}-\d{2}-\d{2})\s*$/.exec(line);
    if (!heading) continue;
    const [, version, date] = heading;
    const range = findSection(lines, version);
    releases.push({ version, date, sections: sectionsOf(lines.slice(range.start + 1, range.end)) });
  }
  return releases.sort((a, b) => {
    const av = a.version.split('.').map(Number);
    const bv = b.version.split('.').map(Number);
    return bv[0] - av[0] || bv[1] - av[1] || bv[2] - av[2];
  });
}

/** @param {string} body */
function usageOf(body) {
  return /```(?:text)?\s*\n([\s\S]*?)\n```/.exec(body)?.[1] ?? '';
}

/** @param {string} text */
function commandsOf(text) {
  const lines = text.split(/\r?\n/);
  const headings = [];
  let fenced = false;
  for (let i = 0; i < lines.length; i += 1) {
    if (lines[i].startsWith('```')) fenced = !fenced;
    if (!fenced && /^## \S/.test(lines[i])) headings.push(i);
  }
  return headings.map((start, index) => {
    const name = lines[start].slice(3).trim();
    const body = lines.slice(start + 1, headings[index + 1] ?? lines.length).join('\n');
    const parts = body.split(/^### /m);
    const main = parts.shift();
    return {
      name,
      description: main.split('```')[0].trim(),
      usage: usageOf(main),
      subcommands: parts.map((part) => {
        const end = part.indexOf('\n');
        const heading = part.slice(0, end).trim();
        return { name: heading.startsWith(`${name} `) ? heading.slice(name.length + 1) : heading, usage: usageOf(part.slice(end + 1)) };
      }),
    };
  });
}

/** @param {string} dir @param {string} date */
function proofOf(dir, date) {
  let files;
  try {
    if (!statSync(dir).isDirectory()) throw new Error('not a directory');
    files = readdirSync(dir);
  } catch {
    throw new Error(`cannot read ledger directory ${dir}`);
  }
  const filesForSlug = files.filter((file) => /^code-forge(?:\.\d+)?\.jsonl$/.test(file))
    .sort((a, b) => {
      const rotation = (file) => file === 'code-forge.jsonl' ? -1 : Number(file.split('.')[1]);
      return rotation(b) - rotation(a) || a.localeCompare(b);
    });
  const rows = [];
  for (const file of filesForSlug) {
    const lines = readFileSync(path.join(dir, file), 'utf8').split(/\r?\n/);
    for (let i = 0; i < lines.length; i += 1) {
      if (!lines[i].trim()) continue;
      let row;
      try { row = JSON.parse(lines[i]); } catch { throw new Error(`invalid JSON in ledger file ${file}, line ${i + 1}`); }
      if (!row || typeof row !== 'object' || Array.isArray(row)) throw new Error(`invalid row in ledger file ${file}, line ${i + 1}`);
      rows.push(row);
    }
  }
  const runs = new Set(rows.filter((row) => typeof row.run === 'string' && row.run).map((row) => row.run));
  const blocks = new Set(rows.filter((row) => row.event === 'block.close' && row.status === 'complete'
    && typeof row.run === 'string' && row.run && typeof row.block === 'string' && row.block)
    .map((row) => JSON.stringify([row.run, row.block])));
  return { runs: runs.size, blocks: blocks.size, spend: Math.round(buildSpend(rows).total.usd * 100) / 100, date };
}

function main() {
  try {
    const opts = parseArgs(process.argv.slice(2));
    const changelog = changelogOf(atTag(opts.tag, 'CHANGELOG.md'));
    const release = changelog.find((entry) => entry.version === opts.tag.slice(1));
    if (!release) throw new Error(`no dated CHANGELOG release for tag ${opts.tag}`);
    const data = {
      version: release.version,
      date: release.date,
      changelog,
      commands: commandsOf(atTag(opts.tag, 'docs/reference/commands.md')),
      proof: proofOf(opts.ledgerDir, release.date),
    };
    const json = `${JSON.stringify(data, null, 2)}\n`;
    if (opts.out) writeFileSync(opts.out, json);
    else process.stdout.write(json);
    return 0;
  } catch (error) {
    process.stderr.write(`gen-site-data: ${error.message}\n`);
    return 1;
  }
}

if (process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) process.exitCode = main();
