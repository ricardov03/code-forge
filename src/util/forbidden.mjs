/**
 * The one forbidden-command list (plan §8.4), its structural matcher, and its three per-CLI
 * renderers.
 *
 * ## What this is — and what it is not
 *
 * This is a DENYLIST, and a denylist can never be complete: a shell can spell any command in
 * endless ways (`sh -c '…'`, `xargs rm`, `find -delete`, a git alias defined with `git config`,
 * `sudo`/`env`/`nice` wrappers, a script file the coder writes and then runs, …). It is ONE
 * layer of three, and must never be described or relied on as the only one:
 *
 *  1. The sandbox the coder process runs in (working-directory confinement, `--restricted`
 *     file tools, no production credentials in the environment) — the layer that actually
 *     bounds what a command can reach.
 *  2. This list — rendered into each CLI's own deny flag (Claude `--disallowedTools`, Grok
 *     `--deny`, Codex execpolicy rules) and checked by `isForbidden()` before the package itself
 *     spawns anything on a coder's behalf.
 *  3. The block gates (B5: safe-edit, scope, transcript grep) — which judge the RESULT (a
 *     deleted test file, a rewritten history, a forbidden verb in the transcript) regardless of
 *     how the command was spelled.
 *
 * ## `isForbidden()` — the structural matcher
 *
 * It normalizes argv before matching, so the common respellings of a listed command are caught:
 *  - `argv[0]` is compared by basename (`/usr/bin/git`, `/bin/rm`, `git.exe` → `git`, `rm`).
 *  - git's global options before the subcommand are skipped (`git -C . push --force`,
 *    `git -c k=v reset --hard`, `git --git-dir=.git clean -fd`, `git --work-tree . rm x`).
 *    An UNKNOWN option there is tried both as a lone flag and as a flag taking the next token as
 *    its value, and the argv is forbidden if either reading matches — fail closed.
 *  - flags are matched anywhere after the command (`git push origin main --force`), in `--flag`,
 *    `--flag=value` (`--force-with-lease=main:abc`) and combined short-cluster (`-uf`, `-Rf`,
 *    `-rfv`) spellings, plus `+refspec` pushes (`git push origin +main`).
 *  - `contains` tokens match as a SUBSTRING of any argv token (`--db=prod_main`,
 *    `postgres://prod-host/db`); they do not match across two tokens (`--env production`).
 * Known limits, beyond the unbounded respellings listed above: it sees only argv the package
 * itself is about to run — never the commands a coder runs inside its own Bash tool (layers 1
 * and 3 cover those); git aliases and `-c alias.*` definitions are not expanded.
 *
 * ## Entry kinds
 *
 *  - `prefix` — the normalized argv's first N tokens equal `tokens` (e.g. `git clean`).
 *  - `commandFlag` — the normalized argv starts with `command`, AND for EVERY group in `groups`
 *    at least one later token matches that group: equal to a member of `anyOf`, starting with a
 *    member of `anyPrefixOf`, or a single-dash short-option cluster (`-rfv`) containing one of
 *    `shortLetters`. One group = "any of these flags"; two groups = "one of each" (e.g. `git
 *    branch --delete` AND `--force`).
 *  - `contains` — every member of `tokens` is a substring of some argv token (a configured
 *    `production.markers` / `production.names` value; `mergeForbidden()` adds the real ones).
 *
 * ## Renderers — what the CLIs can and cannot enforce
 *
 * The pinned CLIs' deny rules match a command PREFIX (Claude `Bash(<prefix>:*)`, documented in
 * `test/fixtures/help/claude-2.1.282.txt`; Grok's `--deny` is its documented alias; Codex's
 * execpolicy matches a token-array prefix). So each renderer emits, per entry, one concrete rule
 * per flag spelling in the position right after the command (`Bash(git push --force:*)`,
 * `Bash(git push -f:*)`, `Bash(rm -rf:*)`, …). That blocks the common spellings at the CLI layer;
 * a flag placed after other arguments (`git push origin main --force`), a global option before
 * the subcommand, or a `+refspec` escapes a prefix rule — those are caught by `isForbidden()`
 * (for argv the package runs) and by the gates (layer 3). An entry is `enforced: false` only when
 * the CLI syntax cannot express it at all: today that is the `contains` kind (no pinned CLI
 * documents a match-anywhere rule).
 */

