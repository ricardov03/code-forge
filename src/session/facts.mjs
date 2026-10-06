/**
 * The facts rule (plan §3.8, C16; block B9b): `forge facts` builds the facts sheet a Plan job
 * must read before it designs anything.
 *
 * Step 1 (deterministic): {@link extractClaims} lists every claim token of the brief and the
 * sources it names — CLI flags, `<cli> <verb>` commands (inside code spans), `~/…` and `./…`
 * paths, versions, http(s) endpoints, `@scope/name` packages and environment variables.
 * Step 2: ONE L0 `facts` session (closed-book; Claude gets `--tools "Bash"` with the forbidden list
 * rendered, §5.2) whose packet is the claim list and the rule "run the cheapest read-only check,
 * quote the command and the output line, tag it, never infer". B9b.1: when the caller names the
 * project (`projectDir`), the session runs in a READ-ONLY SNAPSHOT of it ({@link buildSnapshot}:
 * `git archive HEAD` plus the `--sources` inside the project, symlinks and secret-looking files
 * dropped, every file 0444 and directory 0555) so `./…` path claims can be checked; the snapshot
 * lives under the run root — or, when that root itself sits inside a git work tree, under a
 * private B0.1 root in `os.tmpdir()` (`factsTmpRoot`) — and is removed when the verb ends;
 * `spawnSession` refuses a facts cwd below any `.git`. {@link pathViolation} still refuses
 * `~`, `..` and absolute paths outside the tool roots, so the delegate cannot leave it.
 * Step 3 (deterministic): the answer is validated against `schema/facts.schema.json` (only a
 * schema-invalid answer refuses the sheet); rows match claims by their echoed claim
 * ({@link echoedClaim}). A row whose `command` is not an allowed read-only form
 * ({@link readOnlyViolation}'s allow-list; env claims only `printenv NAME >/dev/null`, their excerpt
 * always blanked), or a VERIFIED row with an empty excerpt or from a silent `test`, is downgraded
 * to UNVERIFIABLE on its own (B39) and never counted; every claim, command, excerpt and reason
 * passes through B0 `redact`. The
 * sheet records the brief's sha256 and mtime in its header, both taken from the ONE read of the
 * brief that also feeds the extractor; {@link checkSheetFresh} is what `author` and `plan check` use
 * to refuse a stale one.
 *
 * The sheet carries its rows twice: a table for the reader and a `<!-- facts-json … -->` block
 * that {@link parseSheet} reads back (`plan check` looks claims up there, also when the sheet is
 * pasted verbatim into a plan's §0).
 */

import { createHash, randomBytes } from 'node:crypto';
import { chmodSync, closeSync, constants as FS, fstatSync, lstatSync, mkdirSync, openSync, readdirSync, readFileSync, realpathSync, rmSync, statSync, unlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { Ajv2020 } from 'ajv/dist/2020.js';
import { appendRow } from '../ledger/write.mjs';
import { exec } from '../util/exec.mjs';
import { redact } from '../util/redact.mjs';
import { currentRunRoot } from '../util/tmp.mjs';
import { factsTmpRoot, gitWorkTreeAncestor, spawnSession } from './spawn.mjs';

/** @typedef {'flag'|'command'|'path'|'version'|'endpoint'|'package'|'env'} ClaimKind */
/** @typedef {{token: string, kind: ClaimKind}} Claim */
/**
 * @typedef {object} Fact
 * @property {string} fact_id @property {string} claim @property {ClaimKind} kind
 * @property {string} command @property {string} output_excerpt
 * @property {'VERIFIED'|'NOT-FOUND'|'UNVERIFIABLE'} tag @property {string | null} why
 */

export const FACTS_SCHEMA_PATH = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'schema', 'facts.schema.json');

/** The delegate's answer schema, as the file states it (with `maxLength`). */
export const FACTS_SCHEMA = Object.freeze(JSON.parse(readFileSync(FACTS_SCHEMA_PATH, 'utf8')));

/** @param {any} node @returns {any} a copy without annotations and string-length keywords. */
function portable(node) {
  if (Array.isArray(node)) return node.map(portable);
  if (node === null || typeof node !== 'object') return node;
  /** @type {Record<string, any>} */
  const out = {};
  for (const [key, value] of Object.entries(node)) {
    if (['$schema', '$id', 'title', 'description', 'maxLength', 'minLength', 'pattern'].includes(key)) continue;
    out[key] = portable(value);
  }
  return out;
}

/**
 * The schema handed to the provider: string-length and pattern keywords are not portable to every
 * structured-output mode, so the session gets them stripped and step 3 re-checks the file schema.
 */
export const FACTS_SESSION_SCHEMA = Object.freeze(portable(FACTS_SCHEMA));

const validateFile = new Ajv2020({ allErrors: true, strict: false }).compile(
  Object.fromEntries(Object.entries(FACTS_SCHEMA).filter(([k]) => k !== '$id')),
);

/** Excerpt cap in characters (the schema's `maxLength`). */
export const EXCERPT_MAX = 200;

/** Default `--max-budget-usd` of the facts session (§3.8: "typically under $0.10"). */
export const FACTS_MAX_BUDGET_USD = 0.5;

/** An error raised before or after the session (`code`: `usage`, `refused`, `stale`, `snapshot`). */
export class FactsError extends Error {
  /** @param {string} code @param {string} message */
  constructor(code, message) {
    super(message);
    this.name = 'FactsError';
    this.code = code;
  }
}

// ---------------------------------------------------------------------------------------------
// Step 1 — the claim-token extractor
// ---------------------------------------------------------------------------------------------

