// `code-forge logs` (B27): list, summary, clear, path and report against a fixture log in a temp
// HOME, with a fake `exec` and a fake PATH lookup — `gh` is never run and nothing is ever sent.
// One per-file temp parent, removed in `after()`; HOME points into it before any `src` module loads.
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, describe, test } from 'node:test';

const PARENT = mkdtempSync(path.join(os.tmpdir(), 'cf-logs-'));
process.env.HOME = path.join(PARENT, 'home');
mkdirSync(process.env.HOME, { recursive: true });
after(() => rmSync(PARENT, { recursive: true, force: true }));

const { findSecret, issueRepo, runLogs } = await import('../../src/cli/logs.mjs');

const SYSTEM = { version: '0.2.3', node: 'v22.20.0', os: 'darwin 25.2.0 arm64' };
const NOW = () => new Date('2026-10-01T12:00:00.000Z');

/** @param {object} over */
function entry(over) {
  return {
    ts: '2026-10-01T10:00:00.000Z', version: '0.2.3', node: 'v22.20.0', platform: 'darwin', arch: 'arm64',
    verb: 'keys', sub: 'set', flags: ['--op'], exit: 1, kind: 'op_timeout',
    message: 'keys: 1Password did not answer in time; unlock the app and try again', stack: null,
    cleaned: [{ rule: 'home', count: 2 }, { rule: 'project_path', count: 1 }],
    ...over,
  };
}