import path from 'node:path';

/**
 * @typedef {object} FlagGroup
 * @property {ReadonlyArray<string>} [anyOf] - exact-token flags, any one.
 * @property {ReadonlyArray<string>} [anyPrefixOf] - token prefixes, any one (`--force-with-lease=`, `+`).
 * @property {ReadonlyArray<string>} [shortLetters] - letters matched inside a `-xyz` short-option cluster.
 */

/**
 * @typedef {object} ForbiddenEntry
 * @property {string} id
 * @property {"prefix"|"contains"|"commandFlag"} kind
 * @property {ReadonlyArray<string>} [tokens] - `prefix` and `contains` kinds.
 * @property {ReadonlyArray<string>} [command] - `commandFlag` kind: the required argv prefix.
 * @property {ReadonlyArray<FlagGroup>} [groups] - `commandFlag` kind: every group must match.
 * @property {string} description
 */

/**
 * @param {FlagGroup} group
 * @returns {FlagGroup}
 */
function freezeGroup(group) {
  /** @type {Record<string, unknown>} */
  const frozen = {};
  for (const [key, value] of Object.entries(group)) {
    frozen[key] = Object.freeze([...value]);
  }
  return Object.freeze(/** @type {FlagGroup} */ (frozen));
}

/**
 * @param {object} entry
 * @returns {ForbiddenEntry}
 */
function freezeEntry(entry) {
  /** @type {Record<string, unknown>} */
  const frozen = { ...entry };
  for (const key of ['tokens', 'command']) {
    if (Array.isArray(frozen[key])) {
      frozen[key] = Object.freeze([...frozen[key]]);
    }
  }
  if (Array.isArray(frozen.groups)) {
    frozen.groups = Object.freeze(frozen.groups.map(freezeGroup));
  }
  return Object.freeze(/** @type {ForbiddenEntry} */ (frozen));
}

/** @type {ReadonlyArray<ForbiddenEntry>} */
export const FORBIDDEN = Object.freeze(
  [
    { id: 'gh-pr-ready', kind: 'prefix', tokens: ['gh', 'pr', 'ready'], description: 'Mark a PR ready for review' },
    { id: 'gh-pr-merge', kind: 'prefix', tokens: ['gh', 'pr', 'merge'], description: 'Merge a PR' },
    { id: 'gh-pr-close', kind: 'prefix', tokens: ['gh', 'pr', 'close'], description: 'Close a PR' },
    {
      id: 'gh-pr-base-retarget',
      kind: 'commandFlag',
      command: ['gh', 'pr', 'edit'],
      // `-B` as a prefix also covers pflag's attached form `-Bmain`.
      groups: [{ anyOf: ['--base', '-B'], anyPrefixOf: ['--base=', '-B'] }],
      description: 'Retarget a PR base branch (--base / -B / --base=, anywhere after `gh pr edit`)',
    },
    {
      id: 'git-push-force',
      kind: 'commandFlag',
      command: ['git', 'push'],
      groups: [
        {
          anyOf: ['--force', '-f', '--force-with-lease', '--force-if-includes', '--mirror'],
          anyPrefixOf: ['--force-with-lease=', '--force-if-includes=', '+'],
          shortLetters: ['f'],
        },
      ],
      description:
        'Force-push (rewrites remote history): --force/-f (also inside a cluster like -uf), ' +
        '--force-with-lease[=…], --force-if-includes, --mirror, or a +refspec',
    },
    {
      id: 'git-reset-hard',
      kind: 'commandFlag',
      command: ['git', 'reset'],
      groups: [{ anyOf: ['--hard'] }],
      description: 'Discard local changes (--hard anywhere after `git reset`)',
    },
    {
      id: 'git-checkout-discard',
      kind: 'commandFlag',
      command: ['git', 'checkout'],
      groups: [{ anyOf: ['--', '--force'], shortLetters: ['f'] }],
      description: 'Discard working-tree changes (`git checkout [<ref>] -- <path>`, or a forced checkout)',
    },
    {
      id: 'git-restore',
      kind: 'prefix',
      tokens: ['git', 'restore'],
      description: 'Discard working-tree changes to a path (the modern replacement for checkout --)',
    },
    { id: 'git-clean', kind: 'prefix', tokens: ['git', 'clean'], description: 'Delete untracked files' },
    { id: 'git-stash', kind: 'prefix', tokens: ['git', 'stash'], description: 'Stash working-tree state' },
    {
      id: 'git-branch-force-delete',
      kind: 'commandFlag',
      command: ['git', 'branch'],
      groups: [{ anyOf: ['-D'], shortLetters: ['D'] }],
      description: 'Force-delete a branch (short form -D, also inside a cluster)',
    },
    {
      id: 'git-branch-force-delete-long',
      kind: 'commandFlag',
      command: ['git', 'branch'],
      groups: [
        { anyOf: ['--delete'], shortLetters: ['d'] },
        { anyOf: ['--force'], shortLetters: ['f'] },
      ],
      description: 'Force-delete a branch (--delete/-d together with --force/-f, any order)',
    },
    {
      id: 'git-rm',
      kind: 'prefix',
      tokens: ['git', 'rm'],
      description: 'Delete a tracked file via git — includes deleting a test file',
    },
    {
      id: 'rm-rf',
      kind: 'commandFlag',
      command: ['rm'],
      groups: [{ anyOf: ['--recursive', '--force'], shortLetters: ['r', 'R', 'f'] }],
      description:
        'rm with ANY recursive or force flag (-r, -R, -f, any cluster containing one, --recursive, ' +
        '--force) — a coder never legitimately needs either; the package cleans .code-forge/ itself',
    },
    {
      id: 'production-marker',
      kind: 'contains',
      tokens: ['--env=production'],
      description:
        'Any argv token CONTAINING a configured production.markers/production.names value (env flag, DB ' +
        'name, prod host, deploy tool) — this entry is a placeholder; mergeForbidden() adds the real, ' +
        'project-configured tokens',
    },
  ].map(freezeEntry),
);

