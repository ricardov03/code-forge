import { fakeDeps, freshDir, readRecords, sink } from './helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { copyFileSync, existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const { extractClaims, readOnlyViolation, validateFacts, buildFacts, parseSheet, FactsError } = await import('../../src/session/facts.mjs');
const { runFactsVerb } = await import('../../src/cli/facts.mjs');
const { SessionError } = await import('../../src/session/spawn.mjs');
const { registerSecret, clearSecrets } = await import('../../src/util/redact.mjs');

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const BRIEF = path.join(REPO, 'test', 'fixtures', 'briefs', 'tool-brief.md');
const ANSWER = JSON.parse(readFileSync(path.join(REPO, 'test', 'fixtures', 'briefs', 'tool-brief.answer.json'), 'utf8'));
const CFG = { provider: 'anthropic', levels: Object.fromEntries(['L0', 'L1', 'L2', 'L3'].map((l) => [l, { model: `fake-${l}` }])) };

/** @param {Record<string, unknown>} patch - fields merged into row `index` of the fixture answer. */
const answerWith = (index, patch) => ({ facts: ANSWER.facts.map((f, i) => (i === index ? { ...f, ...patch } : f)) });

test('the extractor finds 7 claim tokens in the fixture brief', () => {
  assert.deepEqual(extractClaims(readFileSync(BRIEF, 'utf8')), [
    { token: 'claude plugin', kind: 'command' },
    { token: '--json-schema', kind: 'flag' },
    { token: '--max-turns', kind: 'flag' },
    { token: '~/.code-forge/ledger', kind: 'path' },
    { token: '@types/node', kind: 'package' },
    { token: 'https://api.typesafe.ai/v1/systemone', kind: 'endpoint' },
    { token: 'DO_NOT_TRACK', kind: 'env' },
  ]);
});

test('facts verb: one L0 facts session, the sheet is written and its header carries the brief sha', async () => {
  const ws = freshDir('facts-verb');
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), path.join(ws, '.code-forge.yml'));
  const brief = path.join(ws, 'brief.md');
  copyFileSync(BRIEF, brief);
  const { deps, records } = fakeDeps({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  const stdout = sink();
  const code = await runFactsVerb(['--brief', 'brief.md', '--out', 'plans/brief.facts.md'], { ...deps, stdout, cwd: ws });
  assert.equal(code, 0);
  const out = path.join(ws, 'plans', 'brief.facts.md');
  assert.deepEqual(JSON.parse(stdout.text()), { out, claims: 7, verified: 3 });
  const sheet = readFileSync(out, 'utf8');
  const sha = createHash('sha256').update(readFileSync(brief)).digest('hex');
  assert.equal(sheet.split('\n').filter((l) => l === `- brief_sha256: \`${sha}\``).length, 1);
  assert.deepEqual(parseSheet(sheet)?.facts.map((f) => [f.fact_id, f.tag]), [
    ['F1', 'VERIFIED'], ['F2', 'VERIFIED'], ['F3', 'NOT-FOUND'], ['F4', 'UNVERIFIABLE'], ['F5', 'VERIFIED'], ['F6', 'UNVERIFIABLE'], ['F7', 'NOT-FOUND'],
  ]);
  const spawned = readRecords(records);
  assert.equal(spawned.length, 1);
  const argv = spawned[0].argv;
  assert.deepEqual([argv[argv.indexOf('--model') + 1], argv[argv.indexOf('--tools') + 1], argv.includes('--restricted')], ['claude-haiku-4-5-20251001', 'Bash', true]);
  assert.equal(Buffer.from(spawned[0].stdin_b64, 'base64').toString('utf8').includes('- F7 env: DO_NOT_TRACK\n'), true);
});

test('a delegate answer with a write verb in command is refused and no sheet is written', async () => {
  const ws = freshDir('facts-write');
  const out = path.join(ws, 'brief.facts.md');
  const { deps } = fakeDeps({ FAKE_ANSWER: JSON.stringify(answerWith(1, { command: 'claude --help | touch notes.txt' })) });
  await assert.rejects(
    buildFacts({ cfg: CFG, briefPath: BRIEF, outPath: out }, deps),
    (err) => err instanceof FactsError && err.code === 'refused' && err.message === 'facts sheet refused: F2: command is not read-only: not an allowed read-only form: "touch"',
  );
  assert.equal(existsSync(out), false);
});

