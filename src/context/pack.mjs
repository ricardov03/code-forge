/**
 * The coder context pack (block B63a; issue #13 item 5, issue #10 item 2).
 *
 * The top-level `context` key of `.code-forge.yml` names what every coder task should carry:
 *
 *   context:
 *     rules: [CLAUDE.md, docs/conventions.md]   repository-relative files, in order
 *     lessons: docs/lessons.md                  one repository-relative file
 *     commands: {build: [...], test: [...], lint: [...], format: [...]}   argv arrays, each optional
 *     max_lines: 300                            the pack's line cap (integer >= 20)
 *
 * `assemblePack` turns it into ONE pack for a block, in this order: the header (run, block, level,
 * owned files, test files when the block has them, worktree path and branch when it has one), the
 * commands (never run; each argv shown POSIX shell-quoted, so a pasted line expands nothing), each
 * rules file, then the lessons file — every file section headed by its source path (an empty
 * file: the heading alone). Over `max_lines` the lessons are cut first, then the
 * rules from the last file backwards, each cut section ending in `[cut: N lines dropped]`; the
 * header and the commands are never cut.
 *
 * `reviewDigest` is the ONE builder of the review packets' rules digest (`## project rules`, ≤ 60
 * lines, `packet.mjs`): the rules files' text, then a `### lessons` subheading and the lessons.
 * With no `context` key it is '' — the packet is then byte-identical to before (`(none)`). A file
 * it cannot read throws: the worker answers that ticket `review.unavailable`, `context-unreadable`.
 *
 * Files are always read from the MAIN tree's root (`repoRoot`), never from a block worktree a coder
 * can write to. A path that is absolute, has a `..` segment, or leads out of the repository through
 * a symlink (a broken one too: its target is checked) is refused, and so is a missing file
 * (`ContextError`). A control character (a newline included) in a command token, a rules or
 * lessons path, or a header value (owned or test file, worktree path or branch) is refused, so
 * nothing can forge a line of the pack. Command lines go through `redact`, as the rules and
 * lessons do. The `context` key is not
 * authority: it is not fixed for the run, and `run reload` takes a change to it.
 */