/**
 * Merge project-specific tokens (from `production.markers` / `production.names`, schema owned by
 * B1) into the static list as additional `contains` entries. The static list never changes.
 * `contains` entries are never rendered into a CLI rule string (see the renderers), so a token
 * may hold any character — `db.prod:5432` and `postgres://prod-host` are both valid.
 *
 * @param {string[]} [extraTokens]
 * @returns {ForbiddenEntry[]}
 * @throws {TypeError} if `extraTokens` is not an array (a bare string would otherwise be iterated
 *   one character at a time), or if any token is not a non-empty, non-whitespace-only string —
 *   an empty token is a substring of every argv token and would forbid everything.
 */
export function mergeForbidden(extraTokens = []) {
  if (!Array.isArray(extraTokens)) {
    throw new TypeError(`mergeForbidden: extraTokens must be an array of strings, got ${typeof extraTokens}`);
  }
  for (const token of extraTokens) {
    if (typeof token !== 'string' || token.trim().length === 0) {
      throw new TypeError(`mergeForbidden: invalid token ${JSON.stringify(token)} — must be a non-empty string`);
    }
  }
  const extras = extraTokens.map((token, i) =>
    freezeEntry({
      id: `production-marker-extra-${i}`,
      kind: 'contains',
      tokens: [token],
      description: `Configured production marker: ${token}`,
    }),
  );
  return [...FORBIDDEN, ...extras];
}

// ── Normalization ────────────────────────────────────────────────────────────

/** git global options that take the NEXT argv token as their value (when not written `--opt=value`). */
const GIT_GLOBAL_WITH_VALUE = new Set([
  '-C',
  '-c',
  '--git-dir',
  '--work-tree',
  '--namespace',
  '--config-env',
  '--attr-source',
  '--super-prefix',
  '--list-cmds',
]);

/** git global options that stand alone. */
const GIT_GLOBAL_NO_VALUE = new Set([
  '-p',
  '-P',
  '--paginate',
  '--no-pager',
  '--bare',
  '--no-replace-objects',
  '--literal-pathspecs',
  '--glob-pathspecs',
  '--noglob-pathspecs',
  '--icase-pathspecs',
  '--no-optional-locks',
  '--no-lazy-fetch',
  '--no-advice',
  '--exec-path',
  '--html-path',
  '--man-path',
  '--info-path',
]);

