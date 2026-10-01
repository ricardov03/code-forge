// `code-forge logs` (B27, B28): list, summary, clear, path and report against a fixture log in a
// temp HOME, with a fake `exec` (npm, the AI session's `claude`, gh) and a fake PATH lookup — no
// real npm, model CLI, gh or editor ever runs, and nothing is ever sent.
// One per-file temp parent, removed in `after()`; HOME points into it before any `src` module loads.
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, describe, test } from 'node:test';

const PARENT = mkdtempSync(path.join(os.tmpdir(), 'cf-logs-'));
process.env.HOME = path.join(PARENT, 'home');
mkdirSync(process.env.HOME, { recursive: true });
after(() => rmSync(PARENT, { recursive: true, force: true }));

const { compareVersions, editorCommand, findSecret, issueRepo, runLogs } = await import('../../src/cli/logs.mjs');
const { AI_MAX_BYTES, AI_REPORT_MAX_BYTES, applyItems, readItems, runAiScrub } = await import('../../src/session/scrub.mjs');
const YAML = (await import('yaml')).default;

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const SYSTEM = { version: '0.2.3', node: 'v22.20.0', os: 'darwin 25.2.0 arm64' };
const NOW = () => new Date('2026-10-01T12:00:00.000Z');
const OK = (stdout = '') => ({ result: 'ok', code: 0, signal: null, stdout, stderr: '', timedOut: false });
const FAIL = (code, stderr = '') => ({ result: 'failed', code, signal: null, stdout: '', stderr, timedOut: false });
const NPM_ARGV = ['npm', 'view', '@codedology/code-forge', 'version'];

/** @param {string} text @param {string} needle @returns {number} how many times `needle` is in `text` */
const count = (text, needle) => text.split(needle).length - 1;

/** The JSON a `claude -p --output-format json --json-schema …` run prints (shape only). */
function claudeAnswer(answer) {
  return JSON.stringify({ type: 'result', subtype: 'success', is_error: false, result: JSON.stringify(answer), structured_output: answer });
}

/** @param {object} over */
function entry(over) {
  return {
    ts: '2026-10-01T10:00:00.000Z', version: '0.2.3', node: 'v22.20.0', platform: 'darwin', arch: 'arm64',
    verb: 'keys', sub: 'set', flags: ['--op'], exit: 1, kind: 'op_timeout',
    message: 'keys: 1Password did not answer in time; unlock the app and try again', stack: null,
    cleaned: [{ rule: 'home', count: 2 }, { rule: 'project_path', count: 1 }],
    fp: 'f00000000003',
    ...over,
  };
}

/** Oldest first, as the file holds them. */
const FIXTURE = [
  entry({ ts: '2026-08-01T09:00:00.000Z', verb: 'tools', sub: 'install', flags: ['--yes'], kind: 'error', message: 'old one', cleaned: [], fp: 'f00000000001' }),
  entry({ ts: '2026-09-30T08:00:00.000Z', verb: 'init', sub: null, flags: ['--jev-ref', '--yes'], exit: 2, kind: 'usage', message: 'init: unknown flag "--x"\nusage: code-forge init', cleaned: [{ rule: 'email', count: 1 }, { rule: 'item_id', count: 1 }], fp: 'f00000000002' }),
  entry({ ts: '2026-10-01T10:00:00.000Z' }),
];

let n = 0;
/** @param {object[]|null} entries @returns {string} a fresh HOME holding that log */
function home(entries) {
  n += 1;
  const h = path.join(PARENT, `h${n}`);
  mkdirSync(path.join(h, '.code-forge', 'logs'), { recursive: true });
  if (entries) writeFileSync(path.join(h, '.code-forge', 'logs', 'errors.jsonl'), entries.map((e) => `${JSON.stringify(e)}\n`).join(''));
  return h;
}

function sink() {
  const chunks = [];
  return { write: (s) => (chunks.push(String(s)), true), text: () => chunks.join('') };
}

/**
 * @param {string[]} args @param {string} h
 * @param {{gh?: any, npm?: any, ai?: any, aiResult?: any, onPath?: any, isTTY?: boolean, confirms?: boolean[], selects?: string[], cwd?: string, pkg?: any, env?: object, runEditor?: any}} [o]
 *   `gh(argv)`: the answer for every gh call (default ok, `[]` for a search); `npm`: the npm answer
 *   (default 0.2.3); `ai`: the claude stdout (default no items); `confirms`/`selects`: answers in order.
 */
async function logs(args, h, o = {}) {
  const stdout = sink();
  const stderr = sink();
  const calls = [];
  const sessions = [];
  const npmOpts = [];
  const prompts = [];
  const confirms = [...(o.confirms ?? [])];
  const selects = [...(o.selects ?? [])];
  const ui = {
    confirm: async (q) => (prompts.push({ type: 'confirm', ...q }), confirms.shift() ?? false),
    select: async (q) => (prompts.push({ type: 'select', ...q }), selects.shift() ?? 'cancel'),
    isCancel: () => false,
  };
  const exec = async (argv, opts) => {
    calls.push(argv);
    if (argv[0] === 'npm') {
      npmOpts.push(opts);
      return o.npm ?? OK('0.2.3\n');
    }
    if (argv[0] === 'claude') {
      const cwd = opts.cwd;
      const cwdAtCall = typeof cwd === 'string' && existsSync(cwd) ? { dir: statSync(cwd).isDirectory(), entries: readdirSync(cwd).length } : null;
      sessions.push({ argv, input: Buffer.from(opts.input ?? '').toString('utf8'), cwd, cwdAtCall });
      return o.aiResult ?? OK(o.ai ?? claudeAnswer({ items: [] }));
    }
    if (o.gh) return o.gh(argv);
    return argv[2] === 'list' ? OK('[]') : OK('');
  };
  const code = await runLogs(args, {
    env: { HOME: h, PATH: '', ...(o.env ?? {}) }, cwd: o.cwd ?? h, stdout, stderr, isTTY: o.isTTY ?? false, ui, exec,
    onPath: o.onPath ?? (() => false), now: NOW, system: SYSTEM, ...(o.pkg ? { pkg: o.pkg } : {}), ...(o.runEditor ? { runEditor: o.runEditor } : {}),
  });
  return { code, out: stdout.text(), err: stderr.text(), calls, sessions, prompts, npmOpts };
}

