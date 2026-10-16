/**
 * `code-forge upgrade [--source <path>] [--plain]` — re-points every recorded install at a skill source
 * (plan §10.3 acceptance: "`upgrade` re-points all 3 recorded links"; research/installer-patterns.md
 * point 8, "a self `upgrade` command"). A symlink record is re-created to point at the new
 * source; a copy record is re-copied from it. With no `--source`, defaults to this running
 * package's own `skill/` directory (the normal case: `npm update` moved the package, `upgrade`
 * re-syncs every harness to it) — `--source` exists for pointing at an explicit path instead
 * (tests, or a power user working from a git checkout of the skill).
 */

import { readFileSync } from 'node:fs';
import { readFile, stat } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { stripVTControlCharacters } from 'node:util';
import { writeSafe } from '../util/redact.mjs';
import { installsPath, readInstalls, upgradeAll } from '../install/link.mjs';

const USAGE = 'usage: code-forge upgrade [--source <path>] [--plain]\n';
const LOGO = '+------------+\n| code-forge |\n+------------+\n';
const PACKAGE_VERSION = JSON.parse(readFileSync(new URL('../../package.json', import.meta.url), 'utf8')).version;
const CHANGELOG_PATH = fileURLToPath(new URL('../../CHANGELOG.md', import.meta.url));

const __dirname = path.dirname(fileURLToPath(import.meta.url));
/** This package's own skill directory: `<package root>/skill`, i.e. two levels above `src/cli/`. */
export const DEFAULT_SKILL_SOURCE = path.join(__dirname, '..', '..', 'skill');

/**
 * Strict parse: optional source and plain flags, each at most once, in either order.
 * @param {string[]} args
 * @returns {{source?: string, plain?: boolean} | null}
 */
export function parseUpgradeArgs(args) {
  /** @type {{source?: string, plain?: boolean}} */
  const parsed = {};
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--plain' && !parsed.plain) {
      parsed.plain = true;
    } else if (args[i] === '--source' && parsed.source === undefined) {
      const value = args[++i];
      if (!value || value.startsWith('--')) return null;
      parsed.source = value;
    } else {
      return null;
    }
  }
  return parsed;
}

/** Semver ordering, including numeric prerelease identifiers (rc.2 precedes rc.10).
 * @param {string} a @param {string} b @returns {number}
 */
function compareVersions(a, b) {
  const [coreA, preA] = a.split('+')[0].split(/-(.*)/);
  const [coreB, preB] = b.split('+')[0].split(/-(.*)/);
  const coreOrder = coreA.localeCompare(coreB, 'en', { numeric: true });
  if (coreOrder) return coreOrder;
  if (preA === undefined) return preB === undefined ? 0 : 1;
  if (preB === undefined) return -1;
  const idsA = preA.split('.');
  const idsB = preB.split('.');
  for (let i = 0; i < Math.max(idsA.length, idsB.length); i++) {
    if (idsA[i] === undefined) return -1;
    if (idsB[i] === undefined) return 1;
    if (idsA[i] === idsB[i]) continue;
    const numericA = /^\d+$/.test(idsA[i]);
    const numericB = /^\d+$/.test(idsB[i]);
    if (numericA !== numericB) return numericA ? -1 : 1;
    return numericA ? Number(idsA[i]) - Number(idsB[i]) : (idsA[i] < idsB[i] ? -1 : 1);
  }
  return 0;
}

/** Read only bullets in the exact release section; unavailable notes are a silent omission.
 * @param {string} file @param {string} version @returns {Promise<string[]>}
 */
async function releaseBullets(file, version) {
  let text;
  try {
    text = await readFile(file, 'utf8');
  } catch {
    return [];
  }
  const bullets = [];
  let inRelease = false;
  for (const line of text.split(/\r?\n/)) {
    if (/^##\s/.test(line)) {
      if (inRelease) break;
      inRelease = /^## \[([^\]]+)\](?:\s|$)/.exec(line)?.[1] === version;
    } else if (inRelease && line.startsWith('- ')) {
      const chars = Array.from(stripVTControlCharacters(line.slice(2)));
      bullets.push(chars.length > 100 ? `${chars.slice(0, 97).join('')}...` : chars.join(''));
      if (bullets.length === 5) break;
    }
  }
  return bullets;
}

/**
 * @param {string[]} args
 * @param {object} deps
 * @param {{write: (s: string) => unknown}} [deps.stdout]
 * @param {{write: (s: string) => unknown}} [deps.stderr]
 * @param {string} [deps.home]
 * @param {string} [deps.defaultSource]
 * @param {string} [deps.packageVersion] - running package version, injectable for tests.
 * @param {string} [deps.changelogPath] - packaged release notes, injectable for tests.
 * @param {string} [deps.cwd] - a relative `--source` is resolved against this, never against the
 *   link's own directory (which `symlink()` would otherwise silently use).
 * @returns {Promise<number>}
 */
export async function runUpgrade(
  args,
  {
    stdout = process.stdout,
    stderr = process.stderr,
    home = os.homedir(),
    defaultSource = DEFAULT_SKILL_SOURCE,
    packageVersion = PACKAGE_VERSION,
    changelogPath = CHANGELOG_PATH,
    cwd = process.cwd(),
  } = {},
) {
  const out = (/** @type {string} */ s) => writeSafe(stdout, stripVTControlCharacters(s));
  const err = (/** @type {string} */ s) => writeSafe(stderr, stripVTControlCharacters(s));

  const parsed = parseUpgradeArgs(args);
  if (!parsed) {
    err(USAGE);
    return 2;
  }

  // Resolved BEFORE use: a relative --source (e.g. "./skill") must mean "relative to where the
  // user ran this command", never get stored/symlinked as a relative string (which `readlink`
  // would then resolve against the LINK's own directory, not the user's cwd).
  const resolvedSource = path.resolve(cwd, parsed.source ?? defaultSource);

  let sourceStat;
  try {
    sourceStat = await stat(resolvedSource);
  } catch {
    err(`upgrade: source "${resolvedSource}" does not exist\n`);
    return 1;
  }
  if (!sourceStat.isDirectory()) {
    err(`upgrade: source "${resolvedSource}" is not a directory\n`);
    return 1;
  }

  const installsFile = installsPath(home);
  const previous = await readInstalls(installsFile);
  const updated = await upgradeAll({ installsFile, source: resolvedSource, version: packageVersion });
  if (!parsed.plain) {
    const versions = previous.map((r) => r.version).filter((v) => v !== undefined).sort(compareVersions);
    const oldest = versions[0];
    const change = oldest === undefined ? 'first versioned upgrade'
      : oldest === packageVersion ? `already on ${packageVersion}` : `${oldest} -> ${packageVersion}`;
    out(`${LOGO}code-forge ${packageVersion}\n${change}\n`);
  }
  for (const record of updated) {
    out(`upgraded ${record.harness} (${record.scope}) -> ${record.source}\n`);
  }
  if (updated.length === 0) {
    out('nothing to upgrade\n');
  }
  if (!parsed.plain) {
    const notesPath = path.resolve(changelogPath);
    const bullets = await releaseBullets(notesPath, packageVersion);
    if (bullets.length > 0) {
      out(`What's new in ${packageVersion}:\n${bullets.join('\n')}\nFull notes: ${notesPath}\n`);
    }
  }
  return 0;
}

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function upgrade(args) {
  return runUpgrade(args, {});
}