/** Upper bound on alternative readings of an argv with unknown git global options. */
const MAX_GIT_READINGS = 64;

/**
 * @param {string} token
 * @returns {string} the command name: basename, minus a Windows `.exe` suffix.
 */
function commandName(token) {
  return path.basename(token).replace(/\.exe$/i, '');
}

/**
 * Every plausible `[subcommand, ...rest]` tail of a git argv, after skipping global options.
 * @param {ReadonlyArray<string>} argv
 * @param {number} i
 * @param {string[][]} out
 */
function collectGitTails(argv, i, out) {
  while (i < argv.length && out.length < MAX_GIT_READINGS) {
    const token = argv[i];
    if (!token.startsWith('-')) {
      out.push(argv.slice(i));
      return;
    }
    if (token === '--' || (token.startsWith('--') && token.includes('=')) || GIT_GLOBAL_NO_VALUE.has(token)) {
      i += 1;
    } else if (GIT_GLOBAL_WITH_VALUE.has(token)) {
      i += 2;
    } else if (/^-[Cc]./.test(token)) {
      i += 1; // value attached to -C / -c
    } else {
      // Unknown option: fail closed by trying it both as a lone flag and as one taking a value.
      collectGitTails(argv, i + 2, out);
      i += 1;
    }
  }
}

/**
 * @param {ReadonlyArray<string>} argv
 * @returns {string[][]} the normalized readings of argv to match entries against.
 */
function normalizedReadings(argv) {
  const name = commandName(argv[0]);
  if (name !== 'git') {
    return [[name, ...argv.slice(1)]];
  }
  /** @type {string[][]} */
  const tails = [];
  collectGitTails(argv, 1, tails);
  return tails.length > 0 ? tails.map((tail) => ['git', ...tail]) : [['git']];
}

// ── Matching ─────────────────────────────────────────────────────────────────

/**
 * @param {ReadonlyArray<string>} argv
 * @param {ReadonlyArray<string>} prefix
 * @returns {boolean}
 */
function startsWithPrefix(argv, prefix) {
  if (argv.length < prefix.length) return false;
  return prefix.every((token, i) => argv[i] === token);
}

/**
 * @param {string} token
 * @param {FlagGroup} group
 * @returns {boolean}
 */
function tokenMatchesGroup(token, group) {
  if (group.anyOf?.includes(token)) return true;
  if (group.anyPrefixOf?.some((p) => token.startsWith(p))) return true;
  if (group.shortLetters && /^-[^-]/.test(token)) {
    const cluster = token.slice(1);
    return group.shortLetters.some((letter) => cluster.includes(letter));
  }
  return false;
}

/**
 * @param {ReadonlyArray<string>} reading - a normalized argv
 * @param {ReadonlyArray<string>} rawArgv - the argv as given (for `contains`)
 * @param {ForbiddenEntry} entry
 * @returns {boolean}
 */
function matchesEntry(reading, rawArgv, entry) {
  if (entry.kind === 'contains') {
    return entry.tokens.every((t) => rawArgv.some((a) => a.includes(t)));
  }
  if (entry.kind === 'prefix') {
    return startsWithPrefix(reading, entry.tokens);
  }
  if (!startsWithPrefix(reading, entry.command)) return false;
  const rest = reading.slice(entry.command.length);
  return entry.groups.every((group) => rest.some((token) => tokenMatchesGroup(token, group)));
}

/**
 * The package's own structural check (see the module doc for what it normalizes and its limits).
 *
 * @param {ReadonlyArray<string>} argv
 * @param {ReadonlyArray<ForbiddenEntry>} [list]
 * @returns {ForbiddenEntry | null} the first matching entry in list order, or null if argv is clear.
 * @throws {TypeError} if argv is not an array of strings.
 */
export function isForbidden(argv, list = FORBIDDEN) {
  if (!Array.isArray(argv) || !argv.every((a) => typeof a === 'string')) {
    throw new TypeError('isForbidden: argv must be an array of strings');
  }
  if (argv.length === 0) return null;
  const readings = normalizedReadings(argv);
  for (const entry of list) {
    if (readings.some((reading) => matchesEntry(reading, argv, entry))) {
      return entry;
    }
  }
  return null;
}

