import { fakeDeps, freshDir, readRecords, sink, writeIn } from './helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { copyFileSync, existsSync, lstatSync, mkdirSync, readdirSync, readFileSync, realpathSync, statSync, symlinkSync, unlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const { extractClaims, readOnlyViolation, validateFacts, buildFacts, buildSnapshot, removeSnapshot, parseSheet, FactsError } = await import('../../src/session/facts.mjs');
const { runFactsVerb } = await import('../../src/cli/facts.mjs');
const { SessionError, spawnSession } = await import('../../src/session/spawn.mjs');
const { exec: realExec } = await import('../../src/util/exec.mjs');
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

test('find is a predicate allow-list: -execdir, -ok, -okdir, -fls and -fprintf are refused (5), 2 forms pass', () => {
  const refused = 'find: only -name -iname -path -ipath -type -maxdepth -mindepth -print -print0 -newer -size -empty -mtime -mmin -not ! -a -o -and -or and paths';
  const cases = [
    ["find . -execdir ls '{}' +", refused],
    ["find . -ok ls '{}' +", refused],
    ["find . -okdir ls '{}' +", refused],
    ['find . -fls out.txt', refused],
    ["find . -fprintf out.txt '%p'", refused],
    ["find src -name '*.mjs' -type f", null],
    ['find src -mtime -1 -not -empty', null],
  ];
  assert.deepEqual(cases.map(([cmd]) => [cmd, readOnlyViolation(cmd)]), cases);
});