describe('logs, summary, clear, path', () => {
  test('logs: newest first with the fingerprint, default 10; --last; no log', async () => {
    const h = home(FIXTURE);
    const r = await logs([], h);
    assert.equal(r.code, 0);
    assert.equal(
      r.out,
      '2026-10-01T10:00:00.000Z  keys set  1  op_timeout  f00000000003  keys: 1Password did not answer in time; unlock the app and try again\n' +
        '2026-09-30T08:00:00.000Z  init  2  usage  f00000000002  init: unknown flag "--x"\n' +
        '2026-08-01T09:00:00.000Z  tools install  1  error  f00000000001  old one\n',
    );
    const one = await logs(['--last', '1', '--json'], h);
    assert.deepEqual(JSON.parse(one.out), { errors: [FIXTURE[2]] });
    const none = await logs([], home(null));
    assert.deepEqual([none.code, none.out], [0, 'no errors logged\n']);
    const bad = await logs(['--last', '0'], h);
    assert.deepEqual([bad.code, bad.out, bad.err], [2, '', `logs: --last must be a positive integer\n${USAGE}`]);
  });

  test('logs: 11 entries print exactly the newest 10 by default', async () => {
    const eleven = Array.from({ length: 11 }, (_, i) => entry({ ts: `2026-09-${String(i + 1).padStart(2, '0')}T00:00:00.000Z`, message: `m${i + 1}` }));
    const r = await logs([], home(eleven));
    const got = r.out.split('\n').filter((l) => l.length > 0);
    assert.equal(got.length, 10);
    assert.equal(got[0], '2026-09-11T00:00:00.000Z  keys set  1  op_timeout  f00000000003  m11');
    assert.equal(got[9], '2026-09-02T00:00:00.000Z  keys set  1  op_timeout  f00000000003  m2');
  });

  test('summary: one row per fingerprint within the window, "seen N times", sorted by count', async () => {
    const h = home([...FIXTURE, entry({ ts: '2026-10-01T11:00:00.000Z', message: 'keys: 1Password did not answer in 9 s' })]);
    const r = await logs(['summary'], h);
    assert.deepEqual([r.code, r.out], [0, 'errors in the last 30 days:\nf00000000003  keys set  op_timeout  seen 2 times\nf00000000002  init  usage  seen 1 time\n']);
    const all = await logs(['summary', '--days', '90', '--json'], h);
    assert.deepEqual(JSON.parse(all.out), {
      days: 90,
      counts: [
        { fp: 'f00000000003', verb: 'keys', sub: 'set', kind: 'op_timeout', count: 2 },
        { fp: 'f00000000002', verb: 'init', sub: null, kind: 'usage', count: 1 },
        { fp: 'f00000000001', verb: 'tools', sub: 'install', kind: 'error', count: 1 },
      ],
    });
  });

  test('clear: non-TTY without --yes exits 2 and keeps the log; a no keeps it; --yes deletes it', async () => {
    const h = home(FIXTURE);
    const file = path.join(h, '.code-forge', 'logs', 'errors.jsonl');
    const r = await logs(['clear'], h);
    assert.deepEqual([r.code, existsSync(file)], [2, true]);
    const no = await logs(['clear'], h, { isTTY: true, confirms: [false] });
    assert.deepEqual([no.code, no.out, existsSync(file)], [1, 'cancelled — nothing deleted\n', true]);
    assert.deepEqual(no.prompts, [{ type: 'confirm', message: 'Delete the error log (3 entries)?', initialValue: false }]);
    const yes = await logs(['clear', '--yes'], h);
    assert.deepEqual([yes.code, yes.out, existsSync(file)], [0, `deleted ${file}\n`, false]);
  });

  test('path prints the log path; an unknown subcommand exits 2', async () => {
    const h = home(null);
    const r = await logs(['path'], h);
    assert.deepEqual([r.code, r.out], [0, `${path.join(h, '.code-forge', 'logs', 'errors.jsonl')}\n`]);
    assert.equal((await logs(['nope'], h)).code, 2);
  });
});

const USAGE =
  'usage: code-forge logs [--last N] [--json] | logs summary [--days N] [--json] | logs clear [--yes] | logs path\n' +
  '       code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--no-ai] [--allow-old] [--dry-run] [--yes]\n';

const DISCLOSURE =
  'What you will share (public on GitHub): code-forge, Node and OS versions; command names and flag NAMES; exit codes; error types; error messages and crash reports after cleaning.\n' +
  'Never shared: flag values, file contents, your code, keys or tokens, your home folder, project folder or project name.\n';

const TWO_TITLE = '[error report] 2 errors';
const TWO_BODY = `## code-forge error report

### What you were doing

it fails at ~/work

### Versions

- code-forge: 0.2.3
- latest on npm: 0.2.3
- Node: v22.20.0
- OS: darwin 25.2.0 arm64

### Error details

| # | when | command | exit | kind | fingerprint |
|---|---|---|---|---|---|
| 1 | 2026-10-01T10:00:00.000Z | keys set | 1 | op_timeout | f00000000003 |
| 2 | 2026-09-30T08:00:00.000Z | init | 2 | usage | f00000000002 |

#### 1. keys set: op_timeout

- when: 2026-10-01T10:00:00.000Z
- flags: \`--op\`
- exit: 1
- kind: op_timeout
- fingerprint: f00000000003
- code-forge 0.2.3, Node v22.20.0, darwin arm64

message:

\`\`\`text
keys: 1Password did not answer in time; unlock the app and try again
\`\`\`

#### 2. init: usage

- when: 2026-09-30T08:00:00.000Z
- flags: \`--jev-ref\` \`--yes\`
- exit: 2
- kind: usage
- fingerprint: f00000000002
- code-forge 0.2.3, Node v22.20.0, darwin arm64

message:

\`\`\`text
init: unknown flag "--x"
usage: code-forge init
\`\`\`

### Fingerprint

Fingerprint: f00000000003

Other fingerprints: f00000000002

<!-- code-forge-fp: f00000000003 -->
<!-- code-forge-fp: f00000000002 -->
`;

/** Built by hand (not by the code under test): the prefilled issue-form link for the 2-error report. */
const LINK = `https://github.com/ricardov03/code-forge/issues/new?template=error-report.yml&title=${encodeURIComponent(TWO_TITLE)}&labels=error-report&fingerprint=f00000000003&details=${encodeURIComponent(TWO_BODY)}`;
const SEARCH = 'Already reported? Search first:\nhttps://github.com/ricardov03/code-forge/issues?q=f00000000003\n';

