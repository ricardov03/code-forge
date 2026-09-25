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
 * Step 3 (deterministic): the answer is validated against `schema/facts.schema.json`; a row whose
 * `command` is not an allowed read-only form ({@link readOnlyViolation}'s allow-list; env claims only
 * `printenv NAME >/dev/null`, their excerpt always blanked) or a VERIFIED row with an empty excerpt
 * refuses the whole sheet; every claim, command, excerpt and reason passes through B0 `redact`. The
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
 * Every claim token of `text`, deduplicated by (kind, token), in order of first appearance.
 * @param {string} text
 * @returns {Claim[]}
 */
export function extractClaims(text) {
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
    found.push({ index: start, token: `${cmd[1]} ${cmd[2]}`, kind: 'command' });
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
 * @returns {{segments: string[][], refused: string | null}}
 */
function splitCommand(command) {
  if (/[`]/.test(command)) return { segments: [], refused: 'backticks are not allowed' };
  if (/\$/.test(command)) return { segments: [], refused: '$ expansion is not allowed' };
  if (/[<>]/.test(command)) return { segments: [], refused: 'redirection is not allowed' };
  /** @type {string[][]} */
  const segments = [[]];
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
      if (end < 0) return { segments: [], refused: 'unbalanced quote' };
      word += command.slice(i + 1, end);
      inWord = true;
      i = end;
    } else if (c === '\\') {
      if (i + 1 >= command.length || command[i + 1] === '\n' || command[i + 1] === '\r') return { segments: [], refused: 'line continuation is not allowed' };
      word += command[i + 1];
      inWord = true;
      i += 1;
    } else if (SEPARATORS.has(c)) {
      flush();
      if ((c === '&' || c === '|') && command[i + 1] === c) i += 1;
      segments.push([]);
    } else if (c === ' ' || c === '\t') {
      flush();
    } else {
      if (GLOB_CHARS.has(c)) return { segments: [], refused: 'unquoted glob characters (* ? [ ] { }) are not allowed' };
      word += c;
      inWord = true;
    }
  }
  flush();
  return { segments: segments.filter((s) => s.length > 0), refused: null };
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
const PLAIN_READERS = new Set(['ls', 'cat', 'head', 'tail', 'wc', 'grep', 'rg', 'stat', 'file', 'uname', 'sw_vers', 'jq', 'which']);

const GIT_READS = new Set(['log', 'show', 'status', 'rev-parse', 'ls-files', 'cat-file', '--version']);
const NPM_READS = new Set(['view', 'ls', '--version']);
const HELP_FORMS = new Set(['--help', '-h', '--version']);
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
/** Build/task runners: their `help`/default targets run user recipes, so the `<cli> --help` case never admits them. */
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
    if (verb === 'rg' && rest.some((t) => t.startsWith('--pre'))) return 'rg --pre runs a command';
    if (verb === 'grep' && rest.some((t) => /^-[a-zA-Z]*[rR]/.test(t) || t === '--recursive' || t === '--dereference-recursive')) return 'grep: -r, -R and --recursive are not allowed';
    return null;
  }
  switch (verb) {
    case 'command':
      return rest.length === 2 && rest[0] === '-v' ? null : 'command: only command -v <name>';
    case 'find':
      return findViolation(rest);
    case 'git':
      if (rest.length === 0 || !GIT_READS.has(rest[0])) return 'git: only log, show, status, rev-parse, ls-files, cat-file or --version, with no global option';
      return rest.some((t) => /^--(output|ext-diff|textconv)/.test(t)) ? 'git: --output, --ext-diff and --textconv are not allowed' : null;
    case 'npm':
      return rest.length > 0 && NPM_READS.has(rest[0]) ? null : 'npm: only view, ls or --version';
    case 'node':
      return rest.length === 1 && (rest[0] === '--version' || rest[0] === '-v') ? null : 'node: only --version';
    case 'test':
      return rest.length === 2 && ['-e', '-f', '-d', '-n'].includes(rest[0]) ? null : 'test: only -e, -f, -d or -n with one operand';
    case 'curl':
      return curlViolation(rest);
    default:
      // `<cli> --help|-h|--version` — exactly two tokens, a bare CLI name (never a path), never a build runner
      if (BUILD_RUNNERS.has(verb)) return `${verb}: build and task runners are not allowed`;
      return rest.length === 1 && HELP_FORMS.has(rest[0]) && /^[a-z0-9][a-z0-9._-]*$/.test(verb) ? null : `not an allowed read-only form: "${verb}"`;
  }
}

/**
 * Why a delegate's `command` is not an allowed read-only command, or null when it is. An
 * ALLOW-LIST (fail closed): every segment between `;`, `&&`, `||`, `|`, `&`, newline or CR must
 * be one of the forms in {@link segmentViolation}; `$`, backticks and redirections are refused.
 * @param {string} command
 * @returns {string | null}
 */
export function readOnlyViolation(command) {
  const { segments, refused } = splitCommand(command);
  if (refused) return refused;
  if (segments.length === 0) return 'empty command';
  for (const argv of segments) {
    const why = segmentViolation(argv);
    if (why) return why;
  }
  return null;
}

/** The one form an env claim may be checked with: existence only, the value never printed. */
const ENV_CHECK = /^printenv ([A-Z][A-Z0-9_]*) >\/dev\/null$/;

/**
 * Validate the delegate's answer (step 3). Claims the delegate left out are kept as UNVERIFIABLE
 * rows ("no answer") — a missing answer is never read as a fact.
 * @param {unknown} answer @param {ReadonlyArray<Claim>} claims
 * @returns {Fact[]} the rows, in claim order.
 * @throws {FactsError} `refused` naming each fact id and reason (never the output excerpt).
 */
export function validateFacts(answer, claims) {
  if (!validateFile(answer)) {
    const where = (validateFile.errors ?? []).map((e) => `${e.instancePath || '/'} ${e.message}`).slice(0, 5);
    throw new FactsError('refused', `facts answer does not match facts.schema.json: ${where.join('; ')}`);
  }
  const rows = /** @type {{facts: Fact[]}} */ (answer).facts;
  const problems = [];
  for (const row of rows) {
    if (row.tag === 'VERIFIED' && row.output_excerpt.trim().length === 0) problems.push(`${row.fact_id}: VERIFIED with an empty output excerpt`);
    if (row.command.trim().length === 0) {
      if (row.tag !== 'UNVERIFIABLE') problems.push(`${row.fact_id}: ${row.tag} without a command`);
      continue;
    }
    if (row.kind === 'env') {
      const env = ENV_CHECK.exec(row.command);
      if (!env || env[1] !== row.claim) problems.push(`${row.fact_id}: an env claim may only be checked with printenv ${row.claim} >/dev/null`);
      continue;
    }
    const why = readOnlyViolation(row.command);
    if (why) problems.push(`${row.fact_id}: command is not read-only: ${why}`);
  }
  if (problems.length > 0) throw new FactsError('refused', `facts sheet refused: ${problems.join('; ')}`);
  /** @type {Fact[]} */
  const out = [];
  claims.forEach((claim, i) => {
    const row = rows.find((r) => r.claim === claim.token && r.kind === claim.kind);
    out.push(
      row
        ? { ...row, fact_id: `F${i + 1}`, claim: redact(row.claim), command: redact(row.command), output_excerpt: row.kind === 'env' ? '' : redact(row.output_excerpt), why: row.why === null ? null : redact(row.why) }
        : { fact_id: `F${i + 1}`, claim: redact(claim.token), kind: claim.kind, command: '', output_excerpt: '', tag: 'UNVERIFIABLE', why: 'no answer from the delegate' },
    );
  });
  return out;
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
  'For each claim below, run the cheapest READ-ONLY check (`<cli> --help | grep -c <flag>`, `which <cli>`, `ls <path>`,',
  '`<cli> --version`, `npm view <name> version`). Quote the exact command and the output line it printed (at most 200',
  'characters). Tag each claim VERIFIED (the output shows it), NOT-FOUND (the check ran and it is absent) or',
  'UNVERIFIABLE (no read-only check can decide it — say why in `why`). Never infer, never guess. Only these forms are',
  'accepted, joined by `|` if needed: which, command -v, ls, cat, head, tail, wc, grep (no -r), rg,',
  'find (only -name -iname -path -ipath -type -maxdepth -mindepth -print -print0 -newer -size -empty -mtime -mmin',
  '-not ! -a -o -and -or, plus paths), stat, file, uname, sw_vers, jq, `<cli> --help|-h|--version` (a bare CLI name, no path, no build runner), git',
  'log|show|status|rev-parse|ls-files|cat-file, npm view|ls, node --version, test -e|-f|-d|-n, `curl -sI <one URL>`.',
  'No `$`, no backticks, no redirection, no unquoted glob, no `~`, no absolute path outside /usr, /opt/homebrew or',
  '/bin, no secret files.',
  'An env claim is checked ONLY with `printenv NAME >/dev/null` (quote its exit status; the value is never shown).',
  '',
  'Reply with ONE JSON object: {"facts": [{"fact_id", "claim", "kind", "command", "output_excerpt", "tag", "why"}]} —',
  'one row per claim, `fact_id`, `claim` and `kind` copied from the list, `why` null unless UNVERIFIABLE.',
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
 * @returns {Promise<{status: string, reason?: string | null, outPath?: string, claims: Claim[], facts?: Fact[]}>}
 */
export async function buildFacts(opts, deps = {}) {
  const brief = readBriefOnce(opts.briefPath);
  const claims = extractClaims([brief.bytes.toString('utf8'), ...readSourcesText(opts.sources ?? [])].join('\n'));
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
    const facts = validateFacts(result.answer, claims);
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
    return { status: 'ok', outPath: opts.outPath, claims, facts };
  } finally {
    removeSnapshot(snapDir);
    rmSync(dir, { recursive: true, force: true });
  }
}