/** @param {string[]} args @param {string} cwd @returns {string} git stdout, run with no inherited GIT_* and no user config. */
function gitIn(args, cwd) {
  const env = { PATH: process.env.PATH ?? '', HOME: process.env.HOME ?? '', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull };
  return execFileSync('git', ['-c', 'user.name=fake', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', ...args], { cwd, env, encoding: 'utf8' });
}

/** @param {string} p @returns {boolean} true when anything (a dangling symlink included) is at `p`. */
function present(p) {
  try {
    lstatSync(p);
    return true;
  } catch {
    return false;
  }
}

/**
 * A fake-CLI exec seam that looks at the session cwd before the fake runs: which of `probe` are
 * present (lstat), and whether a new file and an overwrite are refused. Every `rows` claim whose
 * relative path is present is answered VERIFIED, else NOT-FOUND.
 * @param {string[]} probe @param {string[]} claims - `./rel` path claims.
 */
function snapshotProbe(probe, claims) {
  const state = { cwd: '', seen: /** @type {Array<[string, boolean]>} */ ([]), writes: /** @type {string[]} */ ([]) };
  /** @type {typeof realExec} */
  const exec = async (argv, opts = {}) => {
    state.cwd = /** @type {string} */ (opts.cwd);
    state.seen = probe.map((rel) => [rel, present(path.join(state.cwd, rel))]);
    state.writes = [path.join(state.cwd, 'new.txt'), path.join(state.cwd, claims[0])].map((f) => {
      try {
        writeFileSync(f, 'x');
        return 'written';
      } catch (err) {
        return String(/** @type {NodeJS.ErrnoException} */ (err).code);
      }
    });
    const rows = claims.map((claim, i) => {
      const found = present(path.join(state.cwd, claim));
      return { fact_id: `F${i + 1}`, claim, kind: 'path', command: `ls ${claim}`, output_excerpt: found ? claim : '', tag: found ? 'VERIFIED' : 'NOT-FOUND', why: null };
    });
    return realExec(argv, { ...opts, env: { ...opts.env, FAKE_ANSWER: JSON.stringify({ facts: rows }) } });
  };
  return { exec, state };
}

test('the facts delegate runs in a read-only snapshot of HEAD + sources: ./src/a.mjs is VERIFIED, 6 secrets/links are absent, the snapshot is removed, the repo is unchanged', async () => {
  const repo = freshDir('facts-repo');
  const outside = writeIn(freshDir('facts-outside'), 'secret.txt', 'outside the repo\n');
  mkdirSync(path.join(repo, 'src'));
  mkdirSync(path.join(repo, 'notes'));
  writeFileSync(path.join(repo, 'src', 'a.mjs'), 'export const a = 1;\n');
  writeFileSync(path.join(repo, '.env'), 'TOKEN=FAKE\n');
  symlinkSync(outside, path.join(repo, 'tracked-link'));
  gitIn(['init', '-q'], repo);
  gitIn(['add', 'src/a.mjs', '.env', 'tracked-link'], repo);
  gitIn(['commit', '-q', '-m', 'fixture'], repo);
  assert.equal(gitIn(['ls-files'], repo), '.env\nsrc/a.mjs\ntracked-link\n');
  writeFileSync(path.join(repo, 'notes', 'extra.md'), 'The entry point is ./src/a.mjs and the notes live in ./notes/extra.md.\n');
  writeFileSync(path.join(repo, 'notes', 'id_rsa'), 'FAKE KEY\n');
  writeFileSync(path.join(repo, 'notes', '.env'), 'TOKEN=FAKE\n');
  symlinkSync('extra.md', path.join(repo, 'notes', 'inside-link'));
  symlinkSync(outside, path.join(repo, 'notes', 'outside-link'));
  writeFileSync(path.join(repo, 'scratch.txt'), 'untracked, not a source\n');
  const brief = writeIn(repo, 'brief.md', 'Read ./src/a.mjs first.\n');
  const statusBefore = gitIn(['status', '--porcelain'], repo);
  const root = freshDir('facts-snap-root');
  const probe = ['src/a.mjs', 'notes/extra.md', 'scratch.txt', 'brief.md', '.env', 'tracked-link', 'notes/id_rsa', 'notes/.env', 'notes/inside-link', 'notes/outside-link'];
  const { exec, state } = snapshotProbe(probe, ['./src/a.mjs', './notes/extra.md']);
  const { deps } = fakeDeps();
  const out = path.join(root, 'brief.facts.md');
  const result = await buildFacts({ cfg: CFG, briefPath: brief, sources: [path.join(repo, 'notes')], outPath: out, runRoot: root, projectDir: repo }, { ...deps, exec });
  assert.equal(result.status, 'ok');
  assert.deepEqual(result.facts?.map((f) => [f.fact_id, f.claim, f.tag]), [['F1', './src/a.mjs', 'VERIFIED'], ['F2', './notes/extra.md', 'VERIFIED']]);
  assert.deepEqual(state.seen, [
    ['src/a.mjs', true], ['notes/extra.md', true], ['scratch.txt', false], ['brief.md', false],
    ['.env', false], ['tracked-link', false], ['notes/id_rsa', false], ['notes/.env', false], ['notes/inside-link', false], ['notes/outside-link', false],
  ]);
  assert.deepEqual(state.writes, ['EACCES', 'EACCES']);
  assert.equal(state.cwd, path.join(realpathSync(root), 'facts', path.basename(path.dirname(state.cwd)), 'snapshot'));
  assert.deepEqual([present(state.cwd), present(path.dirname(state.cwd))], [false, false]);
  assert.deepEqual([gitIn(['status', '--porcelain'], repo), readFileSync(path.join(repo, 'src', 'a.mjs'), 'utf8')], [statusBefore, 'export const a = 1;\n']);
});

test('a project in a subdirectory of the repo: the session runs in that subdirectory, ./file is VERIFIED, a sibling source is not copied', async () => {
  const repo = freshDir('facts-mono');
  mkdirSync(path.join(repo, 'pkg'));
  mkdirSync(path.join(repo, 'other'));
  writeFileSync(path.join(repo, 'pkg', 'file.mjs'), 'export {};\n');
  writeFileSync(path.join(repo, 'top.md'), 'top\n');
  gitIn(['init', '-q'], repo);
  gitIn(['add', 'pkg/file.mjs', 'top.md'], repo);
  gitIn(['commit', '-q', '-m', 'fixture'], repo);
  writeFileSync(path.join(repo, 'other', 'sibling.md'), 'sibling\n');
  mkdirSync(path.join(repo, 'pkg', 'docs'));
  writeFileSync(path.join(repo, 'pkg', 'docs', 'own.md'), 'own\n');
  const brief = writeIn(repo, 'brief.md', 'The entry is ./file.mjs.\n');
  const root = freshDir('facts-mono-root');
  const { exec, state } = snapshotProbe(['file.mjs', 'docs/own.md', '../other/sibling.md', '../top.md'], ['./file.mjs']);
  const { deps } = fakeDeps();
  const sources = [path.join(repo, 'other', 'sibling.md'), path.join(repo, 'pkg', 'docs')];
  const result = await buildFacts({ cfg: CFG, briefPath: brief, sources, outPath: path.join(root, 'b.facts.md'), runRoot: root, projectDir: path.join(repo, 'pkg') }, { ...deps, exec });
  assert.deepEqual(result.facts?.map((f) => [f.claim, f.tag]), [['./file.mjs', 'VERIFIED']]);
  assert.deepEqual(state.seen, [['file.mjs', true], ['docs/own.md', true], ['../other/sibling.md', false], ['../top.md', true]]);
  assert.equal(path.basename(state.cwd), 'pkg');
  assert.equal(present(path.dirname(state.cwd)), false);
});

test('facts cwd override fails closed: the repo root, the live cwd, "" and a path outside <runRoot>/facts are refused (4) and nothing spawns', async () => {
  const root = freshDir('facts-cwd-root');
  mkdirSync(path.join(root, 'facts', 'x', 'snapshot'), { recursive: true });
  const packet = writeIn(freshDir('facts-cwd-packet'), 'packet.md', 'facts\n');
  const { deps, records } = fakeDeps();
  const cases = [
    [REPO, 'facts cwd refused: it is outside the run root facts snapshot area'],
    [process.cwd(), 'facts cwd refused: it is outside the run root facts snapshot area'],
    ['', 'facts cwd refused: it must be a non-empty path'],
    [freshDir('facts-cwd-elsewhere'), 'facts cwd refused: it is outside the run root facts snapshot area'],
  ];
  const got = [];
  for (const [cwd] of cases) {
    try {
      await spawnSession({ cfg: CFG, level: 'L0', role: 'facts', promptPath: packet, cwd, runRoot: root }, deps);
      got.push([cwd, 'spawned']);
    } catch (err) {
      got.push([cwd, err instanceof SessionError && err.code === 'usage' ? err.message : String(err)]);
    }
  }
  assert.deepEqual(got, cases);
  assert.equal(readRecords(records).length, 0);
  const ok = await spawnSession({ cfg: CFG, level: 'L0', role: 'facts', promptPath: packet, cwd: path.join(root, 'facts', 'x', 'snapshot'), runRoot: root }, deps);
  assert.deepEqual([ok.status, readRecords(records).length], ['ok', 1]);
});

test('a run root inside a git work tree: the snapshot is built in a private 0700 tmp root under os.tmpdir(), not in the repo; ./src/a.mjs is VERIFIED, 1 spawn, the snapshot is removed', async () => {
  const repo = freshDir('facts-inrepo');
  mkdirSync(path.join(repo, 'src'));
  writeFileSync(path.join(repo, 'src', 'a.mjs'), 'export const a = 1;\n');
  gitIn(['init', '-q'], repo);
  gitIn(['add', 'src/a.mjs'], repo);
  gitIn(['commit', '-q', '-m', 'fixture'], repo);
  const brief = writeIn(repo, 'brief.md', 'Read ./src/a.mjs first.\n');
  const root = path.join(repo, '.tmp', 'run-1'); // tmp.root configured inside the project
  mkdirSync(root, { recursive: true });
  const { exec, state } = snapshotProbe(['src/a.mjs'], ['./src/a.mjs']);
  const { deps, records } = fakeDeps();
  const result = await buildFacts({ cfg: CFG, briefPath: brief, sources: [], outPath: path.join(freshDir('facts-inrepo-out'), 'b.facts.md'), runRoot: root, projectDir: repo }, { ...deps, exec });
  assert.deepEqual([result.status, result.facts?.map((f) => [f.fact_id, f.claim, f.tag]), readRecords(records).length], ['ok', [['F1', './src/a.mjs', 'VERIFIED']], 1]);
  assert.deepEqual(state.seen, [['src/a.mjs', true]]);
  const tmpRoot = path.dirname(path.dirname(path.dirname(state.cwd))); // <tmpRoot>/facts/<stamp>/snapshot
  assert.deepEqual(
    [
      state.cwd.startsWith(`${realpathSync(repo)}${path.sep}`),
      path.dirname(tmpRoot),
      /^facts-[0-9a-f]{16}$/.test(path.basename(tmpRoot)),
      path.basename(path.dirname(path.dirname(state.cwd))),
      path.basename(state.cwd),
      statSync(tmpRoot).mode & 0o777,
      existsSync(path.join(tmpRoot, 'owner.json')),
      existsSync(path.join(root, 'facts')),
    ],
    [false, path.join(realpathSync(os.tmpdir()), 'code-forge'), true, 'facts', 'snapshot', 0o700, true, false],
  );
  assert.deepEqual([present(state.cwd), present(path.dirname(state.cwd))], [false, false]);
});

test('a hand-built facts cwd inside a git work tree (a .git directory, a .git file) is refused naming the ancestor (2) and nothing spawns', async () => {
  const packet = writeIn(freshDir('facts-git-packet'), 'packet.md', 'facts\n');
  const { deps, records } = fakeDeps();
  const withDir = freshDir('facts-git-dir');
  gitIn(['init', '-q'], withDir);
  const withFile = freshDir('facts-git-file');
  writeFileSync(path.join(withFile, '.git'), 'gitdir: ../elsewhere/.git/worktrees/x\n');
  const got = [];
  for (const tree of [withDir, withFile]) {
    const root = path.join(tree, 'tmp', 'run');
    const cwd = path.join(root, 'facts', 'x', 'snapshot'); // inside <runRoot>/facts, so only the ancestor walk can refuse it
    mkdirSync(cwd, { recursive: true });
    try {
      await spawnSession({ cfg: CFG, level: 'L0', role: 'facts', promptPath: packet, cwd, runRoot: root }, deps);
      got.push('spawned');
    } catch (err) {
      got.push(err instanceof SessionError && err.code === 'usage' ? err.message : String(err));
    }
  }
  assert.deepEqual(got, [withDir, withFile].map((tree) => `facts cwd refused: inside a git work tree (${realpathSync(tree)})`));
  assert.equal(readRecords(records).length, 0);
});

/** @param {string} dir @returns {string} sha256 over every entry below `dir` (sorted relative path, type, content). */
function treeDigest(dir) {
  const hash = createHash('sha256');
  for (const rel of readdirSync(dir, { recursive: true }).map(String).sort()) {
    const full = path.join(dir, rel);
    const st = lstatSync(full);
    hash.update(`${rel}\0${st.isSymbolicLink() ? 'link' : st.isDirectory() ? 'dir' : 'file'}\0`);
    if (st.isFile()) hash.update(readFileSync(full));
    hash.update('\0');
  }
  return hash.digest('hex');
}

test('a HEAD symlink shadowed by a working-tree directory holding a source: the copy lands inside the snapshot (./shadow/src.md VERIFIED) and the directory the link pointed at is unchanged', async () => {
  const outside = freshDir('facts-escape-outside');
  writeIn(outside, 'sentinel.txt', 'keep\n');
  const repo = freshDir('facts-escape-repo');
  writeFileSync(path.join(repo, 'keep.md'), 'tracked\n');
  symlinkSync(outside, path.join(repo, 'shadow'));
  gitIn(['init', '-q'], repo);
  gitIn(['add', 'keep.md', 'shadow'], repo);
  gitIn(['commit', '-q', '-m', 'fixture'], repo);
  unlinkSync(path.join(repo, 'shadow')); // the working tree now has a real directory where HEAD has the link
  mkdirSync(path.join(repo, 'shadow'));
  writeFileSync(path.join(repo, 'shadow', 'src.md'), 'inside\n');
  const brief = writeIn(repo, 'brief.md', 'See ./shadow/src.md.\n');
  const before = treeDigest(outside);
  const root = freshDir('facts-escape-root');
  const { exec, state } = snapshotProbe(['shadow/src.md', 'keep.md'], ['./shadow/src.md']);
  const { deps } = fakeDeps();
  const result = await buildFacts({ cfg: CFG, briefPath: brief, sources: [path.join(repo, 'shadow')], outPath: path.join(root, 'b.facts.md'), runRoot: root, projectDir: repo }, { ...deps, exec });
  assert.deepEqual([result.status, result.facts?.map((f) => [f.claim, f.tag])], ['ok', [['./shadow/src.md', 'VERIFIED']]]);
  assert.deepEqual(state.seen, [['shadow/src.md', true], ['keep.md', true]]);
  assert.deepEqual([treeDigest(outside), readdirSync(outside), readFileSync(path.join(outside, 'sentinel.txt'), 'utf8')], [before, ['sentinel.txt'], 'keep\n']);
});

test('HEAD resolves but git archive fails ⇒ FactsError(snapshot) and no head.tar left; a repo without a commit ⇒ the sources alone, git archive never called', async () => {
  const repo = freshDir('facts-archive-fail');
  writeFileSync(path.join(repo, 'a.md'), 'a\n');
  gitIn(['init', '-q'], repo);
  gitIn(['add', 'a.md'], repo);
  gitIn(['commit', '-q', '-m', 'fixture'], repo);
  const work = freshDir('facts-archive-work');
  let archives = 0;
  /** @type {typeof realExec} */
  const failing = async (argv, opts) => {
    if (argv[0] === 'git' && argv[1] === 'archive') {
      archives += 1;
      return { result: 'failed', code: 128, signal: null, stdout: '', stderr: 'fatal: injected\n', timedOut: false };
    }
    return realExec(argv, opts);
  };
  await assert.rejects(
    buildSnapshot(repo, [], path.join(work, 'snapshot'), work, failing),
    (err) => err instanceof FactsError && err.code === 'snapshot' && err.message === 'the project snapshot could not be archived (git archive exit 128)',
  );
  assert.deepEqual([archives, present(path.join(work, 'head.tar'))], [1, false]);

  const bare = freshDir('facts-no-commit');
  gitIn(['init', '-q'], bare);
  mkdirSync(path.join(bare, 'notes'));
  writeFileSync(path.join(bare, 'notes', 'n.md'), 'n\n');
  const work2 = freshDir('facts-no-commit-work');
  const snap2 = path.join(work2, 'snapshot');
  const cwd = await buildSnapshot(bare, [path.join(bare, 'notes')], snap2, work2, failing);
  assert.deepEqual([cwd, readdirSync(snap2, { recursive: true }).map(String).sort(), archives, present(path.join(work2, 'head.tar'))], [snap2, ['notes', 'notes/n.md'], 1, false]);
  removeSnapshot(snap2);
});

test('only the facts role takes the cwd override: a reviewer still runs in a fresh EMPTY directory (readdir at session start is [], 1 record)', async () => {
  const given = freshDir('reviewer-given-cwd');
  writeIn(given, 'marker.txt', 'must not be seen\n');
  const packet = path.join(freshDir('reviewer-packet'), 'packet.md');
  writeFileSync(packet, 'review\n');
  const { deps, records } = fakeDeps();
  /** @type {string[][]} */
  const listings = [];
  /** @type {typeof realExec} */
  const exec = async (argv, opts = {}) => {
    listings.push(readdirSync(/** @type {string} */ (opts.cwd)));
    return realExec(argv, opts);
  };
  const result = await spawnSession({ cfg: CFG, level: 'L0', role: 'reviewer', promptPath: packet, cwd: given, runRoot: freshDir('reviewer-root') }, { ...deps, exec });
  assert.equal(result.status, 'ok');
  const spawned = readRecords(records);
  assert.deepEqual([listings, spawned.length, spawned[0].cwd === realpathSync(given), path.basename(spawned[0].cwd)], [[[]], 1, false, 'cwd']);
});