const CLEANED = 'Cleaned: 3 home paths, 1 project path, 1 email, 1 1Password item ID.\n';
/** What every 2-error report (with the note) prints before it asks or sends, when the AI found nothing. */
const PREVIEW = `${DISCLOSURE}${CLEANED}AI pass: nothing found.\n\nTitle: ${TWO_TITLE}\n\n${TWO_BODY}\n`;
const SEND_PROMPT = (initialValue, edit = true) => ({
  type: 'select',
  message: 'This will be public on GitHub. Send it?',
  options: [{ value: 'send', label: 'Send' }, ...(edit ? [{ value: 'edit', label: 'Edit in my editor' }] : []), { value: 'cancel', label: 'Cancel' }],
  initialValue,
});

describe('logs report', () => {
  test('--dry-run: disclosure, cleaning counts, the AI line, the exact title and body; npm + 1 AI session, 0 gh calls', async () => {
    const h = home(FIXTURE);
    const r = await logs(['report', '--last', '2', '--note', `it fails at ${h}/work`, '--dry-run'], h, { onPath: () => true });
    assert.equal(r.code, 0);
    assert.deepEqual(r.calls.map((c) => c[0]), ['npm', 'claude']);
    assert.deepEqual(r.calls[0], NPM_ARGV);
    assert.deepEqual(r.npmOpts.map((x) => x.timeoutMs), [10_000]);
    assert.equal(r.err, '');
    assert.equal(r.out, `${PREVIEW}dry run: not sent\n`);
  });

  test('one verb + kind gives a named title; --kind and --verb filter', async () => {
    const h = home(FIXTURE);
    const r = await logs(['report', '--verb', 'keys', '--dry-run'], h);
    assert.equal(count(r.out, '\nTitle: [error report] keys op_timeout\n'), 1);
    const none = await logs(['report', '--kind', 'crash', '--dry-run'], h);
    assert.deepEqual([none.code, none.out, none.calls.length], [0, 'no matching errors to report\n', 0]);
  });

  test('non-TTY without --yes prints, exits 2, 0 gh calls; Cancel at the prompt: "not sent", exit 1', async () => {
    const h = home(FIXTURE);
    const args = ['report', '--last', '2', '--note', `it fails at ${h}/work`];
    const r = await logs(args, h, { onPath: () => true });
    assert.deepEqual([r.code, r.calls.filter((c) => c[0] === 'gh').length], [2, 0]);
    assert.equal(r.out, PREVIEW);
    assert.equal(r.err, 'logs: no terminal to confirm; not sent — pass --yes to send the report above\n');
    const no = await logs(args, h, { onPath: () => true, isTTY: true, selects: ['cancel'] });
    assert.deepEqual([no.code, no.calls.filter((c) => c[0] === 'gh').length, no.out, no.err], [1, 0, `${PREVIEW}not sent\n`, '']);
    assert.deepEqual(no.prompts, [SEND_PROMPT('send')]);
  });

  test('gh path: search finds nothing, create with the error-report and kind labels, label retry once, removes the body file', async () => {
    const h = home(FIXTURE);
    let bodyFile = '';
    let bodyText = '';
    const gh = (argv) => {
      if (argv[1] === 'auth') return OK();
      if (argv[2] === 'list') return OK('[]\n');
      bodyFile = argv[argv.indexOf('--body-file') + 1];
      bodyText = readFileSync(bodyFile, 'utf8');
      return argv.includes('--label') ? FAIL(1, "could not add label: 'kind:op_timeout' not found") : OK('Creating issue\nhttps://github.com/ricardov03/code-forge/issues/42\n');
    };
    const r = await logs(['report', '--last', '2', '--note', `it fails at ${h}/work`, '--yes'], h, { gh, onPath: (c) => c === 'gh' });
    assert.equal(r.code, 0);
    const base = ['gh', 'issue', 'create', '--repo', 'ricardov03/code-forge', '--title', TWO_TITLE, '--body-file', bodyFile];
    assert.deepEqual(r.calls.filter((c) => c[0] === 'gh'), [
      ['gh', 'auth', 'status'],
      ['gh', 'issue', 'list', '--repo', 'ricardov03/code-forge', '--state', 'all', '--search', 'f00000000003 in:body', '--json', 'number,url,state', '--limit', '5'],
      [...base, '--label', 'error-report', '--label', 'kind:op_timeout', '--label', 'kind:usage'],
      base,
    ]);
    assert.equal(bodyText, TWO_BODY);
    assert.equal(existsSync(bodyFile), false);
    assert.equal(r.out, `${PREVIEW}sent: https://github.com/ricardov03/code-forge/issues/42\n`);
    assert.equal(r.err, '');
  });

  test('gh create failing for another reason: no retry, falls back to the links and the saved report, exit 0', async () => {
    const h = home(FIXTURE);
    const gh = (argv) => (argv[1] === 'auth' ? OK() : argv[2] === 'list' ? OK('[]') : { ...FAIL(4, 'HTTP 502 gh-err-detail'), stdout: 'gh-out-detail' });
    const r = await logs(['report', '--last', '2', '--note', `it fails at ${h}/work`, '--yes'], h, { gh, onPath: () => true });
    assert.deepEqual([r.code, r.calls.filter((c) => c[0] === 'gh').length, r.err], [0, 3, '']);
    assert.equal(`${r.out}${r.err}`.split('gh-err-detail').length - 1, 0);
    assert.equal(`${r.out}${r.err}`.split('gh-out-detail').length - 1, 0);
    const saved = path.join(h, '.code-forge', 'logs', 'report-2026-10-01T12-00-00-000Z.md');
    assert.equal(r.out, `${PREVIEW}gh failed (exit 4); here is a link instead\nFull report saved at ${saved}\n${SEARCH}Open this link to file the issue:\n${LINK}\n`);
    assert.equal(readFileSync(saved, 'utf8'), TWO_BODY);
  });

  test('gh on PATH but not signed in: 1 gh call (auth status), the search link and the form link, exit 0', async () => {
    const h = home(FIXTURE);
    const r = await logs(['report', '--last', '2', '--note', `it fails at ${h}/work`, '--yes'], h, { gh: () => FAIL(1, 'not logged in'), onPath: () => true });
    assert.deepEqual([r.code, r.calls.filter((c) => c[0] === 'gh'), r.err], [0, [['gh', 'auth', 'status']], '']);
    assert.equal(r.out, `${PREVIEW}${SEARCH}Open this link to file the issue:\n${LINK}\n`);
  });

  test('the project folder under HOME becomes <project> in the report', async () => {
    const h = home(FIXTURE);
    const cwd = path.join(h, 'proj');
    mkdirSync(cwd);
    const r = await logs(['report', '--verb', 'tools', '--note', `it fails in ${cwd}/src`, '--dry-run'], h, { cwd });
    assert.equal(r.code, 0);
    assert.equal(count(r.out, '\n### What you were doing\n\nit fails in <project>/src\n'), 1);
    assert.equal(r.out.split(h).length - 1, 0);
    assert.equal(count(r.out, `${DISCLOSURE}Cleaned: 1 project path.\n`), 1);
  });

  test('issueRepo reads bugs.url, then repository, with or without a query or fragment', () => {
    assert.equal(issueRepo({ bugs: { url: 'https://github.com/acme/tool/issues?q=1' } }), 'acme/tool');
    assert.equal(issueRepo({ bugs: 'https://github.com/acme/tool#readme' }), 'acme/tool');
    assert.equal(issueRepo({ repository: { url: 'git+https://github.com/acme/tool.git#main' } }), 'acme/tool');
    assert.equal(issueRepo({ bugs: { url: 'https://example.com/x' }, repository: 'git@github.com:acme/other.git' }), 'acme/other');
    assert.equal(issueRepo({ name: 'x' }), null);
    assert.equal(issueRepo(), 'ricardov03/code-forge');
  });

  test('no GitHub repository in package.json: exit 1, nothing printed or run', async () => {
    const h = home(FIXTURE);
    const r = await logs(['report', '--yes'], h, { onPath: () => true, pkg: { name: 'x', repository: 'https://example.com/x.git' } });
    assert.deepEqual([r.code, r.out, r.err, r.calls.length], [1, '', 'logs: package.json names no GitHub repository to report to\n', 0]);
  });

  test('no gh: the search link and the exact prefilled issue-form link, no gh call', async () => {
    const h = home(FIXTURE);
    const r = await logs(['report', '--last', '2', '--note', `it fails at ${h}/work`, '--yes'], h);
    assert.equal(r.code, 0);
    assert.equal(r.calls.filter((c) => c[0] === 'gh').length, 0);
    const lines = r.out.split('\n');
    assert.deepEqual(lines.slice(-5, -2), ['Already reported? Search first:', 'https://github.com/ricardov03/code-forge/issues?q=f00000000003', 'Open this link to file the issue:']);
    const url = new URL(lines.at(-2));
    assert.equal(`${url.origin}${url.pathname}`, 'https://github.com/ricardov03/code-forge/issues/new');
    assert.deepEqual([...url.searchParams.keys()], ['template', 'title', 'labels', 'fingerprint', 'details']);
    assert.deepEqual([...url.searchParams.values()], ['error-report.yml', TWO_TITLE, 'error-report', 'f00000000003', TWO_BODY]);
  });

  test('a long body is cut to fit 7,500 characters, keeps its fingerprint section, and is saved in full', async () => {
    const h = home(FIXTURE);
    const note = 'the same words again and again '.repeat(400);
    const r = await logs(['report', '--note', note, '--yes'], h);
    assert.equal(r.code, 0);
    const saved = path.join(h, '.code-forge', 'logs', 'report-2026-10-01T12-00-00-000Z.md');
    const link = r.out.split('\n').at(-2);
    assert.equal(link.length <= 7500, true);
    assert.equal(link.length > 7400, true);
    const body = new URL(link).searchParams.get('details');
    const footer = '\n### Fingerprint\n\nFingerprint: f00000000003\n\nOther fingerprints: f00000000002, f00000000001\n\n<!-- code-forge-fp: f00000000003 -->\n<!-- code-forge-fp: f00000000002 -->\n<!-- code-forge-fp: f00000000001 -->\n';
    assert.equal(body.endsWith(`\n\n(report cut; full report saved at ~/.code-forge/logs/report-2026-10-01T12-00-00-000Z.md)\n${footer}`), true);
    const full = readFileSync(saved, 'utf8');
    assert.equal(count(full, note), 1);
    assert.equal(full.startsWith(body.split('\n\n(report cut;')[0]), true);
    assert.equal(count(r.out, `Full report saved at ${saved}\n`), 1);
  });

  const SECRETS = [
    ['sk-ant-FAKE0123456789abcdef', 'API key (sk-)'],
    ['ghp_FAKE0123456789abcdefghij', 'GitHub token'],
    ['github_pat_FAKE0123456789_abcdefghij', 'GitHub token'],
    ['xoxb-FAKE-0123456789-abc', 'Slack token'],
    ['AKIAFAKE012345678901', 'AWS access key'],
    ['-----BEGIN RSA PRIVATE KEY----- FAKE', 'private key'],
    ['eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJGQUtFIn0.FAKEsignature0123', 'JWT'],
    ['FAKEq8Zr3LmN0pWx7Yt2Vb5Kc9Hd4Js6Ga1Ue', 'high-entropy string'],
  ];
  for (const [secret, rule] of SECRETS) {
    test(`${rule} left in the report blocks it: exit 1, 0 AI sessions, 0 gh calls, the text printed 0 times`, async () => {
      const h = home([entry({ message: `failed with ${secret}` })]);
      const r = await logs(['report', '--yes'], h, { onPath: () => true });
      assert.deepEqual([r.code, r.calls], [1, [NPM_ARGV]]);
      assert.equal(`${r.out}${r.err}`.split(secret).length - 1, 0);
      assert.equal(r.out, DISCLOSURE);
      assert.equal(r.err, `Possible secret found in the report (${rule}, report line 33; the title is line 1); not sent. Find it with code-forge logs --json and remove it with code-forge logs clear.\n`);
    });
  }

  test('a two-line note moves the reported line by 1; the same text checked twice gives the same hit', async () => {
    const secret = 'ghp_FAKE0123456789abcdefghij';
    const h = home([entry({ message: `failed with ${secret}` })]);
    const r = await logs(['report', '--note', 'see\nbelow', '--yes'], h, { onPath: () => true });
    assert.equal(r.code, 1);
    assert.equal(r.err, 'Possible secret found in the report (GitHub token, report line 34; the title is line 1); not sent. Find it with code-forge logs --json and remove it with code-forge logs clear.\n');
    const text = `title\nok\nkey ${secret}`;
    assert.deepEqual([findSecret(text), findSecret(text)], [{ rule: 'GitHub token', line: 3 }, { rule: 'GitHub token', line: 3 }]);
  });
});