/** Oldest first, as the file holds them. */
const FIXTURE = [
  entry({ ts: '2026-08-01T09:00:00.000Z', verb: 'tools', sub: 'install', flags: ['--yes'], kind: 'error', message: 'old one', cleaned: [] }),
  entry({ ts: '2026-09-30T08:00:00.000Z', verb: 'init', sub: null, flags: ['--jev-ref', '--yes'], exit: 2, kind: 'usage', message: 'init: unknown flag "--x"\nusage: code-forge init', cleaned: [{ rule: 'email', count: 1 }, { rule: 'item_id', count: 1 }] }),
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
 * @param {{exec?: any, onPath?: any, isTTY?: boolean, confirm?: boolean, cwd?: string, pkg?: any}} [o]
 */
async function logs(args, h, o = {}) {
  const stdout = sink();
  const stderr = sink();
  const calls = [];
  const answer = o.exec ?? (async () => ({ result: 'ok', code: 0, stdout: '', stderr: '', timedOut: false }));
  const prompts = [];
  const ui = { confirm: async (q) => (prompts.push(q), o.confirm ?? false), isCancel: () => false };
  const code = await runLogs(args, {
    env: { HOME: h, PATH: '' }, cwd: o.cwd ?? h, stdout, stderr, isTTY: o.isTTY ?? false, ui,
    exec: async (argv, opts) => (calls.push(argv), answer(argv, opts)),
    onPath: o.onPath ?? (() => false), now: NOW, system: SYSTEM, ...(o.pkg ? { pkg: o.pkg } : {}),
  });
  return { code, out: stdout.text(), err: stderr.text(), calls, prompts };
}

describe('logs, summary, clear, path', () => {
  test('logs: newest first, default 10; --last; no log', async () => {
    const h = home(FIXTURE);
    const r = await logs([], h);
    assert.equal(r.code, 0);
    assert.equal(
      r.out,
      '2026-10-01T10:00:00.000Z  keys set  1  op_timeout  keys: 1Password did not answer in time; unlock the app and try again\n' +
        '2026-09-30T08:00:00.000Z  init  2  usage  init: unknown flag "--x"\n' +
        '2026-08-01T09:00:00.000Z  tools install  1  error  old one\n',
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
    assert.equal(got[0], '2026-09-11T00:00:00.000Z  keys set  1  op_timeout  m11');
    assert.equal(got[9], '2026-09-02T00:00:00.000Z  keys set  1  op_timeout  m2');
  });

  test('summary: counts by verb + kind within the window, sorted by count', async () => {
    const h = home([...FIXTURE, entry({ ts: '2026-10-01T11:00:00.000Z' })]);
    const r = await logs(['summary'], h);
    assert.deepEqual([r.code, r.out], [0, 'errors in the last 30 days:\n    2  keys  op_timeout\n    1  init  usage\n']);
    const all = await logs(['summary', '--days', '90', '--json'], h);
    assert.deepEqual(JSON.parse(all.out), {
      days: 90,
      counts: [{ verb: 'keys', kind: 'op_timeout', count: 2 }, { verb: 'init', kind: 'usage', count: 1 }, { verb: 'tools', kind: 'error', count: 1 }],
    });
  });

  test('clear: non-TTY without --yes exits 2 and keeps the log; a no keeps it; --yes deletes it', async () => {
    const h = home(FIXTURE);
    const file = path.join(h, '.code-forge', 'logs', 'errors.jsonl');
    const r = await logs(['clear'], h);
    assert.deepEqual([r.code, existsSync(file)], [2, true]);
    const no = await logs(['clear'], h, { isTTY: true, confirm: false });
    assert.deepEqual([no.code, no.out, existsSync(file)], [1, 'cancelled — nothing deleted\n', true]);
    assert.deepEqual(no.prompts, [{ message: 'Delete the error log (3 entries)?', initialValue: false }]);
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
  '       code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--dry-run] [--yes]\n';

const DISCLOSURE =
  'What you will share (public on GitHub): code-forge, Node and OS versions; command names and flag NAMES; exit codes; error types; error messages and crash reports after cleaning.\n' +
  'Never shared: flag values, file contents, your code, keys or tokens, your home folder, project folder or project name.\n';

const TWO_TITLE = '[error report] 2 errors';
/** Built by hand (not by the code under test): the prefilled link for the 2-error report. */
let LINK = '';
const TWO_BODY = `## code-forge error report

- code-forge: 0.2.3
- Node: v22.20.0
- OS: darwin 25.2.0 arm64

### Note

it fails at ~/work

### Summary

| # | when | command | exit | kind |
|---|---|---|---|---|
| 1 | 2026-10-01T10:00:00.000Z | keys set | 1 | op_timeout |
| 2 | 2026-09-30T08:00:00.000Z | init | 2 | usage |

### Errors

#### 1. keys set: op_timeout

- when: 2026-10-01T10:00:00.000Z
- flags: \`--op\`
- exit: 1
- kind: op_timeout
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
- code-forge 0.2.3, Node v22.20.0, darwin arm64

message:

\`\`\`text
init: unknown flag "--x"
usage: code-forge init
\`\`\`
`;

LINK = `https://github.com/ricardov03/code-forge/issues/new?title=${encodeURIComponent(TWO_TITLE)}&body=${encodeURIComponent(TWO_BODY)}&labels=bug`;

/** What every 2-error report (with the note) prints before it asks or sends. */
const PREVIEW = `${DISCLOSURE}Cleaned: 3 home paths, 1 project path, 1 email, 1 1Password item ID.\n\nTitle: ${TWO_TITLE}\n\n${TWO_BODY}\n`;

describe('logs report', () => {
  test('--dry-run: disclosure, cleaning counts, the exact title and body; 0 exec calls', async () => {
    const h = home(FIXTURE);
    const r = await logs(['report', '--last', '2', '--note', `it fails at ${h}/work`, '--dry-run'], h);
    assert.equal(r.code, 0);
    assert.equal(r.calls.length, 0);
    assert.equal(r.err, '');
    assert.equal(
      r.out,
      `${DISCLOSURE}Cleaned: 3 home paths, 1 project path, 1 email, 1 1Password item ID.\n\nTitle: ${TWO_TITLE}\n\n${TWO_BODY}\ndry run: not sent\n`,
    );
  });

  test('one verb + kind gives a named title; --kind and --verb filter', async () => {
    const h = home(FIXTURE);
    const r = await logs(['report', '--verb', 'keys', '--dry-run'], h);
    assert.equal(r.out.includes('\nTitle: [error report] keys op_timeout\n'), true);
    const none = await logs(['report', '--kind', 'crash', '--dry-run'], h);
    assert.deepEqual([none.code, none.out], [0, 'no matching errors to report\n']);
  });

  test('non-TTY without --yes prints, exits 2, 0 exec calls; a no at the prompt: "not sent", exit 1, 0 calls', async () => {
    const h = home(FIXTURE);
    const args = ['report', '--last', '2', '--note', `it fails at ${h}/work`];
    const r = await logs(args, h, { onPath: () => true });
    assert.deepEqual([r.code, r.calls.length], [2, 0]);
    assert.equal(r.out, PREVIEW);
    assert.equal(r.err, 'logs: no terminal to confirm; not sent — pass --yes to send the report above\n');
    const no = await logs(args, h, { onPath: () => true, isTTY: true, confirm: false });
    assert.deepEqual([no.code, no.calls.length, no.out, no.err], [1, 0, `${PREVIEW}not sent\n`, '']);
    assert.deepEqual(no.prompts, [{ message: 'This will be public on GitHub. Send it?', initialValue: false }]);
  });

  test('gh path: exact argv, label retry once, prints the issue URL, removes the body file', async () => {
    const h = home(FIXTURE);
    const calls = [];
    let bodyFile = '';
    let bodyText = '';
    const exec = async (argv) => {
      calls.push(argv);
      if (argv[1] === 'auth') return { result: 'ok', code: 0, stdout: '', stderr: '', timedOut: false };
      bodyFile = argv[argv.indexOf('--body-file') + 1];
      bodyText = readFileSync(bodyFile, 'utf8');
      return argv.includes('--label')
        ? { result: 'failed', code: 1, stdout: '', stderr: "could not add label: 'bug' not found", timedOut: false }
        : { result: 'ok', code: 0, stdout: 'Creating issue\nhttps://github.com/ricardov03/code-forge/issues/42\n', stderr: '', timedOut: false };
    };
    const r = await logs(['report', '--last', '2', '--note', `it fails at ${h}/work`, '--yes'], h, { exec, onPath: (c) => c === 'gh' });
    assert.equal(r.code, 0);
    const base = ['gh', 'issue', 'create', '--repo', 'ricardov03/code-forge', '--title', TWO_TITLE, '--body-file', bodyFile];
    assert.deepEqual(calls, [['gh', 'auth', 'status'], [...base, '--label', 'bug'], base]);
    assert.equal(bodyText, TWO_BODY);
    assert.equal(existsSync(bodyFile), false);
    assert.equal(r.out, `${PREVIEW}sent: https://github.com/ricardov03/code-forge/issues/42\n`);
    assert.equal(r.err, '');
  });

  test('gh create failing for another reason: no retry, falls back to the link and the saved report, exit 0', async () => {
    const h = home(FIXTURE);
    const calls = [];
    const exec = async (argv) => (calls.push(argv), argv[1] === 'auth'
      ? { result: 'ok', code: 0, stdout: '', stderr: '', timedOut: false }
      : { result: 'failed', code: 4, stdout: 'gh-out-detail', stderr: 'HTTP 502 gh-err-detail', timedOut: false });
    const r = await logs(['report', '--last', '2', '--note', `it fails at ${h}/work`, '--yes'], h, { exec, onPath: () => true });
    assert.deepEqual([r.code, calls.length, r.err], [0, 2, '']);
    assert.equal(`${r.out}${r.err}`.split('gh-err-detail').length - 1, 0);
    assert.equal(`${r.out}${r.err}`.split('gh-out-detail').length - 1, 0);
    const saved = path.join(h, '.code-forge', 'logs', 'report-2026-10-01T12-00-00-000Z.md');
    assert.equal(r.out, `${PREVIEW}gh failed (exit 4); here is a link instead\nFull report saved at ${saved}\nOpen this link to file the issue:\n${LINK}\n`);
    assert.equal(readFileSync(saved, 'utf8'), TWO_BODY);
  });

  test('gh on PATH but not signed in: 1 exec call (auth status), the link, exit 0', async () => {
    const h = home(FIXTURE);
    const calls = [];
    const exec = async (argv) => (calls.push(argv), { result: 'failed', code: 1, stdout: '', stderr: 'not logged in', timedOut: false });
    const r = await logs(['report', '--last', '2', '--note', `it fails at ${h}/work`, '--yes'], h, { exec, onPath: () => true });
    assert.deepEqual([r.code, calls, r.err], [0, [['gh', 'auth', 'status']], '']);
    assert.equal(r.out, `${PREVIEW}Open this link to file the issue:\n${LINK}\n`);
  });

  test('the project folder under HOME becomes <project> in the report', async () => {
    const h = home(FIXTURE);
    const cwd = path.join(h, 'proj');
    mkdirSync(cwd);
    const r = await logs(['report', '--verb', 'tools', '--note', `it fails in ${cwd}/src`, '--dry-run'], h, { cwd });
    assert.equal(r.code, 0);
    assert.equal(r.out.includes('\n### Note\n\nit fails in <project>/src\n'), true);
    assert.equal(r.out.split(h).length - 1, 0);
    assert.equal(r.out.includes(`${DISCLOSURE}Cleaned: 1 project path.\n`), true);
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

  test('no gh: the exact prefilled link, no exec call', async () => {
    const h = home(FIXTURE);
    const r = await logs(['report', '--last', '2', '--note', `it fails at ${h}/work`, '--yes'], h);
    assert.equal(r.code, 0);
    assert.equal(r.calls.length, 0);
    const [label, link] = r.out.split('\n').slice(-3, -1);
    assert.equal(label, 'Open this link to file the issue:');
    const url = new URL(link);
    assert.equal(`${url.origin}${url.pathname}`, 'https://github.com/ricardov03/code-forge/issues/new');
    assert.deepEqual([...url.searchParams.keys()], ['title', 'body', 'labels']);
    assert.deepEqual([url.searchParams.get('title'), url.searchParams.get('body'), url.searchParams.get('labels')], [TWO_TITLE, TWO_BODY, 'bug']);
    assert.equal(link.includes('%20'), true);
  });

  test('a long body is cut to fit 7,500 characters and saved in full', async () => {
    const h = home(FIXTURE);
    const note = 'the same words again and again '.repeat(400);
    const r = await logs(['report', '--note', note, '--yes'], h);
    assert.equal(r.code, 0);
    const saved = path.join(h, '.code-forge', 'logs', 'report-2026-10-01T12-00-00-000Z.md');
    const link = r.out.split('\n').at(-2);
    assert.equal(link.length <= 7500, true);
    assert.equal(link.length > 7400, true);
    const body = new URL(link).searchParams.get('body');
    assert.equal(body.endsWith('\n\n(report cut; full report saved at ~/.code-forge/logs/report-2026-10-01T12-00-00-000Z.md)\n'), true);
    const full = readFileSync(saved, 'utf8');
    assert.equal(full.includes(note), true);
    assert.equal(full.startsWith(body.split('\n\n(report cut;')[0]), true);
    assert.equal(r.out.includes(`Full report saved at ${saved}\n`), true);
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
    test(`${rule} left in the report blocks it: exit 1, 0 calls, the text printed 0 times`, async () => {
      const h = home([entry({ message: `failed with ${secret}` })]);
      const r = await logs(['report', '--yes'], h, { onPath: () => true });
      assert.deepEqual([r.code, r.calls.length], [1, 0]);
      assert.equal(`${r.out}${r.err}`.split(secret).length - 1, 0);
      assert.equal(r.out, DISCLOSURE);
      assert.equal(r.err, `Possible secret found in the report (${rule}, report line 27; the title is line 1); not sent. Find it with code-forge logs --json and remove it with code-forge logs clear.\n`);
    });
  }

  test('a note moves the reported line (4 more lines); the same text checked twice gives the same hit', async () => {
    const secret = 'ghp_FAKE0123456789abcdefghij';
    const h = home([entry({ message: `failed with ${secret}` })]);
    const r = await logs(['report', '--note', 'see below', '--yes'], h, { onPath: () => true });
    assert.deepEqual([r.code, r.calls.length], [1, 0]);
    assert.equal(r.err, 'Possible secret found in the report (GitHub token, report line 31; the title is line 1); not sent. Find it with code-forge logs --json and remove it with code-forge logs clear.\n');
    const text = `title\nok\nkey ${secret}`;
    assert.deepEqual([findSecret(text), findSecret(text)], [{ rule: 'GitHub token', line: 3 }, { rule: 'GitHub token', line: 3 }]);
  });
});
