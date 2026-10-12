/**
 * B60: how many FILES a block owns, for `gates.full_suite_threshold_files`.
 *
 * The owned entries are resolved against the workspace's tracked and new (not ignored) files
 * (`git ls-files -z --cached --others --exclude-standard`, argv only) with the B57
 * ownership rule (`ownsFile`, state/registry.mjs): a directory entry counts every such file below it, a glob counts its matches, a file counts once. Each file is counted
 * once however many entries cover it. An entry that covers none of them (a path the block will
 * create) counts as 1. Outside a git repository every entry counts as 1; any other git failure
 * (a timeout, a spawn error, git failing inside a repository) gives `Infinity`, so the full suite
 * runs (`reason: threshold`) rather than a guess.
 *
 * {@link countOwnedFiles} is the pure rule; {@link ownedFileCount} reads the files from git.
 */
import os from 'node:os';
import { ownsFile } from '../state/registry.mjs';
import { exec } from '../util/exec.mjs';

const GIT_TIMEOUT_MS = 30000;

/**
 * The number of files `owned` covers among `files`, each counted once, plus 1 for every entry
 * that covers none of them.
 * @param {ReadonlyArray<string>} owned
 * @param {ReadonlyArray<string>} tracked - the workspace's files
 * @param {Record<string, string>} [kinds] - the block's `owned_kinds`
 * @returns {number}
 */
export function countOwnedFiles(owned, tracked, kinds) {
  const covered = new Set();
  let notThere = 0;
  for (const entry of owned) {
    const hits = tracked.filter((file) => ownsFile([entry], file, kinds));
    if (hits.length === 0) notThere += 1;
    for (const file of hits) covered.add(file);
  }
  return covered.size + notThere;
}

/** `process.env` minus every inherited `GIT_*`, with no system/global config, messages in English. */
function gitEnv() {
  /** @type {NodeJS.ProcessEnv} */
  const env = { GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull };
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith('GIT_')) env[key] = value;
  }
  env.LC_ALL = 'C'; // the "not a git repository" message is matched below
  return env;
}

/** The files `git ls-files` lists: tracked (`--cached`) and new, not ignored (`--others --exclude-standard`). */
const LS_FILES_ARGV = Object.freeze(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard']);

/**
 * {@link countOwnedFiles} over the tracked and new (not ignored) files of the workspace at `cwd`.
 * Not a git repository: every entry counts 1. Any other failure: `Infinity` (the full suite runs).
 * @param {string} cwd
 * @param {ReadonlyArray<string>} owned
 * @param {Record<string, string>} [kinds]
 * @returns {Promise<number>}
 */
export async function ownedFileCount(cwd, owned, kinds) {
  const res = await exec([...LS_FILES_ARGV], { cwd, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS });
  if (res.result === 'ok') return countOwnedFiles(owned, res.stdout.split('\0').filter((f) => f.length > 0), kinds);
  if (!res.timedOut && res.code === 128 && /not a git repository/i.test(res.stderr)) return countOwnedFiles(owned, [], kinds);
  return Number.POSITIVE_INFINITY;
}