describe('logs report: AI cleaning pass (B28)', () => {
  const MSG = 'keys: cannot reach db.internal.acme.io for acme (mail jane@acme.io) in HOMEDIR/src';

  test('two overlapping items are replaced longest first; a missing item is ignored; counts only, never the text', async () => {
    const h = home(null);
    writeFileSync(path.join(h, '.code-forge', 'logs', 'errors.jsonl'), `${JSON.stringify(entry({ message: MSG.replace('HOMEDIR', h) }))}\n`);
    const ai = claudeAnswer({
      items: [
        { text: 'acme', kind: 'company', reason: 'a company name' },
        { text: 'db.internal.acme.io', kind: 'host', reason: 'an internal host' },
        { text: 'not-in-the-report', kind: 'person', reason: 'x' },
      ],
    });
    const r = await logs(['report', '--dry-run'], h, { ai });
    assert.equal(r.code, 0);
    assert.equal(count(r.out, '\nkeys: cannot reach <host> for <company> (mail <email>) in ~/src\n'), 1);
    assert.equal(count(r.out, '\nAI pass: 1 company, 1 host.\n'), 1);
    assert.equal(r.out.split('acme').length - 1, 0);
    assert.equal(r.out.split('not-in-the-report').length - 1, 0);
  });

  test('the AI packet is closed-book (L1, no tools, stdin) and never holds a string the built-in scrub removed', async () => {
    const h = home(null);
    writeFileSync(path.join(h, '.code-forge', 'logs', 'errors.jsonl'), `${JSON.stringify(entry({ message: MSG.replace('HOMEDIR', h) }))}\n`);
    const r = await logs(['report', '--dry-run'], h);
    assert.equal(r.sessions.length, 1);
    const [s] = r.sessions;
    assert.deepEqual(s.argv.slice(0, 4), ['claude', '-p', '--model', 'claude-sonnet-5']);
    assert.deepEqual(s.argv.slice(s.argv.indexOf('--tools'), s.argv.indexOf('--tools') + 2), ['--tools', '']);
    assert.equal(count(s.argv.join(' '), 'jane'), 0);
    assert.equal(s.input.split('jane@acme.io').length - 1, 0);
    assert.equal(s.input.split(h).length - 1, 0);
    const report = s.input.slice(s.input.indexOf('\n## Report\n'));
    assert.equal(report.split('<email>').length - 1, 1);
    assert.equal(count(s.input, 'keys: cannot reach db.internal.acme.io for acme (mail <email>) in ~/src'), 1);
    // the cwd: a fresh, empty directory of its own (not HOME, not the project), gone afterwards
    assert.equal(typeof s.cwd, 'string');
    assert.deepEqual(s.cwdAtCall, { dir: true, entries: 0 });
    assert.notEqual(s.cwd, h);
    assert.notEqual(path.resolve(s.cwd), path.resolve(h));
    assert.equal(existsSync(s.cwd), false);
  });

  test('a report over 16 KB is cut first: the AI packet is at most 16 KB and the body says so', async () => {
    const h = home(FIXTURE);
    const note = 'x'.repeat(20_000);
    const r = await logs(['report', '--last', '1', '--note', note, '--dry-run'], h);
    assert.equal(r.code, 0);
    assert.equal(r.sessions.length, 1);
    assert.equal(Buffer.byteLength(r.sessions[0].input) <= AI_MAX_BYTES, true);
    assert.equal(Buffer.byteLength(r.sessions[0].input) > AI_MAX_BYTES - 200, true);
    assert.equal(AI_REPORT_MAX_BYTES < AI_MAX_BYTES, true);
    assert.equal(count(r.out, `\n\n(report cut to ${AI_REPORT_MAX_BYTES} bytes)\n`), 1);
    assert.equal(count(r.sessions[0].input, `(report cut to ${AI_REPORT_MAX_BYTES} bytes)`), 1);
    const noAi = await logs(['report', '--last', '1', '--note', note, '--no-ai', '--dry-run'], h);
    assert.equal(count(noAi.out, `\n\n(report cut to ${AI_REPORT_MAX_BYTES} bytes)\n`), 1);
    assert.equal(count(noAi.out, 'the most the AI'), 0);
  });

  for (const [what, result, reason] of [
    ['claude missing', { result: 'failed', code: null, signal: null, stdout: '', stderr: '', timedOut: false, error: 'spawn claude ENOENT' }, 'the model CLI is not installed'],
    ['a non-zero exit', FAIL(1, 'boom'), 'the session failed'],
    ['a timeout', { ...FAIL(null), signal: 'SIGKILL', timedOut: true }, 'it timed out'],
  ]) {
    test(`AI unavailable (${what}): the notice and the extra default-no yes`, async () => {
      const h = home(FIXTURE);
      const r = await logs(['report', '--last', '1'], h, { aiResult: result, isTTY: true, selects: ['send'], confirms: [false] });
      assert.equal(r.code, 1);
      assert.equal(r.sessions.length, 1);
      assert.equal(count(r.out, `\nAI cleaning was not available (${reason}); only the built-in cleaning ran.\n`), 1);
      assert.deepEqual(r.prompts, [SEND_PROMPT('cancel'), { type: 'confirm', message: 'Send without the AI check?', initialValue: false }]);
    });
  }

  test('applyItems protects only the tool\'s own placeholders; a range holding one is replaced whole; exact counts', () => {
    const angle = applyItems(['John Doe <jdoe@acme-corp.com> and Map<AcmeBillingClient>'], [
      { text: 'John Doe', kind: 'person' },
      { text: 'jdoe@acme-corp.com', kind: 'account' },
      { text: 'AcmeBillingClient', kind: 'company' },
    ]);
    assert.deepEqual(angle, { texts: ['<person> <<account>> and Map<<company>>'], counts: [{ kind: 'person', count: 1 }, { kind: 'company', count: 1 }, { kind: 'account', count: 1 }] });
    const whole = applyItems(['host acme-<project>-prod up'], [{ text: 'acme-<project>-prod', kind: 'host' }, { text: 'project', kind: 'project' }]);
    assert.deepEqual(whole, { texts: ['host <host> up'], counts: [{ kind: 'host', count: 1 }] });
    const guarded = applyItems(['see <path> and path and <email> here', 'path'], [{ text: 'path', kind: 'path' }, { text: 'mail> he', kind: 'other' }]);
    assert.deepEqual(guarded, { texts: ['see <path> and <path> and <email> here', '<path>'], counts: [{ kind: 'path', count: 2 }] });
    const nested = applyItems(['a db.acme.io b acme'], [{ text: 'acme', kind: 'company' }, { text: 'db.acme.io', kind: 'host' }]);
    assert.deepEqual(nested, { texts: ['a <host> b <company>'], counts: [{ kind: 'company', count: 1 }, { kind: 'host', count: 1 }] });
    // an item that is exactly one of our placeholders changes nothing and counts nothing
    const exact = applyItems(['mail <email> sent'], [{ text: '<email>', kind: 'account' }]);
    assert.deepEqual(exact, { texts: ['mail <email> sent'], counts: [] });
  });

  test('readItems drops blank and too-short texts and maps an unknown kind to other', () => {
    assert.deepEqual(
      readItems({ items: [{ text: '   ', kind: 'host' }, { text: '\t\n ', kind: 'host' }, { text: 'ab', kind: 'host' }, { text: 'acme', kind: 'brand' }, { text: 'db.local', kind: 'host' }] }),
      [{ text: 'acme', kind: 'other' }, { text: 'db.local', kind: 'host' }],
    );
    assert.equal(readItems({ nope: [] }), null);
    // kinds are matched case-insensitively
    assert.deepEqual(readItems({ items: [{ text: 'db.local', kind: 'Host' }, { text: 'https://x.acme', kind: 'URL' }] }), [{ text: 'db.local', kind: 'host' }, { text: 'https://x.acme', kind: 'url' }]);
  });

  test('runAiScrub refuses a packet over 16 KB itself: 0 sessions', async () => {
    const calls = [];
    const res = await runAiScrub({ report: 'y'.repeat(AI_MAX_BYTES), cwd: PARENT }, { exec: /** @type {any} */ (async (argv) => (calls.push(argv), OK())) });
    assert.deepEqual([res, calls.length], [{ ok: false, reason: 'the report is over 16 KB' }, 0]);
  });

  test('a project L1 that does not resolve uses the shipped defaults only; an unreadable config gives a fixed reason', async () => {
    const h = home(FIXTURE);
    const proj = path.join(h, 'p1');
    mkdirSync(proj);
    writeFileSync(path.join(proj, '.code-forge.yml'), 'provider: anthropic\nlevels:\n  L1:\n    effort: high\n');
    const r = await logs(['report', '--last', '1', '--dry-run'], h, { cwd: proj });
    assert.deepEqual(r.sessions[0].argv.slice(0, 5), ['claude', '-p', '--model', 'claude-sonnet-5', '--safe-mode']);
    const bad = path.join(h, 'p2');
    mkdirSync(bad);
    writeFileSync(path.join(bad, '.code-forge.yml'), 'levels: [unclosed\n  secret-ish: FAKE\n');
    const b = await logs(['report', '--last', '1', '--dry-run'], h, { cwd: bad });
    assert.equal(b.sessions.length, 0);
    assert.equal(count(b.out, '\nAI cleaning was not available (the project config could not be read); only the built-in cleaning ran.\n'), 1);
    assert.equal(count(b.out, 'unclosed'), 0);
  });

  test('bad JSON: the unavailable line, Cancel is the default, and Send needs an extra default-no yes', async () => {
    const h = home(FIXTURE);
    const ai = JSON.stringify({ type: 'result', is_error: false, result: 'sure, here you go' });
    const line = 'AI cleaning was not available (its answer was not valid JSON); only the built-in cleaning ran.\n';
    const r = await logs(['report', '--last', '1'], h, { ai, isTTY: true, selects: ['send'], confirms: [false] });
    assert.equal(r.code, 1);
    assert.equal(count(r.out, `Cleaned: 2 home paths, 1 project path.\n${line}\nTitle:`), 1);
    assert.equal(r.out.endsWith('not sent\n'), true);
    assert.deepEqual(r.prompts, [SEND_PROMPT('cancel'), { type: 'confirm', message: 'Send without the AI check?', initialValue: false }]);
    const yes = await logs(['report', '--last', '1', '--yes'], h, { ai });
    assert.deepEqual([yes.code, yes.prompts], [1, []]);
    assert.equal(yes.err, 'logs: the AI check did not run and there is no terminal to confirm; not sent — pass --no-ai with --yes to send with only the built-in cleaning\n');
    assert.equal(count(yes.out, 'Open this link'), 0);
  });

  test('AI unavailable + --yes in a terminal: the extra default-no question is asked; yes sends, no does not', async () => {
    const h = home(FIXTURE);
    const ai = 'not json at all';
    const extra = { type: 'confirm', message: 'Send without the AI check?', initialValue: false };
    const no = await logs(['report', '--last', '1', '--yes'], h, { ai, isTTY: true, confirms: [false] });
    assert.deepEqual([no.code, no.prompts, no.out.endsWith('not sent\n'), count(no.out, 'Open this link')], [1, [extra], true, 0]);
    const go = await logs(['report', '--last', '1', '--yes'], h, { ai, isTTY: true, confirms: [true] });
    assert.deepEqual([go.code, go.prompts, count(go.out, 'Open this link to file the issue:')], [0, [extra], 1]);
    const noAi = await logs(['report', '--last', '1', '--yes', '--no-ai'], h, { isTTY: true });
    assert.deepEqual([noAi.code, noAi.prompts, noAi.sessions.length], [0, [], 0]);
  });

  test('--no-ai: 0 AI sessions, the skipped line; with --yes it sends (link)', async () => {
    const h = home(FIXTURE);
    const r = await logs(['report', '--last', '1', '--no-ai', '--yes'], h);
    assert.equal(r.code, 0);
    assert.equal(r.sessions.length, 0);
    assert.deepEqual(r.calls, [NPM_ARGV]);
    assert.equal(count(r.out, '\nAI cleaning was skipped (--no-ai); only the built-in cleaning ran.\n\nTitle:'), 1);
    assert.equal(count(r.out, 'Open this link to file the issue:'), 1);
  });
});

