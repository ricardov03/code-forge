#!/usr/bin/env node
/** Maintainer-only input and completion stamp for the docs block before a release. */
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { findUnreleased } from './changelog.mjs';

const STAMP = 'docs/release-docs.json';
const REPO_ENV = ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_PREFIX', 'GIT_OBJECT_DIRECTORY',
  'GIT_COMMON_DIR', 'GIT_ALTERNATE_OBJECT_DIRECTORIES', 'GIT_IMPLICIT_WORK_TREE'];

/** @param {string} root @param {string[]} args */
function git(root, args) {
  const env = { ...process.env, GIT_TERMINAL_PROMPT: '0' };
  for (const key of REPO_ENV) delete env[key];
  return spawnSync('git', args, { cwd: root, env, encoding: 'utf8', timeout: 60000, maxBuffer: 16 * 1024 * 1024 });
}

/** @param {string} root @param {string[]} args */
function gitOk(root, args) {
  const r = git(root, args);
  if (r.status !== 0) throw new Error(`git ${args.join(' ')} failed: ${(r.stderr || r.error?.message || '').trim()}`);
  return r.stdout.trimEnd();
}

/** The nearest v* tag reachable from HEAD, including lightweight tags. @param {string} root */
export function latestReleaseTag(root) {
  // An empty tag list is expected before the first release; other git failures still refuse.
  const tags = gitOk(root, ['tag', '--merged', 'HEAD', '--list', 'v*']);
  return tags ? gitOk(root, ['describe', '--tags', '--match', 'v*', '--abbrev=0', 'HEAD']) : '';
}

/** @param {string} root @param {string} dir @returns {string[]} */
function markdownDocs(root, dir) {
  if (!existsSync(path.join(root, dir))) return [];
  return readdirSync(path.join(root, dir), { withFileTypes: true }).flatMap((entry) => {
    const file = `${dir}/${entry.name}`;
    return entry.isDirectory() ? markdownDocs(root, file) : entry.isFile() && file.endsWith('.md') ? [file] : [];
  }).sort();
}

/** @param {string} title @param {string[]} lines */
function section(title, lines) {
  process.stdout.write(`${title}:\n${lines.length ? lines.join('\n') : '(none)'}\n\n`);
}

/** @param {string} root @param {string} tag */
function brief(root, tag) {
  const since = tag || 'repository start';
  process.stdout.write(`Latest release tag: ${tag || '(none)'}\n\n`);
  section(`Commits since ${since}`, gitOk(root, ['log', '--reverse', '--format=- %h %s', tag ? `${tag}..HEAD` : 'HEAD']).split('\n').filter(Boolean));
  const changed = tag
    ? gitOk(root, ['diff', '--name-only', '-z', tag, 'HEAD']).split('\0').filter(Boolean)
    : gitOk(root, ['ls-tree', '-r', '--name-only', '-z', 'HEAD']).split('\0').filter(Boolean);
  section(`Files changed since ${since}`, changed.map((file) => `- ${file}`));
  const lines = readFileSync(path.join(root, 'CHANGELOG.md'), 'utf8').split('\n');
  const unreleased = findUnreleased(lines);
  section('CHANGELOG [Unreleased]', unreleased ? [lines.slice(unreleased.start + 1, unreleased.end).join('\n').trim()] : []);
  section('Docs', [...(existsSync(path.join(root, 'README.md')) ? ['README.md'] : []), ...markdownDocs(root, 'docs')].map((file) => `- ${file}`));
  section('Generated-doc checks', ['- node scripts/gen-config-doc.mjs --check', '- node scripts/gen-commands-doc.mjs --check']);
  const site = path.resolve(root, process.env.CODE_FORGE_SITE_DIR || '../code-forge-site');
  if (!existsSync(site)) {
    section('Site pages a person must check', [`Site folder absent: ${site}`]);
    return;
  }
  const pages = path.join(site, 'src/pages');
  section('Site pages a person must check', existsSync(pages)
    ? readdirSync(pages, { withFileTypes: true }).filter((entry) => entry.isFile() && entry.name.endsWith('.astro'))
      .map((entry) => `- src/pages/${entry.name}`).sort()
    : []);
}

/** @param {string[]} argv @param {string} [root] */
export function main(argv, root = process.cwd()) {
  if (argv.length !== 1 || !['brief', 'stamp'].includes(argv[0])) {
    process.stderr.write('usage: npm run docs-block -- <brief|stamp>\n');
    return 2;
  }
  try {
    if (argv[0] === 'stamp' && gitOk(root, ['status', '--porcelain', '--untracked-files=normal'])) {
      throw new Error('the working tree is not clean; commit the docs before stamping');
    }
    const tag = latestReleaseTag(root);
    if (argv[0] === 'brief') brief(root, tag);
    else {
      const through = gitOk(root, ['rev-parse', 'HEAD']);
      mkdirSync(path.join(root, 'docs'), { recursive: true });
      writeFileSync(path.join(root, STAMP), JSON.stringify({ through, tag }, null, 2) + '\n');
      process.stdout.write(`${STAMP}\n`);
    }
    return 0;
  } catch (error) {
    process.stderr.write(`REFUSED: ${error.message}\n`);
    return 1;
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv.slice(2));
}