import { lstatSync, readFileSync, readlinkSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { estimateTokens } from '../review/packet.mjs';
import { splitLines } from '../review/context.mjs';
import { redact } from '../util/redact.mjs';

/** The pack's line cap when `context.max_lines` is not set. */
export const DEFAULT_MAX_LINES = 300;

/** The smallest `context.max_lines` accepted. */
export const MIN_MAX_LINES = 20;

/** The `context.commands` keys, in the order the pack lists them. */
export const COMMAND_KEYS = Object.freeze(['build', 'test', 'lint', 'format']);

/** The subheading the lessons get in the review digest. */
export const DIGEST_LESSONS_HEADING = '### lessons';

/**
 * A refusal with a stable `code`. Thrown here: `bad-context` (a bad `context` value),
 * `control-character` (in a path or a header value), `outside`, `missing` (ENOENT), `unreadable`
 * (any other lstat or read error, with its code), `not-file`. Thrown by `src/cli/context.mjs`:
 * `no-block`, `bad-config`, `no-context`, `no-run-root`, `write-failed`.
 */
export class ContextError extends Error {
  /** @param {string} code @param {string} message */
  constructor(code, message) {
    super(message);
    this.name = 'ContextError';
    this.code = code;
  }
}

/** C0 and C1 control characters and DEL: a newline among them. */
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/;

/** @param {unknown} v @returns {v is Record<string, unknown>} */
const isMapping = (v) => v !== null && typeof v === 'object' && !Array.isArray(v);

/**
 * @typedef {object} ContextSettings
 * @property {string[]} rules
 * @property {string | null} lessons
 * @property {Record<string, string[]>} commands - only the keys that are set.
 * @property {number} maxLines
 */

/**
 * The `context` key of a config, checked; null when the key is absent.
 * @param {Record<string, any> | null | undefined} cfg
 * @returns {ContextSettings | null}
 * @throws {ContextError} `bad-context`, naming the key.
 */
export function contextSettings(cfg) {
  const raw = cfg?.context;
  if (raw === undefined || raw === null) return null;
  if (!isMapping(raw)) throw new ContextError('bad-context', 'context must be a mapping');
  const rules = raw.rules ?? [];
  if (!Array.isArray(rules) || !rules.every((r) => typeof r === 'string' && r.length > 0)) throw new ContextError('bad-context', 'context.rules must be a list of file paths');
  // `CLAUDE.md` and `./CLAUDE.md` are the same file
  if (new Set(rules.map((r) => path.posix.normalize(r))).size !== rules.length) throw new ContextError('bad-context', 'context.rules names a file twice');
  const lessons = raw.lessons ?? null;
  if (lessons !== null && (typeof lessons !== 'string' || lessons.length === 0)) throw new ContextError('bad-context', 'context.lessons must be one file path');
  const commandsRaw = raw.commands ?? {};
  if (!isMapping(commandsRaw)) throw new ContextError('bad-context', 'context.commands must be a mapping');
  /** @type {Record<string, string[]>} */
  const commands = {};
  for (const key of Object.keys(commandsRaw)) {
    if (!COMMAND_KEYS.includes(key)) throw new ContextError('bad-context', `context.commands.${key} is not one of ${COMMAND_KEYS.join(', ')}`);
  }
  for (const key of COMMAND_KEYS) {
    const argv = commandsRaw[key];
    if (argv === undefined || argv === null) continue;
    if (!Array.isArray(argv) || argv.length === 0 || !argv.every((a) => typeof a === 'string' && a.length > 0)) throw new ContextError('bad-context', `context.commands.${key} must be an argv list of non-empty strings`);
    if (argv.some((a) => CONTROL.test(a))) throw new ContextError('bad-context', `context.commands.${key} contains a control character`);
    commands[key] = [...argv];
  }
  const maxLines = raw.max_lines ?? DEFAULT_MAX_LINES;
  if (!Number.isInteger(maxLines) || /** @type {number} */ (maxLines) < MIN_MAX_LINES) throw new ContextError('bad-context', `context.max_lines must be an integer >= ${MIN_MAX_LINES}`);
  return { rules: [...rules], lessons: /** @type {string | null} */ (lessons), commands, maxLines: /** @type {number} */ (maxLines) };
}

/** How many symlinks one path may pass through before it counts as a loop (as the kernel's ELOOP). */
const MAX_LINK_HOPS = 40;

/**
 * `abs` with every symlink on it resolved, as far as its components exist — a symlink whose target
 * is missing (a broken link) is still followed through its `readlink` text, so where it POINTS is
 * what is checked. A component that does not exist ends the walk (the rest is joined as given); a
 * link loop ends it at the link.
 * @param {string} abs - absolute, without `..` segments. @returns {string}
 */
function walkLinks(abs) {
  const parts = abs.split(path.sep).filter(Boolean);
  let cur = path.parse(abs).root;
  let hops = 0;
  while (parts.length > 0) {
    const next = path.join(cur, /** @type {string} */ (parts.shift()));
    let st;
    try {
      st = lstatSync(next);
    } catch {
      return path.join(next, ...parts);
    }
    if (!st.isSymbolicLink()) {
      cur = next;
      continue;
    }
    hops += 1;
    if (hops > MAX_LINK_HOPS) return path.join(next, ...parts);
    // `cur` holds no symlink, so resolving the target's `..` against it is exact
    const target = path.resolve(cur, readlinkSync(next));
    parts.unshift(...target.split(path.sep).filter(Boolean));
    cur = path.parse(target).root;
  }
  return cur;
}

/** @param {string} root @param {string} p @returns {boolean} `p` is `root` or under it. */
const inside = (root, p) => {
  const rel = path.relative(root, p);
  // only a real `..` segment leaves the root: a file named `..notes.md` is inside
  return !(rel === '..' || rel.startsWith(`..${path.sep}`) || path.isAbsolute(rel));
};

/**
 * The real path of the context file `rel` (named by `key`) under `repoRoot`.
 * @param {string} repoRoot - the main tree's root. @param {string} rel @param {string} key
 * @returns {string}
 * @throws {ContextError} `control-character`, `outside`, `missing`, `unreadable`, `not-file`
 */
export function resolveContextFile(repoRoot, rel, key) {
  const shown = JSON.stringify(rel);
  if (CONTROL.test(rel)) throw new ContextError('control-character', `${key}: ${shown} contains a control character`);
  if (path.isAbsolute(rel) || rel.split(/[\\/]/).includes('..')) {
    throw new ContextError('outside', `${key}: ${shown} is outside the repository (give a path relative to the repository root)`);
  }
  const root = realpathSync(repoRoot);
  // every symlink on the way is followed — a broken one too, by its target — and the end checked
  const real = walkLinks(path.resolve(root, rel));
  if (!inside(root, real)) throw new ContextError('outside', `${key}: ${shown} leads outside the repository`);
  let st;
  try {
    st = lstatSync(real);
  } catch (err) {
    const code = /** @type {NodeJS.ErrnoException} */ (err).code ?? 'error';
    if (code === 'ENOENT') throw new ContextError('missing', `${key}: ${shown} does not exist`);
    throw new ContextError('unreadable', `${key}: ${shown} cannot be read (${code})`);
  }
  // still a link here: a loop (or more than MAX_LINK_HOPS links) — nothing to read
  if (st.isSymbolicLink()) throw new ContextError('missing', `${key}: ${shown} does not exist`);
  if (!st.isFile()) throw new ContextError('not-file', `${key}: ${shown} is not a file`);
  return real;
}

/**
 * @param {string} repoRoot @param {string} rel @param {string} key
 * @returns {string[]} the file's lines (redacted, trailing whitespace of the file dropped); none
 *   for an empty or whitespace-only file.
 * @throws {ContextError} as {@link resolveContextFile}, or `unreadable` when the read fails.
 */
function readLines(repoRoot, rel, key) {
  const file = resolveContextFile(repoRoot, rel, key);
  let raw;
  try {
    raw = readFileSync(file, 'utf8');
  } catch (err) {
    throw new ContextError('unreadable', `${key}: ${JSON.stringify(rel)} cannot be read (${/** @type {NodeJS.ErrnoException} */ (err).code ?? 'error'})`);
  }
  const text = /** @type {string} */ (redact(raw)).trimEnd();
  return text === '' ? [] : splitLines(text);
}

/**
 * Every rules file and the lessons file, read. A missing or outside file is a refusal.
 * @param {string} repoRoot @param {ContextSettings} settings
 * @returns {{rules: Array<{path: string, lines: string[]}>, lessons: {path: string, lines: string[]} | null}}
 * @throws {ContextError}
 */
export function readSources(repoRoot, settings) {
  const rules = settings.rules.map((rel) => ({ path: rel, lines: readLines(repoRoot, rel, 'context.rules') }));
  const lessons = settings.lessons === null ? null : { path: settings.lessons, lines: readLines(repoRoot, settings.lessons, 'context.lessons') };
  return { rules, lessons };
}

/**
 * The token as a POSIX shell would take it back: bare when it holds only characters no shell
 * expands, else in single quotes (an embedded `'` as `'\\''`), so a pasted line never expands
 * `$x`, a backtick, a glob or a brace.
 * @param {string} token @returns {string}
 */
const argvToken = (token) => (/^[A-Za-z0-9_@%+=:,./-]+$/.test(token) ? token : `'${token.replaceAll("'", "'\\''")}'`);

/** @param {string[]} argv @returns {string} */
export const argvText = (argv) => argv.map(argvToken).join(' ');

/**
 * @typedef {object} PackBlock
 * @property {string} run
 * @property {string} block
 * @property {string | null} [level]
 * @property {string[]} [owned]
 * @property {string[] | null} [tests]
 * @property {{path: string, branch: string} | null} [worktree]
 */

/**
 * Build the pack text for a block (pure over what it is given).
 * @param {{settings: ContextSettings, sources: ReturnType<typeof readSources>, block: PackBlock}} opts
 * @returns {{text: string, lines: number, cut: number}} `cut`: the lines dropped.
 * @throws {ContextError} `control-character` when a header value holds one.
 */
export function assemblePack({ settings, sources, block }) {
  /** @type {Array<[string, unknown]>} */
  const values = [['level', block.level], ...(block.owned ?? []).map((f) => /** @type {[string, unknown]} */ (['owned file', f])), ...(block.tests ?? []).map((f) => /** @type {[string, unknown]} */ (['test file', f]))];
  if (block.worktree) values.push(['worktree path', block.worktree.path], ['worktree branch', block.worktree.branch]);
  for (const [label, v] of values) {
    if (typeof v === 'string' && CONTROL.test(v)) throw new ContextError('control-character', `block ${block.block}: ${label} ${JSON.stringify(v)} contains a control character`);
  }
  const header = ['# code-forge context pack', '## block', `run: ${block.run}`, `block: ${block.block}`, `level: ${block.level ?? '(unknown)'}`, 'owned files:'];
  for (const f of block.owned ?? []) header.push(`- ${f}`);
  if (Array.isArray(block.tests) && block.tests.length > 0) {
    header.push('test files:');
    for (const f of block.tests) header.push(`- ${f}`);
  }
  if (block.worktree) header.push(`worktree: ${block.worktree.path}`, `branch: ${block.worktree.branch}`);
  header.push('## commands (never run by code-forge)');
  const keys = COMMAND_KEYS.filter((k) => Object.hasOwn(settings.commands, k));
  if (keys.length === 0) header.push('(none)');
  // a command line can name a secret (a token in an argv): it is redacted as rules and lessons are
  for (const k of keys) header.push(`${k}: ${redact(argvText(settings.commands[k]))}`);

  /** @type {Array<{heading: string, body: string[], dropped: number}>} */
  const sections = sources.rules.map((r) => ({ heading: `## rules: ${r.path}`, body: [...r.lines], dropped: 0 }));
  if (sources.lessons) sections.push({ heading: `## lessons: ${sources.lessons.path}`, body: [...sources.lessons.lines], dropped: 0 });

  const total = () => header.length + sections.reduce((n, s) => n + 1 + s.body.length + (s.dropped > 0 ? 1 : 0), 0);
  // cut order: the lessons first, then the rules from the last file backwards
  for (const s of [...sections].reverse()) {
    let excess = total() - settings.maxLines;
    if (excess <= 0) break;
    // the marker takes one line: a section is only cut when that leaves it shorter
    if (s.body.length < 2) continue;
    const drop = Math.min(s.body.length, excess + 1);
    s.body = s.body.slice(0, s.body.length - drop);
    s.dropped = drop;
  }
  const out = [...header];
  for (const s of sections) {
    out.push(s.heading, ...s.body);
    if (s.dropped > 0) out.push(`[cut: ${s.dropped} lines dropped]`);
  }
  return { text: `${out.join('\n')}\n`, lines: out.length, cut: sections.reduce((n, s) => n + s.dropped, 0) };
}

/**
 * The review packets' rules digest (`## project rules`), built from the MAIN tree each time a
 * packet is built: the rules files' lines in order, then `### lessons` and the lessons' lines.
 * '' when the config has no `context` key (the packet stays as it was), or names no rules and no
 * lessons. `packet.mjs` keeps the first 60 lines and trims from the end over budget, rules and
 * lessons together.
 * @param {{repoRoot: string, cfg: Record<string, any> | null | undefined}} opts
 * @returns {string}
 * @throws {ContextError} a bad `context` value, or a rules or lessons file that is missing,
 *   outside the repository or unreadable — the worker makes that ticket `review.unavailable`
 *   with `reason: context-unreadable` (no review runs without the rules it was promised).
 */
export function reviewDigest({ repoRoot, cfg }) {
  const settings = contextSettings(cfg);
  if (settings === null) return '';
  const out = settings.rules.flatMap((rel) => readLines(repoRoot, rel, 'context.rules'));
  if (settings.lessons !== null) out.push(DIGEST_LESSONS_HEADING, ...readLines(repoRoot, settings.lessons, 'context.lessons'));
  return out.join('\n');
}

/** @param {string} text @returns {number} the pack's estimated tokens (the review packets' bytes / 4). */
export const packTokens = (text) => estimateTokens(text);