describe('logs report: duplicates, version check, edit (B28)', () => {
  const FOUND = OK(JSON.stringify([{ number: 7, state: 'OPEN', url: 'https://github.com/ricardov03/code-forge/issues/7' }]));

  test('an earlier issue with the fingerprint: --yes adds a "happened again" comment with the exact argv; no new issue', async () => {
    const h = home([...FIXTURE, entry({ ts: '2026-10-01T11:00:00.000Z' })]);
    let commentFile = '';
    let commentText = '';
    const gh = (argv) => {
      if (argv[2] === 'list') return FOUND;
      if (argv[2] === 'comment') {
        commentFile = argv.at(-1);
        commentText = readFileSync(commentFile, 'utf8');
      }
      return OK();
    };
    const r = await logs(['report', '--last', '1', '--yes'], h, { gh, onPath: () => true });
    assert.equal(r.code, 0);
    assert.deepEqual(r.calls.filter((c) => c[0] === 'gh').slice(2), [['gh', 'issue', 'comment', '7', '--repo', 'ricardov03/code-forge', '--body-file', commentFile]]);
    assert.equal(
      commentText,
      'This error happened again.\n\n- code-forge: 0.2.3\n- latest on npm: 0.2.3\n- Node: v22.20.0\n- OS: darwin 25.2.0 arm64\n- times in the local log: 2\n\n<!-- code-forge-fp: f00000000003 -->\n',
    );
    assert.equal(existsSync(commentFile), false);
    assert.equal(r.out.endsWith('This error was already reported: https://github.com/ricardov03/code-forge/issues/7 (open)\ncommented: https://github.com/ricardov03/code-forge/issues/7\n'), true);
  });

  test('the search failing: a note, then a new issue is created', async () => {
    const h = home(FIXTURE);
    const gh = (argv) => (argv[2] === 'list' ? { ...FAIL(4, 'HTTP 502 gh-err-detail'), stdout: '' } : argv[2] === 'create' ? OK('https://github.com/ricardov03/code-forge/issues/9\n') : OK());
    const r = await logs(['report', '--last', '1', '--yes'], h, { gh, onPath: () => true });
    assert.equal(r.code, 0);
    assert.deepEqual(r.calls.filter((c) => c[0] === 'gh').map((c) => c[2]), ['status', 'list', 'create']);
    assert.equal(r.out.endsWith('could not search for an existing report (gh exit 4); creating a new one\nsent: https://github.com/ricardov03/code-forge/issues/9\n'), true);
    assert.equal(count(r.out + r.err, 'gh-err-detail'), 0);
  });

  test('"Create a new issue anyway" creates it; the choice defaults to the comment', async () => {
    const h = home(FIXTURE);
    const gh = (argv) => (argv[2] === 'list' ? FOUND : argv[2] === 'create' ? OK('https://github.com/ricardov03/code-forge/issues/8\n') : OK());
    const r = await logs(['report', '--last', '1'], h, { gh, onPath: () => true, isTTY: true, selects: ['send', 'new'] });
    assert.equal(r.code, 0);
    assert.deepEqual(r.calls.filter((c) => c[0] === 'gh').map((c) => c[2]), ['status', 'list', 'create']);
    assert.deepEqual(r.prompts[1], {
      type: 'select',
      message: 'What now?',
      options: [
        { value: 'comment', label: 'Add a comment "happened again"' },
        { value: 'new', label: 'Create a new issue anyway' },
        { value: 'cancel', label: 'Cancel' },
      ],
      initialValue: 'comment',
    });
    assert.equal(r.out.endsWith('sent: https://github.com/ricardov03/code-forge/issues/8\n'), true);
  });

  test('a newer version on npm: the warning and a default-no question; no → not sent, 0 AI sessions', async () => {
    const h = home(FIXTURE);
    const r = await logs(['report', '--last', '1'], h, { npm: OK('0.3.0\n'), isTTY: true, confirms: [false] });
    assert.equal(r.code, 1);
    assert.equal(r.out, 'You use 0.2.3. 0.3.0 is out. Many errors are fixed in newer versions: upgrade with npm install -g @codedology/code-forge@latest and try again first.\nnot sent\n');
    assert.deepEqual(r.prompts, [{ type: 'confirm', message: 'Report anyway?', initialValue: false }]);
    assert.equal(r.sessions.length, 0);
    const dry = await logs(['report', '--last', '1', '--dry-run'], h, { npm: OK('0.3.0\n') });
    assert.equal(count(dry.out, '\n- latest on npm: 0.3.0\n'), 1);
  });

  test('an older version without a terminal: the warning and exit 2 unless --allow-old; --yes in a terminal still asks', async () => {
    const h = home(FIXTURE);
    const warn = 'You use 0.2.3. 0.3.0 is out. Many errors are fixed in newer versions: upgrade with npm install -g @codedology/code-forge@latest and try again first.\n';
    const r = await logs(['report', '--last', '1', '--yes'], h, { npm: OK('0.3.0\n') });
    assert.deepEqual([r.code, r.out, r.sessions.length], [2, warn, 0]);
    assert.equal(r.err, 'logs: an older code-forge than the one on npm; not sent — upgrade first, or pass --allow-old to report from this version\n');
    const tty = await logs(['report', '--last', '1', '--yes'], h, { npm: OK('0.3.0\n'), isTTY: true, confirms: [false] });
    assert.deepEqual([tty.code, tty.out], [1, `${warn}not sent\n`]);
    assert.deepEqual(tty.prompts, [{ type: 'confirm', message: 'Report anyway?', initialValue: false }]);
    const allowed = await logs(['report', '--last', '1', '--yes', '--allow-old'], h, { npm: OK('0.3.0\n') });
    assert.equal(allowed.code, 0);
    assert.deepEqual(allowed.prompts, []);
    assert.equal(count(allowed.out, 'Open this link to file the issue:'), 1);
  });

  test('compareVersions: numbers, then a pre-release is older than its release', () => {
    assert.deepEqual(
      [['0.3.0', '0.2.9'], ['0.2.3', '0.2.3'], ['1.2.0-beta.1', '1.2.0'], ['1.2.0', '1.2.0-rc.1'], ['1.2.0-beta.2', '1.2.0-beta.10'], ['1.2.0-alpha', '1.2.0-beta'], ['1.2.0-1', '1.2.0-alpha'], ['1.0.0+build.5', '1.0.0'], ['x', '1.0.0']].map(([a, b]) => {
        const c = compareVersions(a, b);
        return c === null ? null : Math.sign(c);
      }),
      [1, 0, -1, 1, -1, -1, -1, 0, null],
    );
  });

  test('npm failing: a note, and the report goes on', async () => {
    const h = home(FIXTURE);
    const r = await logs(['report', '--last', '1', '--dry-run'], h, { npm: { ...FAIL(null), timedOut: true } });
    assert.equal(r.code, 0);
    assert.equal(r.out.startsWith(`Could not check npm for a newer code-forge (timed out); going on.\n${DISCLOSURE}`), true);
    assert.equal(count(r.out, '\n- latest on npm: unknown\n'), 1);
    assert.equal(r.out.endsWith('dry run: not sent\n'), true);
  });

  test('edit: the editor adds a token → the secret check blocks it; the token is printed 0 times', async () => {
    const h = home(FIXTURE);
    const token = 'ghp_FAKE0123456789abcdefghij';
    const seen = [];
    const runEditor = async (argv) => {
      seen.push(argv);
      writeFileSync(argv[1], `${readFileSync(argv[1], 'utf8')}\nmy token ${token}\n`);
      return { code: 0 };
    };
    const r = await logs(['report', '--last', '1'], h, { isTTY: true, selects: ['edit'], runEditor, env: { EDITOR: 'vim' } });
    assert.equal(r.code, 1);
    assert.deepEqual([seen.length, seen[0][0], path.basename(seen[0][1])], [1, 'vim', 'report.md']);
    assert.equal(existsSync(seen[0][1]), false);
    assert.equal(`${r.out}${r.err}`.split(token).length - 1, 0);
    assert.equal(r.err, 'Possible secret found in the edited report (GitHub token, report line 42; the title is line 1); not sent.\n');
  });

  test('edit: an edit is scrubbed again and shown; then only Send or Cancel', async () => {
    const h = home(FIXTURE);
    const runEditor = async (argv) => (writeFileSync(argv[1], `it broke in ${h}/x\n`), { code: 0 });
    const r = await logs(['report', '--last', '1'], h, { isTTY: true, selects: ['edit', 'cancel'], runEditor, env: { VISUAL: '/usr/bin/vi' } });
    assert.equal(r.code, 1);
    const footer = '\n### Fingerprint\n\nFingerprint: f00000000003\n\n<!-- code-forge-fp: f00000000003 -->\n';
    assert.equal(r.out.endsWith(`the fingerprint section was added back\nTitle: [error report] keys op_timeout\n\nit broke in ~/x\n${footer}\nnot sent\n`), true);
    assert.equal(count(r.out, 'the fingerprint section was added back\n'), 1);
    assert.deepEqual(r.prompts.map((p) => p.options.length), [3, 2]);
  });

  test('VISUAL wins over EDITOR; VISUAL with flags falls back to EDITOR (argv recorded)', async () => {
    const h = home(FIXTURE);
    const seen = [];
    const runEditor = async (argv) => (seen.push(argv[0]), { code: 0 });
    await logs(['report', '--last', '1'], h, { isTTY: true, selects: ['edit', 'cancel'], runEditor, env: { VISUAL: '/opt/ed/bin/edit', EDITOR: 'vim' } });
    await logs(['report', '--last', '1'], h, { isTTY: true, selects: ['edit', 'cancel'], runEditor, env: { VISUAL: 'code --wait', EDITOR: 'vi' } });
    assert.deepEqual(seen, ['/opt/ed/bin/edit', 'vi']);
  });

  test('an edited body ending in blank lines keeps its fingerprint section when the link is cut', async () => {
    const h = home(FIXTURE);
    const runEditor = async (argv) => {
      const text = readFileSync(argv[1], 'utf8');
      writeFileSync(argv[1], `${'more words here '.repeat(600)}\n${text}\n\n`);
      return { code: 0 };
    };
    const r = await logs(['report', '--last', '1'], h, { isTTY: true, selects: ['edit', 'send'], runEditor, env: { EDITOR: 'vim' } });
    assert.equal(r.code, 0);
    const details = new URL(r.out.split('\n').at(-2)).searchParams.get('details');
    assert.equal(details.endsWith('\n\n### Fingerprint\n\nFingerprint: f00000000003\n\n<!-- code-forge-fp: f00000000003 -->\n'), true);
    assert.equal(count(details, '<!-- code-forge-fp: f00000000003 -->'), 1);
    assert.equal(count(details, '(report cut; full report saved at ~/.code-forge/logs/report-2026-10-01T12-00-00-000Z.md)'), 1);
  });

  test('VISUAL and EDITOR both with flags: the refusal text, no editor run', async () => {
    const h = home(FIXTURE);
    let ran = 0;
    const r = await logs(['report', '--last', '1'], h, { isTTY: true, selects: ['edit', 'cancel'], runEditor: async () => ((ran += 1), { code: 0 }), env: { VISUAL: 'code --wait', EDITOR: 'subl -w' } });
    assert.equal(ran, 0);
    assert.equal(r.out.endsWith('Cannot open an editor: set VISUAL or EDITOR to a plain command (a name such as vim, or an absolute path, with no spaces or flags).\nnot sent\n'), true);
    assert.deepEqual(
      [editorCommand({ EDITOR: 'nano' }), editorCommand({ EDITOR: 'subl -w' }), editorCommand({ EDITOR: 'a;b' }), editorCommand({}), editorCommand({ VISUAL: 'code --wait', EDITOR: 'vi' }), editorCommand({ VISUAL: '/opt/ed/bin/edit', EDITOR: 'vi' })],
      ['nano', null, null, null, 'vi', '/opt/ed/bin/edit'],
    );
  });
});

describe('the issue form', () => {
  test('error-report.yml parses, has the error-report label and the four fields; blank issues stay enabled', () => {
    const form = YAML.parse(readFileSync(path.join(ROOT, '.github', 'ISSUE_TEMPLATE', 'error-report.yml'), 'utf8'));
    assert.deepEqual(form.labels, ['error-report']);
    assert.deepEqual(form.body.filter((f) => f.id).map((f) => [f.type, f.id]), [['textarea', 'doing'], ['textarea', 'versions'], ['textarea', 'details'], ['input', 'fingerprint']]);
    const config = YAML.parse(readFileSync(path.join(ROOT, '.github', 'ISSUE_TEMPLATE', 'config.yml'), 'utf8'));
    assert.equal(config.blank_issues_enabled, true);
  });
});