/** Kinds in the order they are matched; a span matched by an earlier kind is masked for later ones. */
const PATTERNS = /** @type {ReadonlyArray<[ClaimKind, RegExp]>} */ ([
  ['endpoint', /https?:\/\/[^\s`'"<>)\]|]+/g],
  ['package', /(?<![\w./@-])@[a-z0-9][a-z0-9._-]*\/[a-z0-9][a-z0-9._-]*/g],
  ['path', /(?<![\w/.~-])(?:~|\.{1,2})\/[^\s`'"<>)\]|,;]*/g],
  ['flag', /(?<![\w-])--[a-z][a-z0-9-]*/g],
  ['version', /(?<![\w.§])\d+\.\d+(?:\.\d+)*/g],
  ['env', /(?<![\w])\$?[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+(?![\w])/g],
]);

/** A `<cli> <verb>` pair at the start of a code span (`claude plugin validate .` ⇒ `claude plugin`). */
const COMMAND_IN_SPAN = /^([a-z][a-z0-9-]*) ([a-z][a-z0-9-]*)(?![\w.\/-])/;

/** @param {string} token */
const trimTail = (token) => token.replace(/[.,:;]+$/, '');

/**
 * @typedef {object} ExtractOpts
 * @property {ReadonlyArray<string>} [projectBins] - the project's own CLI names (its package.json
 *   `bin` keys, {@link readProjectBins}); none ⇒ command claims are kept as written.
 * @property {string} [briefText] - the brief alone (no sources): only IT can name the project CLI
 *   that a command claim gets prefixed with (default: the whole text).
 */

/**
 * `word` as a whole CLI word: not inside another word (`a-word`, `word2`), a path (`./word`,
 * `dir/word`) or a file name (`.word.yml`, `word.config`); a sentence's full stop after it is fine.
 * @param {string} word @returns {RegExp}
 */
export const cliWordRe = (word) => new RegExp(`(?<![\\w./-])${word.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}(?![\\w/-]|\\.\\w)`);

/**
 * Does the brief use `word` as a CLI of its own in a code span — the span is `word` alone, or `word`
 * followed by an option or a path (`` `gh` ``, `` `gh --repo x` ``, `` `jq ./a.json` ``)? A
 * `` `word verb` `` span is the claim shape itself and does not count.
 * @param {string} word @param {string} brief
 * @returns {boolean}
 */
function namedAsCli(word, brief) {
  return [...brief.matchAll(/`([^`\n]+)`/g)].some((m) => {
    const span = m[1].trim();
    return span === word || (span.startsWith(`${word} `) && /^[-./~]/.test(span.slice(word.length + 1).trimStart()));
  });
}

/** CLIs that are never a verb of the project's own CLI: a claim starting with one is never prefixed. */
const WELL_KNOWN_CLIS = new Set(['git', 'gh', 'npm', 'npx', 'node', 'pnpm', 'yarn', 'bun', 'deno', 'ls', 'cat', 'grep', 'which', 'find', 'test', 'make', 'docker', 'curl', 'claude', 'codex', 'python', 'python3', 'pip', 'go', 'cargo', 'op']);

/**
 * B50: a `<cli> <verb>` claim written without the project's CLI (`run reload` in a brief about
 * `code-forge`) gets it — deterministically, from the brief and package.json only (never PATH):
 * the BRIEF (`opts.briefText`, never a source) names a project bin as a whole word
 * ({@link cliWordRe}), the claim's first word is not itself a project bin nor a well-known CLI
 * ({@link WELL_KNOWN_CLIS}), and the brief never uses that first word as a CLI of its own
 * ({@link namedAsCli}). Then the token becomes `<bin> <token>`; anything else stays as written.
 * @param {string} token @param {string} text @param {ExtractOpts} opts
 * @returns {string}
 */
function withProjectCli(token, text, opts) {
  const bins = (opts.projectBins ?? []).filter((b) => BARE_CLI.test(b));
  const first = token.split(' ')[0];
  const brief = opts.briefText ?? text;
  if (bins.length === 0 || bins.includes(first) || WELL_KNOWN_CLIS.has(first) || namedAsCli(first, brief)) return token;
  const named = bins.find((b) => cliWordRe(b).test(brief));
  return named === undefined ? token : `${named} ${token}`;
}

/**
 * The CLI names a project ships: the keys of its package.json `bin` (a string `bin` names the
 * package, without its scope). No package.json, or no `bin` ⇒ none.
 * @param {string} projectDir
 * @returns {string[]}
 * @throws {FactsError} `usage` when package.json exists but cannot be read or is not JSON (the
 *   message names the file only, never its content).
 */
export function readProjectBins(projectDir) {
  let text;
  try {
    text = readFileSync(path.join(projectDir, 'package.json'), 'utf8');
  } catch (err) {
    if (/** @type {NodeJS.ErrnoException} */ (err).code === 'ENOENT') return []; // the project ships no CLI
    throw new FactsError('usage', `package.json at the project root cannot be read (${/** @type {NodeJS.ErrnoException} */ (err).code ?? 'error'})`);
  }
  let pkg;
  try {
    pkg = JSON.parse(text);
  } catch {
    throw new FactsError('usage', 'package.json at the project root is not valid JSON');
  }
  if (typeof pkg?.bin === 'string' && typeof pkg.name === 'string') return [pkg.name.replace(/^@[^/]+\//, '')];
  if (pkg?.bin !== null && typeof pkg?.bin === 'object' && !Array.isArray(pkg.bin)) return Object.keys(pkg.bin);
  return [];
}


/**
 * Every claim token of `text`, deduplicated by (kind, token), in order of first appearance. With
 * `opts.projectBins`, a command claim written without its CLI gets the project's (B50,
 * {@link withProjectCli}).
 * @param {string} text
 * @param {ExtractOpts} [opts]
 * @returns {Claim[]}
 */
export function extractClaims(text, opts = {}) {
  let masked = String(text);
  /** @type {Array<{index: number, token: string, kind: ClaimKind}>} */
  const found = [];
  /** @param {number} start @param {number} length */
  const mask = (start, length) => {
    masked = masked.slice(0, start) + ' '.repeat(length) + masked.slice(start + length);
  };
  /** @param {ClaimKind} kind @param {RegExp} re */
  const scan = (kind, re) => {
    for (const m of [...masked.matchAll(re)]) {
      const token = trimTail(kind === 'env' ? m[0].replace(/^\$/, '') : m[0]);
      if (token.length === 0 || (kind === 'path' && /^(~|\.{1,2})\/$/.test(token))) continue;
      found.push({ index: /** @type {number} */ (m.index), token, kind });
      mask(/** @type {number} */ (m.index), m[0].length);
    }
  };
  scan(...PATTERNS[0]);
  scan(...PATTERNS[1]);
  scan(...PATTERNS[2]);
  for (const m of [...masked.matchAll(/`([^`\n]+)`/g)]) {
    const cmd = COMMAND_IN_SPAN.exec(m[1]);
    if (!cmd) continue;
    const start = /** @type {number} */ (m.index) + 1;
    found.push({ index: start, token: withProjectCli(`${cmd[1]} ${cmd[2]}`, String(text), opts), kind: 'command' });
    mask(start, cmd[0].length);
  }
  for (const entry of PATTERNS.slice(3)) scan(...entry);
  found.sort((a, b) => a.index - b.index);
  const seen = new Set();
  /** @type {Claim[]} */
  const claims = [];
  for (const { token, kind } of found) {
    const key = `${kind}\0${token}`;
    if (seen.has(key)) continue;
    seen.add(key);
    claims.push({ token, kind });
  }
  return claims;
}

/**
 * The text of every source: a file as is, a directory's top-level `.md`/`.txt` files in name order.
 * @param {ReadonlyArray<string>} sources
 * @returns {string[]}
 */
function readSourcesText(sources) {
  /** @type {string[]} */
  const parts = [];
  for (const source of sources) {
    const st = statSync(source);
    if (st.isDirectory()) {
      for (const name of readdirSync(source).filter((n) => /\.(md|txt)$/.test(n)).sort()) {
        parts.push(readFileSync(path.join(source, name), 'utf8'));
      }
    } else {
      parts.push(readFileSync(source, 'utf8'));
    }
  }
  return parts;
}

/**
 * The brief's text plus every source it names ({@link readSourcesText}).
 * @param {string} briefPath @param {ReadonlyArray<string>} [sources]
 * @returns {string}
 */
export function readClaimText(briefPath, sources = []) {
  return [readFileSync(briefPath, 'utf8'), ...readSourcesText(sources)].join('\n');
}

/**
 * The brief, read ONCE: its bytes (sha256 and claim text) and its mtime from the same open file.
 * @param {string} briefPath
 * @returns {{bytes: Buffer, mtime: string}}
 */
function readBriefOnce(briefPath) {
  const fd = openSync(briefPath, 'r');
  try {
    return { bytes: readFileSync(fd), mtime: fstatSync(fd).mtime.toISOString() };
  } finally {
    closeSync(fd);
  }
}

// ---------------------------------------------------------------------------------------------
// Step 3 — the read-only check and the answer validation
// ---------------------------------------------------------------------------------------------

/** Separators between commands: `;`, `&&`, `||`, `|`, `&`, newline, carriage return. */
const SEPARATORS = new Set([';', '&', '|', '\n', '\r']);

/** Characters the shell expands into file names when they are not quoted or escaped. */
const GLOB_CHARS = new Set(['*', '?', '[', ']', '{', '}']);

/**
 * Split a command into segments of words. Quotes group; a backslash escapes the next character.
 * Any `$`, backtick, `<` or `>` outside single quotes — and inside them too, for `<`/`>` — is
 * reported instead of parsed, and so is an unquoted `* ? [ ] { }` (a glob expands AFTER the
 * secret-path check and could name what the check never saw): the allow-list never needs one
 * (fail closed).
 * @param {string} command
 * @returns {{segments: string[][], separators: string[], pipedOut?: boolean[], refused: string | null}}
 *   `separators`: every separator met, in order (`|`, `||`, `&&`, `;`, `&`, newline, CR);
 *   `pipedOut[i]`: segment i feeds a single `|` (its output is not the command's output).
 */
function splitCommand(command) {
  if (/[`]/.test(command)) return { segments: [], separators: [], refused: 'backticks are not allowed' };
  if (/\$/.test(command)) return { segments: [], separators: [], refused: '$ expansion is not allowed' };
  if (/[<>]/.test(command)) return { segments: [], separators: [], refused: 'redirection is not allowed' };
  /** @type {string[][]} */
  const segments = [[]];
  /** @type {string[]} */
  const separators = [];
  /** @type {boolean[]} */
  const piped = [false];
  let word = '';
  let inWord = false;
  const flush = () => {
    if (inWord) segments[segments.length - 1].push(word);
    word = '';
    inWord = false;
  };
  for (let i = 0; i < command.length; i += 1) {
    const c = command[i];
    if (c === "'" || c === '"') {
      const end = command.indexOf(c, i + 1);
      if (end < 0) return { segments: [], separators: [], refused: 'unbalanced quote' };
      word += command.slice(i + 1, end);
      inWord = true;
      i = end;
    } else if (c === '\\') {
      if (i + 1 >= command.length || command[i + 1] === '\n' || command[i + 1] === '\r') return { segments: [], separators: [], refused: 'line continuation is not allowed' };
      word += command[i + 1];
      inWord = true;
      i += 1;
    } else if (SEPARATORS.has(c)) {
      flush();
      if ((c === '&' || c === '|') && command[i + 1] === c) {
        separators.push(c + c);
        i += 1;
      } else {
        separators.push(c);
        if (c === '|') piped[piped.length - 1] = true;
      }
      segments.push([]);
      piped.push(false);
    } else if (c === ' ' || c === '\t') {
      flush();
    } else {
      if (GLOB_CHARS.has(c)) return { segments: [], separators: [], refused: 'unquoted glob characters (* ? [ ] { }) are not allowed' };
      word += c;
      inWord = true;
    }
  }
  flush();
  const kept = segments.map((s, i) => /** @type {[string[], boolean]} */ ([s, piped[i]])).filter(([s]) => s.length > 0);
  return { segments: kept.map(([s]) => s), separators, pipedOut: kept.map(([, p]) => p), refused: null };
}

/** A token that names a secret-looking file (read refused, whatever the command). */
const SECRET_PATH = [
  /(^|\/)\.env[^/]*$/,
  /(^|\/)\.npmrc$/,
  /(^|\/)\.netrc$/,
  /id_rsa/,
  /id_ed25519/,
  /(^|\/)\.ssh(\/|$)/,
  /(^|\/)\.aws(\/|$)/,
  /(^|\/)\.config\/gh(\/|$)/,
  /\.pem$/,
  /\.key$/,
  /(^|\/)\.code-forge(\/|$)/,
  /(^|\/)\.git-credentials$/,
  /(^|\/)\.docker\/config\.json$/,
  /(^|\/)\.kube\/config$/,
  /(^|\/)\.claude\/\.credentials\.json$/,
  /(^|\/)\.pgpass$/,
  /^\/proc\/[^/]+\/environ$/,
];

/** The only roots an absolute argument may point into (tool installs; never `~`, `/` or the rest). */
const TOOL_ROOTS = ['/usr', '/opt/homebrew', '/bin'];

/**
 * Why one argument reaches outside the delegate's cwd, or null. Relative paths stay inside the
 * cwd (the read-only project snapshot, or an empty directory); `~`, a `..` segment and any absolute path outside {@link TOOL_ROOTS} are refused, so
 * a recursive reader (`rg . ~`, `find / -type f`, `ls -R /etc`) has nowhere to go.
 * @param {string} word
 * @returns {string | null}
 */
function pathViolation(word) {
  if (word.startsWith('~')) return 'paths under ~ are not allowed';
  if (/(^|\/)\.\.(\/|$)/.test(word)) return 'paths with a .. segment are not allowed';
  if (word.startsWith('/') && !TOOL_ROOTS.some((root) => word === root || word.startsWith(`${root}/`))) {
    return 'absolute paths outside /usr, /opt/homebrew and /bin are not allowed';
  }
  return null;
}

/** Commands that read and print, allowed with any in-cwd arguments (after the secret-path check). */
// B50: `rg` is not one — it searches recursively by default, so any pattern could sweep the tree
const PLAIN_READERS = new Set(['ls', 'cat', 'head', 'tail', 'wc', 'grep', 'stat', 'file', 'uname', 'sw_vers', 'jq', 'which']);

// B50: no `--help`, `-h` or `--version` form anywhere (a program may read it as an operand: BSD
// `rm foo --help` deletes); a version claim is checked with `npm view`, `which` or `ls`
const GIT_READS = new Set(['log', 'show', 'status', 'rev-parse', 'ls-files', 'cat-file']);
const NPM_READS = new Set(['view', 'ls']);
/**
 * find: an ALLOWED predicate list (B9b.1). Any other word starting with `-` (`-exec`, `-execdir`,
 * `-ok`, `-okdir`, `-delete`, `-fls`, `-fprint*`, …) is refused by omission; `!` and bare words are
 * operands (paths, patterns, numbers).
 */
export const FIND_PREDICATES = Object.freeze(['-name', '-iname', '-path', '-ipath', '-type', '-maxdepth', '-mindepth', '-print', '-print0', '-newer', '-size', '-empty', '-mtime', '-mmin', '-not', '!', '-a', '-o', '-and', '-or']);
const FIND_ALLOWED = new Set(FIND_PREDICATES);
/** The predicates whose next word is their operand (`-mtime -1`, `-size -10k` stay operands). */
const FIND_TAKES_VALUE = new Set(['-name', '-iname', '-path', '-ipath', '-type', '-maxdepth', '-mindepth', '-newer', '-size', '-mtime', '-mmin']);
const FIND_MESSAGE = `find: only ${FIND_PREDICATES.join(' ')} and paths`;

/**
 * @param {string[]} rest - the words after `find`.
 * @returns {string | null}
 */
function findViolation(rest) {
  for (let i = 0; i < rest.length; i += 1) {
    const t = rest[i];
    if (FIND_ALLOWED.has(t)) {
      if (FIND_TAKES_VALUE.has(t)) {
        if (i + 1 >= rest.length) return FIND_MESSAGE;
        i += 1;
      }
    } else if (t.startsWith('-') || ['(', ')', ','].includes(t)) {
      return FIND_MESSAGE;
    }
  }
  return null;
}
/** Build/task runners: their targets run user recipes; named in the refusal so the delegate sees why. */
const BUILD_RUNNERS = new Set(['make', 'gmake', 'just', 'task', 'rake', 'gradle', 'gradlew', 'mvn', 'mvnw', 'ant', 'ninja', 'cmake', 'bazel', 'bazelisk', 'tox', 'nox', 'invoke', 'inv', 'doit', 'earthly', 'mage', 'npx', 'pnpx', 'bunx']);
/** curl: the long options allowed; the short ones are any cluster of `s S I L f`; `--max-time <N>` is handled apart. */
const CURL_LONG = new Set(['--head', '--silent', '--show-error', '--location', '--fail']);
const CURL_MESSAGE = 'curl: only -s -S -I -L -f --head --silent --show-error --location --fail --max-time <N> and exactly one http(s) URL, as a HEAD request';

/**
 * curl as an option allow-list: every token is an allowed flag, `--max-time <digits>` or the one
 * `http(s)://` URL, and one of the flags asks for HEAD. Anything else (`-o`, `-D`, `-c`, `--trace*`,
 * `--stderr`, `--libcurl`, `--etag-save`, `--hsts`, `--alt-svc`, `-K`, …) is refused by omission.
 * @param {string[]} rest
 * @returns {string | null}
 */
function curlViolation(rest) {
  let head = false;
  let urls = 0;
  for (let i = 0; i < rest.length; i += 1) {
    const t = rest[i];
    if (/^https?:\/\//.test(t)) {
      urls += 1;
    } else if (/^-[sSILf]+$/.test(t)) {
      if (t.includes('I')) head = true;
    } else if (CURL_LONG.has(t)) {
      if (t === '--head') head = true;
    } else if (t === '--max-time' && /^\d+$/.test(rest[i + 1] ?? '')) {
      i += 1;
    } else {
      return CURL_MESSAGE;
    }
  }
  return head && urls === 1 ? null : CURL_MESSAGE;
}

/**
 * Why one segment is not an allowed read-only form, or null.
 * @param {string[]} argv
 * @returns {string | null}
 */
function segmentViolation(argv) {
  if (argv.some((t) => SECRET_PATH.some((re) => re.test(t)))) return 'reads a secret-looking file';
  const [verb, ...rest] = argv;
  if (verb.includes('/')) return 'a verb may not be a path';
  if (!/^[A-Za-z0-9._-]+$/.test(verb)) return `not an allowed read-only form: "${verb}"`;
  for (const word of rest) {
    const why = pathViolation(word);
    if (why) return why;
  }
  if (PLAIN_READERS.has(verb)) {
    if (verb === 'grep') return grepViolation(rest);
    return null;
  }
  switch (verb) {
    case 'command':
      return rest.length === 2 && rest[0] === '-v' ? null : 'command: only command -v <name>';
    case 'find':
      return findViolation(rest);
    case 'git':
      if (rest.length === 0 || !GIT_READS.has(rest[0])) return 'git: only log, show, status, rev-parse, ls-files or cat-file, with no global option';
      return rest.some((t) => /^--(output|ext-diff|textconv)/.test(t)) ? 'git: --output, --ext-diff and --textconv are not allowed' : null;
    case 'npm':
      return rest.length > 0 && NPM_READS.has(rest[0]) ? null : 'npm: only view or ls';
    case 'test':
      return rest.length === 2 && ['-e', '-f', '-d', '-n'].includes(rest[0]) ? null : 'test: only -e, -f, -d or -n with one operand';
    case 'curl':
      return curlViolation(rest);
    default:
      // B50: no `<cli> --help|-h|--version` form — any other program is refused
      if (BUILD_RUNNERS.has(verb)) return `${verb}: build and task runners are not allowed`;
      return `not an allowed read-only form: "${verb}"`;
  }
}

/** A bare CLI name (never a path). */
const BARE_CLI = /^[a-z0-9][a-z0-9._-]*$/;
/** The pattern of the recursive source grep: a long flag with at least 3 characters after `--`. */
const SOURCE_GREP_FLAG = /^--[a-z][a-z0-9-]{2,}$/;

/**
 * B50: the ONLY option words grep may carry before `--` (an exact allow-list — no digits, no long
 * option, no other cluster: `-2r`, `--recur`, `--dir=recurse`, `-d`, `-R`, `-l`, `-L`, `-e`, `-f` are
 * all refused). `-rn`/`-nr` is the recursive source grep and needs its exact form ({@link sourceGrep}).
 */
const GREP_OPTIONS = new Set(['-n', '-c', '-i', '-w', '-F', '-rn', '-nr']);
const GREP_RECURSIVE = new Set(['-rn', '-nr']);
const GREP_MESSAGE = 'grep: options only -n -c -i -w -F before --, or the source grep grep -rn -- <flag> <path>';
const SOURCE_GREP_MESSAGE = 'grep -rn: only grep -rn -- <--long-flag> <path> (one relative subdirectory or file, never . or the root)';

/** @param {string[]} rest - the words after `grep`. @returns {string[]} every dash word before `--` (GNU grep permutes, so an option may follow an operand). */
function grepOptionWords(rest) {
  const end = rest.indexOf('--');
  return (end < 0 ? rest : rest.slice(0, end)).filter((t) => t.startsWith('-'));
}

/** @param {string[]} rest @returns {boolean} the grep asks for recursion (`-rn`/`-nr` before `--`). */
function grepRecurses(rest) {
  return grepOptionWords(rest).some((t) => GREP_RECURSIVE.has(t));
}

/**
 * Why a grep segment is not allowed, or null: every option word before `--` is in
 * {@link GREP_OPTIONS}; a recursive one is exactly the source grep ({@link sourceGrep}).
 * @param {string[]} rest - the words after `grep`.
 * @returns {string | null}
 */
function grepViolation(rest) {
  if (!grepOptionWords(rest).every((t) => GREP_OPTIONS.has(t))) return GREP_MESSAGE;
  if (grepRecurses(rest)) return sourceGrep(rest) === null ? SOURCE_GREP_MESSAGE : null;
  return null;
}

/**
 * Is `where` a relative subdirectory or file of the cwd — never the cwd itself (`.`, `./`, `src/..`),
 * the root, `~`, an absolute path, a `..` segment or a secret-looking path? The facts snapshot
 * already drops every secret-looking file at any depth (`sealTree` and `copyNoFollow` test the full
 * relative path against {@link SECRET_PATH}, so `config/.env` goes too); a named subdirectory is a
 * second fence, so a grep never sweeps the whole tree.
 * @param {string} where
 * @returns {boolean}
 */
function isSubPath(where) {
  if (where.length === 0 || where.startsWith('/') || where.startsWith('~') || where.startsWith('-')) return false;
  if (pathViolation(where) !== null || SECRET_PATH.some((re) => re.test(where))) return false;
  const norm = path.posix.normalize(where);
  return norm !== '.' && norm !== './' && !norm.startsWith('..') && !norm.split('/').some((seg) => SECRET_PATH.some((re) => re.test(seg)));
}

/**
 * B50: the one recursive `grep` form — a flag claim checked in the project's SOURCE TEXT, inside the
 * read-only snapshot, alone (never in a pipe), EXACTLY `grep -rn -- <flag> <path>` (or `-nr`): five
 * words, the pattern a long flag of 3+ characters ({@link SOURCE_GREP_FLAG}, so `-a`, `-e` or `-1`
 * cannot match every line), one relative subdirectory or file ({@link isSubPath}). Claude runs grep
 * as a read-only command, so the delegate needs no permission rule for it.
 * @param {string[]} rest - the words after `grep`.
 * @returns {{pattern: string, path: string} | null} null when `rest` is not exactly that form.
 */
function sourceGrep(rest) {
  if (rest.length !== 4 || !GREP_RECURSIVE.has(rest[0]) || rest[1] !== '--') return null;
  const [, , pattern, where] = rest;
  return SOURCE_GREP_FLAG.test(pattern) && isSubPath(where) ? { pattern, path: where } : null;
}

/**
 * The pattern and path of a command that is exactly one source-grep segment ({@link sourceGrep}), or null.
 * @param {string} command
 * @returns {{pattern: string, path: string} | null}
 */
function sourceGrepOf(command) {
  const { segments, separators, refused } = splitCommand(command);
  if (refused || segments.length !== 1 || separators.length !== 0 || segments[0][0] !== 'grep') return null;
  return sourceGrep(segments[0].slice(1));
}

/** Folders whose text is about the code, not the code: a hit there could be the claim quoting itself. */
const NON_SOURCE_DIRS = new Set(['docs', 'plans', 'test', 'tests', 'fixtures', 'node_modules']);
/** Top-level folders a NOT-FOUND never searches as "the source": text, tests, vendored or built output. */
const NON_SOURCE_FOLDERS = new Set([...NON_SOURCE_DIRS, 'dist', 'build', 'coverage', 'out']);
/** Text files never prove a flag (the brief and every note are one). */
const NON_SOURCE_FILE = /\.(md|markdown|txt)$/i;

/** @param {string} p @returns {string} `p` as a normalised POSIX path without a leading `./` or a trailing `/`. */
const relPosix = (p) => path.posix.normalize(p).replace(/^(\.\/)+/, '').replace(/\/+$/, '');

/**
 * Is `file` (relative to the delegate's cwd) a SOURCE file — not a `.md`, `.markdown` or `.txt`
 * file, under no `docs/ plans/ test/ tests/ fixtures/ node_modules/` folder (at any depth), and
 * not the brief itself?
 * @param {string} file @param {string | undefined} briefRel
 * @returns {boolean}
 */
function isSourceFile(file, briefRel) {
  const rel = relPosix(file);
  if ((briefRel !== undefined && rel === relPosix(briefRel)) || NON_SOURCE_FILE.test(rel)) return false;
  return !rel.split('/').slice(0, -1).some((seg) => NON_SOURCE_DIRS.has(seg));
}

/**
 * Does the excerpt hold at least one `file:line:text` hit whose file sits under the grep's `under`
 * path, is a source file ({@link isSourceFile}) and whose text holds `flag` as a whole token
 * ({@link holdsFlag})? A hit line without the `file:line:` prefix never counts.
 * @param {string} excerpt @param {string} flag @param {string} under - the grep's path.
 * @param {string | undefined} briefRel
 * @returns {boolean}
 */
function sourceHit(excerpt, flag, under, briefRel) {
  const scope = relPosix(under);
  return excerpt.split(/\r?\n/).some((line) => {
    const hit = /^([^:\n]+):(\d+):(.*)$/.exec(line.trim());
    if (hit === null) return false;
    const file = relPosix(hit[1]);
    return (file === scope || file.startsWith(`${scope}/`)) && isSourceFile(file, briefRel) && holdsFlag(hit[3], flag);
  });
}

/**
 * Is `where` a source FOLDER of the delegate's cwd (the snapshot): an existing directory, no segment
 * hidden (`.x`) or one of {@link NON_SOURCE_FOLDERS}, and — with `topLevel` — one single name (a
 * NOT-FOUND proves absence only over a whole top-level folder). A file (`package.json`,
 * `src/a.mjs`) never counts; with no known cwd nothing can be checked, so nothing counts.
 * @param {string} where @param {string | undefined} cwd @param {boolean} topLevel
 * @returns {boolean}
 */
function isSourceFolder(where, cwd, topLevel) {
  const rel = relPosix(where);
  const segs = rel.split('/');
  if (cwd === undefined || rel.length === 0 || rel === '.' || (topLevel && segs.length !== 1)) return false;
  if (segs.some((seg) => seg.startsWith('.') || NON_SOURCE_FOLDERS.has(seg))) return false;
  try {
    return statSync(path.join(cwd, rel)).isDirectory();
  } catch {
    return false;
  }
}

const ONE_COMMAND_MESSAGE = 'one command per row: no pipe or list (| ; && || & or a newline)';

/**
 * Why a delegate's `command` is not an allowed read-only command, or null when it is. An
 * ALLOW-LIST (fail closed): exactly ONE command (B50: any `;`, `&&`, `||`, `|`, `&`, newline or CR
 * is refused, as the packet says), and it is one of the forms in {@link segmentViolation}; `$`,
 * backticks and redirections are refused.
 * @param {string} command
 * @returns {string | null}
 */
export function readOnlyViolation(command) {
  const { segments, separators, refused } = splitCommand(command);
  if (refused) return refused;
  if (segments.length === 0) return 'empty command';
  // B50: one command per row, as the packet says — no pipe and no list
  if (separators.length > 0) return ONE_COMMAND_MESSAGE;
  for (const argv of segments) {
    const why = segmentViolation(argv);
    if (why) return why;
  }
  return null;
}

/** The one form an env claim may be checked with: existence only, the value never printed. */
const ENV_CHECK = /^printenv ([A-Z][A-Z0-9_]*) >\/dev\/null$/;

/** @param {string} text @returns {string} `text` trimmed, without one pair of surrounding backtick runs. */
function unquoteBackticks(text) {
  const t = text.trim();
  const m = /^(`+)([\s\S]*?)(`+)$/.exec(t);
  return m && t.length > 1 ? m[2].trim() : t;
}

/**
 * The claim token a delegate echoed back: the packet lists claims as `- F<n> <kind>: <token>`, so
 * the delegate may copy `<kind>: <token>`, with or without backticks. Only a leading `<kind>: ` and
 * surrounding backticks are stripped; anything else stays (and then does not match).
 * @param {string} claim @param {string} kind
 * @returns {string}
 */
export function echoedClaim(claim, kind) {
  let t = unquoteBackticks(String(claim));
  if (t.startsWith(`${kind}: `)) t = t.slice(kind.length + 2);
  return unquoteBackticks(t);
}

/**
 * Does this allow-listed segment print nothing on success? From {@link segmentViolation}'s list:
 * `test …`; `grep`/`rg` with `-q`/`--quiet`/`--silent` (a short cluster holding `q` counts, operands
 * after `--` do not); `which -s` (a short cluster holding `s`). `[ … ]` is never allowed.
 * @param {string[]} argv
 * @returns {boolean}
 */
function isSilentSegment(argv) {
  const [verb, ...rest] = argv;
  if (verb === 'test') return true;
  const end = rest.indexOf('--');
  const opts = end < 0 ? rest : rest.slice(0, end);
  if (verb === 'grep' || verb === 'rg') return opts.some((t) => /^-[a-zA-Z]*q/.test(t) || t === '--quiet' || t === '--silent');
  if (verb === 'which') return opts.some((t) => /^-[a-zA-Z]*s/.test(t));
  return false;
}

/**
 * Does the command print nothing on success? Only a segment not piped into another one prints to
 * the command's output; the command is silent when every such segment is silent.
 * @param {string} command
 * @returns {boolean}
 */
export function isSilentCommand(command) {
  const { segments, pipedOut = [] } = splitCommand(command);
  return segments.length > 0 && segments.every((argv, i) => pipedOut[i] || isSilentSegment(argv));
}

/** @typedef {{fact_id: string, reason: string}} Downgrade */

const SOURCE_GREP_CLAIM_MESSAGE = 'a source grep counts only for a flag claim, with that flag as its pattern; not counted';
const SOURCE_GREP_HIT_MESSAGE = 'VERIFIED source grep without a file:line: hit in a source file naming the claimed flag (docs, plans, tests, fixtures, text files and the brief do not count); not counted';
const SOURCE_GREP_SCOPE_MESSAGE = 'NOT-FOUND source grep must search one whole top-level source folder (not docs, plans, test, tests, fixtures or node_modules); not counted';
const FLAG_PROOF_MESSAGE = 'a flag claim is proved only by grep -rn -- <flag> <source folder>; not counted';

/**
 * Does a grep hit line hold `flag` as a whole token — not preceded by a word character or `-`, not
 * followed by `[A-Za-z0-9_-]` (`--force-push` does not hold `--force`)? A `-l` file-name line never does.
 * @param {string} excerpt @param {string} flag
 * @returns {boolean}
 */
function holdsFlag(excerpt, flag) {
  return new RegExp(`(?<![\\w-])${flag.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}(?![A-Za-z0-9_-])`).test(excerpt);
}

/**
 * Which check rule a counted row (VERIFIED or NOT-FOUND) breaks, or null. A row the delegate
 * already tagged UNVERIFIABLE is never counted, so it keeps its own `why`.
 * @param {Fact} row @param {Claim} claim
 * @param {{briefRel?: string, cwd?: string}} ctx - the brief relative to the delegate's cwd, and that cwd (the snapshot).
 * @returns {string | null}
 */
function ruleBroken(row, claim, ctx) {
  if (row.tag === 'UNVERIFIABLE') return null;
  if (row.command.trim().length === 0) return `${row.tag} without a check command; not counted`;
  if (claim.kind === 'env') {
    const env = ENV_CHECK.exec(row.command);
    if (!env || env[1] !== claim.token) return `an env claim may only be checked with printenv ${claim.token} >/dev/null; not counted`;
  } else {
    const why = readOnlyViolation(row.command);
    if (why) return `check command not allowed: ${why}; not counted`;
    if (row.tag === 'VERIFIED' && isSilentCommand(row.command)) return 'silent command; excerpt cannot be its output; not counted';
    // B50: a flag claim is proved (VERIFIED or NOT-FOUND) ONLY by `grep -rn -- <flag> <dir>`, its
    // pattern the claimed flag, over an existing source directory of the snapshot; a source grep
    // proves nothing else. A VERIFIED one needs a `file:line:` hit in a source file under that
    // directory holding the flag as a whole token (never the brief, a doc or a test: the claim would
    // prove itself); a NOT-FOUND one needs one whole top-level source folder
    const grep = sourceGrepOf(row.command);
    if (grep !== null && (claim.kind !== 'flag' || grep.pattern !== claim.token)) return SOURCE_GREP_CLAIM_MESSAGE;
    if (claim.kind === 'flag' && (grep === null || !isSourceFolder(grep.path, ctx.cwd, false))) return FLAG_PROOF_MESSAGE;
    if (grep !== null && row.tag === 'VERIFIED' && !sourceHit(row.output_excerpt, grep.pattern, grep.path, ctx.briefRel)) return SOURCE_GREP_HIT_MESSAGE;
    if (grep !== null && row.tag === 'NOT-FOUND' && !isSourceFolder(grep.path, ctx.cwd, true)) return SOURCE_GREP_SCOPE_MESSAGE;
  }
  if (row.tag === 'VERIFIED' && row.output_excerpt.trim().length === 0) return 'VERIFIED with an empty output excerpt; not counted';
  return null;
}

/**
 * Validate the delegate's answer (step 3). Only a schema-invalid answer is refused. A row answers
 * claim `F<n>` when its fact_id is `F<n>` and its kind is the claim's kind (the id is the anchor,
 * the claim text may be reworded); failing that, when its echoed claim ({@link echoedClaim})
 * equals the claim token and its fact_id is `F<n>` or its kind is the claim's kind. Claims the
 * delegate left out are kept as UNVERIFIABLE rows ("no answer") — a missing answer is never read
 * as a fact. A VERIFIED or NOT-FOUND row that breaks a check rule (a command off the read-only
 * allow-list, an env claim not checked with `printenv NAME >/dev/null`, a VERIFIED row with an
 * empty excerpt or from a silent `test`) becomes UNVERIFIABLE with `why` = the rule and is listed
 * in `downgraded`. Every row shows the CANONICAL claim token and kind.
 * @param {unknown} answer @param {ReadonlyArray<Claim>} claims
 * @param {{briefRel?: string, cwd?: string}} [opts] - `briefRel`: the brief's path relative to the
 *   delegate's cwd (a source-grep hit in it never counts); `cwd`: that cwd (the project snapshot),
 *   where a NOT-FOUND source grep's folder must exist as a directory (none ⇒ no NOT-FOUND grep counts).
 * @returns {{facts: Fact[], downgraded: Downgrade[]}} the rows in claim order, and the downgrades.
 * @throws {FactsError} `refused` when the answer does not match the schema.
 */
export function validateFacts(answer, claims, opts = {}) {
  if (!validateFile(answer)) {
    const where = (validateFile.errors ?? []).map((e) => `${e.instancePath || '/'} ${e.message}`).slice(0, 5);
    throw new FactsError('refused', `facts answer does not match facts.schema.json: ${where.join('; ')}`);
  }
  const rows = /** @type {{facts: Fact[]}} */ (answer).facts;
  /** @type {Fact[]} */
  const facts = [];
  /** @type {Downgrade[]} */
  const downgraded = [];
  // each row answers at most ONE claim: pass 1 anchors rows by fact_id (with the kind, else with the
  // echoed claim); pass 2 gives the rows left over to claims still open, by kind + echoed claim
  /** @type {Set<Fact>} */
  const taken = new Set();
  /** @type {Array<Fact | undefined>} */
  const chosen = claims.map((claim, i) => {
    const id = `F${i + 1}`;
    const row = rows.find((r) => !taken.has(r) && r.fact_id === id && r.kind === claim.kind) ?? rows.find((r) => !taken.has(r) && r.fact_id === id && echoedClaim(r.claim, claim.kind) === claim.token);
    if (row) taken.add(row);
    return row;
  });
  claims.forEach((claim, i) => {
    if (chosen[i]) return;
    const row = rows.find((r) => !taken.has(r) && r.kind === claim.kind && echoedClaim(r.claim, claim.kind) === claim.token);
    if (row) taken.add(row);
    chosen[i] = row;
  });
  claims.forEach((claim, i) => {
    const id = `F${i + 1}`;
    const row = chosen[i];
    if (!row) {
      facts.push({ fact_id: id, claim: redact(claim.token), kind: claim.kind, command: '', output_excerpt: '', tag: 'UNVERIFIABLE', why: 'no answer from the delegate' });
      return;
    }
    const base = { fact_id: id, claim: redact(claim.token), kind: claim.kind, command: redact(row.command), output_excerpt: claim.kind === 'env' ? '' : redact(row.output_excerpt) };
    const broken = ruleBroken(row, claim, opts);
    if (broken) {
      const reason = redact(broken);
      downgraded.push({ fact_id: id, reason });
      facts.push({ ...base, tag: 'UNVERIFIABLE', why: reason });
    } else {
      facts.push({ ...base, tag: row.tag, why: row.why === null ? null : redact(row.why) });
    }
  });
  return { facts, downgraded };
}

// ---------------------------------------------------------------------------------------------
// The sheet
// ---------------------------------------------------------------------------------------------

/** @param {string | Buffer} bytes @returns {string} */
export const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex');

/** @param {string} text */
const cell = (text) => String(text).replace(/\r?\n/g, ' ').replace(/\|/g, '\\|');

/** @param {string} text @returns {string} a code span that survives backticks inside. */
const code = (text) => (text.length === 0 ? '—' : text.includes('`') ? `\`\` ${text} \`\`` : `\`${text}\``);

/**
 * @param {{briefRef: string, briefSha: string, briefMtime: string, builtAt: string, facts: ReadonlyArray<Fact>}} sheet
 * @returns {string} the facts sheet markdown.
 */
export function renderSheet({ briefRef, briefSha, briefMtime, builtAt, facts }) {
  const count = (/** @type {string} */ tag) => facts.filter((f) => f.tag === tag).length;
  const lines = [
    '# Facts sheet',
    '',
    `- brief: \`${briefRef}\``,
    `- brief_sha256: \`${briefSha}\``,
    `- brief_mtime: \`${briefMtime}\``,
    `- built_at: \`${builtAt}\``,
    `- claims: ${facts.length} (VERIFIED ${count('VERIFIED')} · NOT-FOUND ${count('NOT-FOUND')} · UNVERIFIABLE ${count('UNVERIFIABLE')})`,
    '',
    '| fact_id | tag | kind | claim | command | output | why |',
    '|---|---|---|---|---|---|---|',
    ...facts.map((f) => `| ${f.fact_id} | ${f.tag} | ${f.kind} | ${cell(code(f.claim))} | ${cell(code(f.command))} | ${cell(f.output_excerpt || '—')} | ${cell(f.why ?? '—')} |`),
    '',
    '<!-- facts-json',
    `[\n${facts.map((f) => JSON.stringify(f)).join(',\n')}\n]`.replace(/</g, '\\u003c').replace(/>/g, '\\u003e'),
    '-->',
    '',
  ];
  return lines.join('\n');
}

/**
 * Read a sheet (or a plan whose §0 is the sheet, verbatim) back.
 * @param {string} text
 * @returns {{briefRef: string, briefSha: string, briefMtime: string, facts: Fact[]} | null} null when no sheet is there.
 */
export function parseSheet(text) {
  const sha = /^- brief_sha256: `([0-9a-f]{64})`$/m.exec(text);
  const ref = /^- brief: `([^`]+)`$/m.exec(text);
  const mtime = /^- brief_mtime: `([^`]+)`$/m.exec(text);
  const json = /^<!-- facts-json\n([\s\S]*?)\n-->$/m.exec(text);
  if (!sha || !ref || !json) return null;
  let facts;
  try {
    facts = JSON.parse(json[1]);
  } catch {
    return null;
  }
  if (!Array.isArray(facts)) return null;
  return { briefRef: ref[1], briefSha: sha[1], briefMtime: mtime ? mtime[1] : '', facts };
}

/**
 * Is the sheet still about this brief? Stale ⇔ the brief's current sha256 differs from the one the
 * sheet recorded (a touched-but-unchanged brief is not stale; an edited one is).
 * @param {string} sheetText @param {string} briefPath
 * @returns {{ok: true, sheet: NonNullable<ReturnType<typeof parseSheet>>} | {ok: false, code: 'bad-sheet' | 'stale', message: string}}
 */
export function checkSheetFresh(sheetText, briefPath) {
  const sheet = parseSheet(sheetText);
  if (!sheet) return { ok: false, code: 'bad-sheet', message: 'facts sheet is not a facts sheet: run forge facts first' };
  if (sha256(readFileSync(briefPath)) !== sheet.briefSha) {
    return { ok: false, code: 'stale', message: 'facts sheet is stale: brief changed after it was built' };
  }
  return { ok: true, sheet };
}

// ---------------------------------------------------------------------------------------------
// Step 2 + the whole verb
// ---------------------------------------------------------------------------------------------

const RULE = [
  '# Facts delegate',
  '',
  'For each claim below, run the cheapest READ-ONLY check (`grep -rn -- <flag> <path>`, `which <cli>`,',
  '`ls <path>`, `npm view <name> version`). Quote the exact command and the output line it printed (at most 200',
  'characters). Tag each claim VERIFIED (the output shows it), NOT-FOUND (the check ran and it is absent) or',
  'UNVERIFIABLE (no read-only check can decide it — say why in `why`). Never infer, never guess: the excerpt is the',
  "command's literal output, never your reading of it. For a path use `ls <path>` (it prints the path); `test` prints",
  'nothing, so a `test` check can never be VERIFIED. A flag is checked in the source text of the project, never by',
  'running a program: `grep -rn -- <flag> <path>` (`<path>` a top-level source folder such as `src`, never `.`,',
  'the root, docs, plans or tests) — VERIFIED only with a hit line (`file:line:text`) from a source file (not',
  'Markdown or text, not docs, plans, tests or fixtures) that contains the flag, as the excerpt. Never run a program with `--help`, `-h` or `--version`: a version is checked with `npm view <name>',
  'version`, `which` or `ls`. Run one command at a time, never a pipe. Only these forms are accepted: which,',
  'command -v, ls, cat, head, tail, wc, grep (options only -n -c -i -w -F; recursive only as',
  '`grep -rn -- <flag> <path>`),',
  'find (only -name -iname -path -ipath -type -maxdepth -mindepth -print -print0 -newer -size -empty -mtime -mmin',
  '-not ! -a -o -and -or, plus paths), stat, file, uname, sw_vers, jq, git',
  'log|show|status|rev-parse|ls-files|cat-file, npm view|ls, test -e|-f|-d|-n, `curl -sI <one URL>`.',
  'A check that breaks these rules is not counted.',
  'No `$`, no backticks, no redirection, no unquoted glob, no `~`, no absolute path outside /usr, /opt/homebrew or',
  '/bin, no secret files.',
  'An env claim is checked ONLY with `printenv NAME >/dev/null` (quote its exit status; the value is never shown).',
  '',
  'Reply with ONE JSON object: {"facts": [{"fact_id", "claim", "kind", "command", "output_excerpt", "tag", "why"}]} —',
  'one row per claim: `fact_id` (F<n>) and `kind` copied from the list, `claim` = the token alone (the text after',
  '`<kind>: `), `why` null unless UNVERIFIABLE.',
  '',
  '## Claims',
  '',
].join('\n');

/**
 * @param {ReadonlyArray<Claim>} claims
 * @returns {string} the delegate's packet.
 */
export function buildFactsPacket(claims) {
  return `${RULE}${claims.map((c, i) => `- F${i + 1} ${c.kind}: ${c.token}`).join('\n')}\n`;
}

/**
 * @typedef {object} BuildFactsOpts
 * @property {Record<string, any>} cfg
 * @property {string} briefPath - absolute.
 * @property {string[]} [sources]
 * @property {string} outPath - absolute path of the sheet to write.
 * @property {string} [runRoot] @property {string} [run] @property {string} [slug]
 * @property {number} [maxBudgetUsd] @property {number} [timeoutMs]
 * @property {string} [projectDir] - the project (the verb's cwd): the delegate runs in a read-only
 *   snapshot of its git HEAD plus the in-project `sources`; absent ⇒ an empty cwd.
 * @property {() => Date} [now]
 */

// ---------------------------------------------------------------------------------------------
// B9b.1 — the delegate's read-only project snapshot
// ---------------------------------------------------------------------------------------------

const GIT_TIMEOUT_MS = 120000;

/** `process.env` minus every inherited `GIT_*`, with no system/global git config. */
function gitEnv() {
  /** @type {NodeJS.ProcessEnv} */
  const env = { GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull };
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith('GIT_')) env[key] = value;
  }
  return env;
}

/**
 * Drop symlinks and secret-looking entries, then make every file 0444 and every directory 0555
 * (children first). A symlink could point anywhere outside the snapshot; a secret file is never
 * handed to the delegate even though its reads are refused by name.
 * @param {string} dir @param {string} [rel] - `dir` relative to the snapshot root.
 */
function sealTree(dir, rel = '') {
  for (const name of readdirSync(dir)) {
    const full = path.join(dir, name);
    const relPath = rel ? `${rel}/${name}` : name;
    const st = lstatSync(full);
    if (st.isSymbolicLink() || SECRET_PATH.some((re) => re.test(relPath))) {
      rmSync(full, { recursive: true, force: true });
    } else if (st.isDirectory()) {
      sealTree(full, relPath);
    } else if (st.isFile()) {
      chmodSync(full, 0o444);
    } else {
      unlinkSync(full); // fifos, sockets, devices: never part of a read-only view
    }
  }
  chmodSync(dir, 0o555);
}

/** @param {string} p @returns {string} `p` realpath'd when it exists, else resolved as given. */
function realOrResolved(p) {
  try {
    return realpathSync(p);
  } catch {
    return path.resolve(p);
  }
}

/** @param {string} dir - make every directory under `dir` writable again, then remove it. */
export function removeSnapshot(dir) {
  /** @param {string} d */
  const unseal = (d) => {
    chmodSync(d, 0o700);
    for (const name of readdirSync(d)) {
      const full = path.join(d, name);
      if (lstatSync(full).isDirectory()) unseal(full);
    }
  };
  try {
    unseal(dir);
  } catch (err) {
    if (/** @type {NodeJS.ErrnoException} */ (err).code !== 'ENOENT') throw err;
  }
  rmSync(dir, { recursive: true, force: true });
}

/** @param {string} p @returns {import('node:fs').Stats | null} lstat of `p`, or null when absent. */
function lstatOrNull(p) {
  try {
    return lstatSync(p);
  } catch {
    return null;
  }
}

/**
 * Remove every symlink and special entry below `dir` (regular files and directories stay). Runs
 * right after `tar -x`, BEFORE any source is copied in: a symlink tracked at HEAD (`foo -> ../..`)
 * that the working tree has replaced with a real directory would otherwise turn a source copy
 * into a write outside the snapshot.
 * @param {string} dir
 */
function dropLinks(dir) {
  for (const name of readdirSync(dir)) {
    const full = path.join(dir, name);
    const st = lstatSync(full);
    if (st.isDirectory()) dropLinks(full);
    else if (!st.isFile()) unlinkSync(full); // a symlink (to anything), fifo, socket or device
  }
}

/**
 * Refuse a path below `snapDir` that crosses a symlink: every segment from `snapDir` down to `dir`
 * (inclusive) is lstat'd; a missing segment ends the walk (the copy creates it next).
 * @param {string} snapDir @param {string} dir
 */
function assertNoLinkSegments(snapDir, dir) {
  const rel = path.relative(snapDir, dir);
  let cur = snapDir;
  for (const seg of rel === '' ? [] : rel.split(path.sep)) {
    cur = path.join(cur, seg);
    const st = lstatOrNull(cur);
    if (st === null) return;
    if (st.isSymbolicLink()) throw new FactsError('snapshot', `the project snapshot refused a copy across a symlink (${path.relative(snapDir, cur)})`);
    if (!st.isDirectory()) throw new FactsError('snapshot', `the project snapshot has a file where a directory is needed (${path.relative(snapDir, cur)})`);
  }
}

/**
 * Copy `src` (a working-tree file or directory) to `dst` inside `snapDir` without following any
 * symlink: entries are lstat'd (symlinks, `.git` and secret-looking paths dropped), parents are
 * created here and checked segment by segment, and every file is created with
 * `O_EXCL | O_NOFOLLOW` — a HEAD file of the same name is unlinked first, never written through.
 * @param {string} src @param {string} dst @param {string} snapDir
 */
function copyNoFollow(src, dst, snapDir) {
  const rel = path.relative(snapDir, dst).split(path.sep).join('/');
  const st = lstatSync(src);
  if (st.isSymbolicLink() || /(^|\/)\.git(\/|$)/.test(rel) || SECRET_PATH.some((re) => re.test(rel))) return;
  assertNoLinkSegments(snapDir, path.dirname(dst));
  mkdirSync(path.dirname(dst), { recursive: true, mode: 0o700 }); // only segments the check found missing
  const at = lstatOrNull(dst);
  if (st.isDirectory()) {
    if (at === null) mkdirSync(dst, { mode: 0o700 });
    else if (!at.isDirectory()) throw new FactsError('snapshot', `the project snapshot has a file where a directory is needed (${rel})`);
    for (const name of readdirSync(src)) copyNoFollow(path.join(src, name), path.join(dst, name), snapDir);
    return;
  }
  if (!st.isFile()) return;
  if (at !== null) {
    if (!at.isFile()) throw new FactsError('snapshot', `the project snapshot has a directory where a file is needed (${rel})`);
    unlinkSync(dst); // the HEAD copy: the source's current content replaces it
  }
  const fd = openSync(dst, FS.O_WRONLY | FS.O_CREAT | FS.O_EXCL | FS.O_NOFOLLOW, 0o600);
  try {
    writeFileSync(fd, readFileSync(src));
  } finally {
    closeSync(fd);
  }
}

/**
 * Build the facts delegate's read-only view of the project at `snapDir`: the tracked tree at HEAD
 * (`git archive` + `tar -x`, binary-safe, the real index and tree never touched; every symlink and
 * special entry dropped right after extraction) plus every `source` inside the project at its
 * current content ({@link copyNoFollow}; untracked sources included; a source outside `projectDir`
 * — a sibling directory of the repo included — is left out: it could only be reached through
 * `..`). Not a git repo, or no commit yet (`git rev-parse --verify HEAD` fails) ⇒ the sources
 * alone; HEAD resolves but the archive or the extraction fails ⇒ `FactsError('snapshot')`. The
 * tar file never outlives the call.
 * @param {string} projectDir @param {ReadonlyArray<string>} sources - absolute.
 * @param {string} snapDir - created here. @param {string} workDir - scratch for the tar file.
 * @param {typeof exec} [run] - the git/tar runner (tests inject a failing one).
 * @returns {Promise<string>} the session cwd: `snapDir` joined with the project's path below the git
 *   top level (the project itself when it is a subdirectory of the repo).
 */
export async function buildSnapshot(projectDir, sources, snapDir, workDir, run = exec) {
  mkdirSync(snapDir, { recursive: true, mode: 0o700 });
  const project = realpathSync(projectDir);
  let root = project;
  const top = await run(['git', 'rev-parse', '--show-toplevel'], { cwd: project, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS });
  if (top.result === 'ok' && top.stdout.trim().length > 0) {
    root = realpathSync(top.stdout.trim());
    const head = await run(['git', 'rev-parse', '--verify', '-q', 'HEAD'], { cwd: root, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS });
    if (head.result === 'ok' && head.stdout.trim().length > 0) {
      const tarPath = path.join(workDir, 'head.tar');
      try {
        const archived = await run(['git', 'archive', '--format=tar', '-o', tarPath, 'HEAD'], { cwd: root, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS });
        if (archived.result !== 'ok') throw new FactsError('snapshot', `the project snapshot could not be archived (git archive exit ${archived.code})`);
        const untar = await run(['tar', '-xf', tarPath, '-C', snapDir], { timeoutMs: GIT_TIMEOUT_MS });
        if (untar.result !== 'ok') throw new FactsError('snapshot', `the project snapshot could not be extracted (tar exit ${untar.code})`);
      } finally {
        rmSync(tarPath, { force: true });
      }
      dropLinks(snapDir);
    }
  }
  // the project may be a subdirectory of the git top level: `./x` claims resolve against IT
  const sub = path.relative(root, project);
  const sessionCwd = path.join(snapDir, sub);
  assertNoLinkSegments(snapDir, sessionCwd);
  mkdirSync(sessionCwd, { recursive: true, mode: 0o700 });
  for (const source of sources) {
    const real = realpathSync(source);
    const inProject = path.relative(project, real);
    if (inProject === '' || inProject.startsWith('..') || path.isAbsolute(inProject)) continue;
    copyNoFollow(real, path.join(sessionCwd, inProject), snapDir);
  }
  sealTree(snapDir);
  return sessionCwd;
}

/**
 * Build the facts sheet (steps 1–3) and write it to `outPath`.
 * @param {BuildFactsOpts} opts
 * @param {import('./spawn.mjs').SessionDeps} [deps]
 * @returns {Promise<{status: string, reason?: string | null, outPath?: string, claims: Claim[], facts?: Fact[], downgraded?: Downgrade[]}>}
 */
export async function buildFacts(opts, deps = {}) {
  const brief = readBriefOnce(opts.briefPath);
  // B50: a command claim written without its CLI gets the project's own (package.json `bin`)
  const briefText = brief.bytes.toString('utf8');
  // prefix; a broken package.json only drops that optional prefix, never the whole sheet.
  let projectBins = [];
  if (opts.projectDir !== undefined) {
    try { projectBins = readProjectBins(opts.projectDir); } catch { projectBins = []; }
  }
  const extractOpts = opts.projectDir === undefined ? {} : { projectBins, briefText };
  const claims = extractClaims([briefText, ...readSourcesText(opts.sources ?? [])].join('\n'), extractOpts);
  const root = opts.runRoot ?? currentRunRoot();
  // B9b.1: a run root inside a git work tree (`tmp.root` configured in the project) cannot hold
  // the snapshot — git and the harness would walk up into the live repo — so the facts area moves
  // to a private B0.1 root under os.tmpdir(); `spawnSession` refuses the cwd either way otherwise.
  const area = gitWorkTreeAncestor(realOrResolved(root)) === null ? path.join(root, 'facts') : path.join(factsTmpRoot(root, { create: true }), 'facts');
  const dir = path.join(area, `${Date.now()}-${randomBytes(4).toString('hex')}`);
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const packetPath = path.join(dir, 'packet.md');
  const snapDir = path.join(dir, 'snapshot');
  try {
    writeFileSync(packetPath, buildFactsPacket(claims), { mode: 0o600 });
    const cwd = opts.projectDir === undefined ? undefined : await buildSnapshot(opts.projectDir, opts.sources ?? [], snapDir, dir);
    const result = await spawnSession(
      {
        cfg: opts.cfg,
        level: 'L0',
        role: 'facts',
        promptPath: packetPath,
        schema: FACTS_SESSION_SCHEMA,
        maxBudgetUsd: opts.maxBudgetUsd ?? FACTS_MAX_BUDGET_USD,
        runRoot: root,
        run: opts.run,
        slug: opts.slug,
        timeoutMs: opts.timeoutMs,
        ...(cwd === undefined ? {} : { cwd }),
      },
      deps,
    );
    if (result.status !== 'ok') return { status: result.status, reason: result.reason ?? null, claims };
    // the delegate's cwd is the project (snapshot): the brief, seen from there
    const briefRel = opts.projectDir === undefined ? undefined : path.relative(realOrResolved(opts.projectDir), realOrResolved(opts.briefPath)).split(path.sep).join('/');
    // validated while the snapshot still exists (it is removed in `finally`)
    const { facts, downgraded } = validateFacts(result.answer, claims, { briefRel, ...(cwd === undefined ? {} : { cwd }) });
    const now = (opts.now ?? (() => new Date()))();
    const rel = path.relative(path.dirname(opts.outPath), opts.briefPath).split(path.sep).join('/');
    const text = renderSheet({
      briefRef: rel,
      briefSha: sha256(brief.bytes),
      briefMtime: brief.mtime,
      builtAt: now.toISOString(),
      facts,
    });
    mkdirSync(path.dirname(opts.outPath), { recursive: true });
    writeFileSync(opts.outPath, text);
    const writeRow = deps.writeRow ?? (opts.slug ? (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: /** @type {string} */ (opts.slug) }) : null);
    if (writeRow) {
      await writeRow({ event: 'facts.built', role: 'facts', run: opts.run ?? null, claims: claims.length, verified: facts.filter((f) => f.tag === 'VERIFIED').length, tokens_in: result.row?.tokens_in ?? null, tokens_out: result.row?.tokens_out ?? null });
    }
    return { status: 'ok', outPath: opts.outPath, claims, facts, downgraded };
  } finally {
    removeSnapshot(snapDir);
    rmSync(dir, { recursive: true, force: true });
  }
}