// ── Rendering ────────────────────────────────────────────────────────────────

/**
 * The literal flag spellings a prefix rule should carry for one group: every `anyOf` flag, every
 * short letter as `-x`, and every two-letter cluster of distinct short letters (`-rf`, `-fR`, …).
 * `anyPrefixOf` forms (`--base=`, `+refspec`) have no fixed spelling and are left to `isForbidden`.
 * @param {FlagGroup} group
 * @returns {string[]}
 */
function groupSpellings(group) {
  const spellings = [...(group.anyOf ?? [])];
  const letters = group.shortLetters ?? [];
  for (const a of letters) {
    spellings.push(`-${a}`);
  }
  for (const a of letters) {
    for (const b of letters) {
      if (a !== b) spellings.push(`-${a}${b}`);
    }
  }
  return [...new Set(spellings)];
}

/**
 * Every ordering of the groups × every choice of spelling per group.
 * @param {ReadonlyArray<FlagGroup>} groups
 * @returns {string[][]}
 */
function flagSequences(groups) {
  if (groups.length === 0) return [[]];
  /** @type {string[][]} */
  const sequences = [];
  groups.forEach((group, i) => {
    const others = [...groups.slice(0, i), ...groups.slice(i + 1)];
    for (const spelling of groupSpellings(group)) {
      for (const tail of flagSequences(others)) {
        sequences.push([spelling, ...tail]);
      }
    }
  });
  return sequences;
}

/**
 * The token-array prefixes an entry renders to (empty for `contains`: not expressible).
 * @param {ForbiddenEntry} entry
 * @returns {string[][]}
 */
function renderPatterns(entry) {
  if (entry.kind === 'prefix') {
    return [[...entry.tokens]];
  }
  if (entry.kind === 'commandFlag') {
    const seen = new Set();
    /** @type {string[][]} */
    const patterns = [];
    for (const flags of flagSequences(entry.groups)) {
      const pattern = [...entry.command, ...flags];
      const key = pattern.join('\u0000');
      if (!seen.has(key)) {
        seen.add(key);
        patterns.push(pattern);
      }
    }
    return patterns;
  }
  return [];
}

/**
 * Render the list as Claude Code `--disallowedTools` values, e.g. `Bash(git reset --hard:*)`.
 * One item per entry; `rules` holds one prefix rule per flag spelling (see the module doc for
 * what prefix rules cannot reach). `enforced` is false only when no rule can be expressed.
 * @param {ReadonlyArray<ForbiddenEntry>} [list]
 * @returns {{id: string, rules: string[], enforced: boolean}[]}
 */
export function renderForClaude(list = FORBIDDEN) {
  return list.map((entry) => {
    const rules = renderPatterns(entry).map((pattern) => `Bash(${pattern.join(' ')}:*)`);
    return { id: entry.id, rules, enforced: rules.length > 0 };
  });
}

/**
 * Render the list as Grok `--deny` rule values. Grok's `--deny` is the documented compat alias
 * of `--disallowedTools` (`grok --help`, pinned fixture), so it shares Claude's rule syntax.
 * @param {ReadonlyArray<ForbiddenEntry>} [list]
 * @returns {{id: string, rules: string[], enforced: boolean}[]}
 */
export function renderForGrok(list = FORBIDDEN) {
  return renderForClaude(list);
}

/**
 * Render the list as execpolicy rule objects for Codex's rules file (the file wiring is probed
 * and pinned by `src/engines/**`, a later block; this only shapes the data). Each `patterns`
 * element is a token-array prefix; `decision` is always `forbidden`.
 * @param {ReadonlyArray<ForbiddenEntry>} [list]
 * @returns {{id: string, patterns: string[][], decision: 'forbidden', enforced: boolean, description: string}[]}
 */
export function renderForCodex(list = FORBIDDEN) {
  return list.map((entry) => {
    const patterns = renderPatterns(entry);
    return {
      id: entry.id,
      patterns,
      decision: /** @type {'forbidden'} */ ('forbidden'),
      enforced: patterns.length > 0,
      description: entry.description,
    };
  });
}