test('readOnlyViolation is an allow-list: 7 escapes are refused, plain reads pass', () => {
  const cases = [
    ['ls\nrm -rf x', 'not an allowed read-only form: "rm"'],
    ['git -C . commit -m x', 'git: only log, show, status, rev-parse, ls-files, cat-file or --version, with no global option'],
    ['npm --prefix . install', 'npm: only view, ls or --version'],
    ['node -p 1', 'node: only --version'],
    ['command rm x', 'command: only command -v <name>'],
    ['cat .env', 'reads a secret-looking file'],
    ['echo $HOME', '$ expansion is not allowed'],
    ['git log -1', null],
    ['ls src', null],
  ];
  assert.deepEqual(cases.map(([cmd]) => [cmd, readOnlyViolation(cmd)]), cases);
});

test('an env claim: only printenv NAME >/dev/null is accepted, and its excerpt is always blanked', () => {
  const claims = extractClaims(readFileSync(BRIEF, 'utf8'));
  const set = validateFacts(answerWith(6, { tag: 'VERIFIED', output_excerpt: 'exit 0' }), claims);
  assert.deepEqual([set[6].tag, set[6].output_excerpt], ['VERIFIED', '']);
  assert.throws(
    () => validateFacts(answerWith(6, { command: 'printenv DO_NOT_TRACK', output_excerpt: '1' }), claims),
    (err) => err instanceof FactsError && err.message === 'facts sheet refused: F7: an env claim may only be checked with printenv DO_NOT_TRACK >/dev/null',
  );
});

test('facts verb: an unreadable --brief is a usage error (exit 2) and spawns nothing', async () => {
  const ws = freshDir('facts-missing');
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), path.join(ws, '.code-forge.yml'));
  const { deps, records, stderr } = fakeDeps({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  assert.equal(await runFactsVerb(['--brief', 'missing.md'], { ...deps, stdout: sink(), cwd: ws }), 2);
  assert.deepEqual([stderr.text(), readRecords(records).length], ['facts: --brief cannot be read\n', 0]);
});

test('facts verb: a directory as --brief is a usage error (exit 2) and spawns nothing', async () => {
  const ws = freshDir('facts-brief-dir');
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), path.join(ws, '.code-forge.yml'));
  const { deps, records, stderr } = fakeDeps({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  assert.equal(await runFactsVerb(['--brief', '.'], { ...deps, stdout: sink(), cwd: ws }), 2);
  assert.deepEqual([stderr.text(), readRecords(records).length], ['facts: --brief cannot be read\n', 0]);
});

test('a VERIFIED row with an empty excerpt is refused', () => {
  const claims = extractClaims(readFileSync(BRIEF, 'utf8'));
  assert.throws(
    () => validateFacts(answerWith(0, { output_excerpt: '' }), claims),
    (err) => err instanceof FactsError && err.message === 'facts sheet refused: F1: VERIFIED with an empty output excerpt',
  );
  assert.equal(validateFacts(ANSWER, claims).length, 7);
});

test('an answer over the 200-character excerpt cap fails the schema', () => {
  const claims = extractClaims(readFileSync(BRIEF, 'utf8'));
  assert.throws(
    () => validateFacts(answerWith(1, { output_excerpt: 'x'.repeat(201) }), claims),
    (err) => err instanceof FactsError && err.message === 'facts answer does not match facts.schema.json: /facts/1/output_excerpt must NOT have more than 200 characters',
  );
});

test('curl is an option allow-list: 7 output-writing or extra forms are refused, 2 HEAD forms pass', () => {
  const refused = 'curl: only -s -S -I -L -f --head --silent --show-error --location --fail --max-time <N> and exactly one http(s) URL, as a HEAD request';
  const cases = [
    ['curl -sI -D /tmp/x https://h', 'absolute paths outside /usr, /opt/homebrew and /bin are not allowed'],
    ['curl -sI -D hdrs https://h', refused],
    ['curl -sI -c jar https://h', refused],
    ['curl -sI --trace t https://h', refused],
    ['curl -sI --etag-save e https://h', refused],
    ['curl -sI https://a https://b', refused],
    ['curl -s https://h', refused],
    ['curl -sI https://h', null],
    ['curl -sSILf --max-time 5 --head https://h', null],
  ];
  assert.deepEqual(cases.map(([cmd]) => [cmd, readOnlyViolation(cmd)]), cases);
});

test('secret paths: 3 glob bypasses, 5 recursive or out-of-cwd readers and 6 more secret files are refused', () => {
  const glob = 'unquoted glob characters (* ? [ ] { }) are not allowed';
  const tilde = 'paths under ~ are not allowed';
  const outside = 'absolute paths outside /usr, /opt/homebrew and /bin are not allowed';
  const cases = [
    ['cat ~/.ss?/id_*', glob],
    ['cat .en?', glob],
    ['head ~/.aw*/credentials', glob],
    ['grep -r . ~', tilde],
    ['rg . ~', tilde],
    ['find ~ -type f', tilde],
    ['find / -type f', outside],
    ['grep -rn secret src', 'grep: -r, -R and --recursive are not allowed'],
    ['cat .git-credentials', 'reads a secret-looking file'],
    ['cat .docker/config.json', 'reads a secret-looking file'],
    ['cat .kube/config', 'reads a secret-looking file'],
    ['cat .claude/.credentials.json', 'reads a secret-looking file'],
    ['cat .pgpass', 'reads a secret-looking file'],
    ['cat /proc/1/environ', 'reads a secret-looking file'],
    ['cat ../x', 'paths with a .. segment are not allowed'],
    ["find . -name '*.md'", null],
    ['ls /opt/homebrew/bin', null],
  ];
  assert.deepEqual(cases.map(([cmd]) => [cmd, readOnlyViolation(cmd)]), cases);
});

test('the <cli> --help case: path verbs, the help subcommand and build runners are refused (6), 3 forms pass', () => {
  const cases = [
    ['./x --help', 'a verb may not be a path'],
    ['/tmp/x --version', 'a verb may not be a path'],
    ['claude help', 'not an allowed read-only form: "claude"'],
    ['make help', 'make: build and task runners are not allowed'],
    ['just --help', 'just: build and task runners are not allowed'],
    ['rake --version', 'rake: build and task runners are not allowed'],
    ['claude --help', null],
    ['codex -h', null],
    ['grok --version', null],
  ];
  assert.deepEqual(cases.map(([cmd]) => [cmd, readOnlyViolation(cmd)]), cases);
});

test('a registered token inside command or excerpt never reaches the sheet (0 occurrences, 4 masks)', async () => {
  const token = 'sk-ant-FAKE0123456789abcdef';
  const ws = freshDir('facts-redact');
  const out = path.join(ws, 'brief.facts.md');
  const { deps } = fakeDeps({ FAKE_ANSWER: JSON.stringify(answerWith(0, { command: `claude --help | grep -c ${token}`, output_excerpt: `${token} 3` })) });
  registerSecret(token);
  try {
    const result = await buildFacts({ cfg: CFG, briefPath: BRIEF, outPath: out }, deps);
    assert.equal(result.status, 'ok');
    const sheet = readFileSync(out, 'utf8');
    assert.deepEqual([sheet.split(token).length - 1, sheet.split('[REDACTED]').length - 1], [0, 4]);
    assert.equal(parseSheet(sheet)?.facts[0].command, 'claude --help | grep -c [REDACTED]');
  } finally {
    clearSecrets();
  }
});

test('facts verb: a SessionError from the run exits 1, a bad --run id is a pre-run usage error (2)', async () => {
  const ws = freshDir('facts-session-error');
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), path.join(ws, '.code-forge.yml'));
  copyFileSync(BRIEF, path.join(ws, 'brief.md'));
  // the one SessionError a run can raise (a provider with no CLI mapping) is unreachable through a
  // validated config, so the exec seam stands in for it
  const { deps, stderr } = fakeDeps({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  const exec = async () => {
    throw new SessionError('usage', 'provider "x" has no CLI mapping');
  };
  const code = await runFactsVerb(['--brief', 'brief.md', '--out', 'brief.facts.md'], { ...deps, exec, stdout: sink(), cwd: ws });
  assert.deepEqual([code, stderr.text().includes('facts: provider "x" has no CLI mapping\n'), existsSync(path.join(ws, 'brief.facts.md'))], [1, true, false]);
  const second = fakeDeps({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  assert.equal(await runFactsVerb(['--brief', 'brief.md', '--run', 'bad/id'], { ...second.deps, stdout: sink(), cwd: ws }), 2);
  assert.deepEqual([second.stderr.text(), readRecords(second.records).length], ['facts: a run id is one path segment of letters, digits, ".", "_" or "-"\n', 0]);
});
