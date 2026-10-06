import { fakeDeps, freshDir, readRecords, sink, writeIn } from './helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { copyFileSync, existsSync, lstatSync, mkdirSync, readdirSync, readFileSync, realpathSync, statSync, symlinkSync, unlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const { extractClaims, echoedClaim, readOnlyViolation, validateFacts, buildFacts, buildFactsPacket, isSilentCommand, buildSnapshot, removeSnapshot, parseSheet, FactsError, readProjectBins } = await import('../../src/session/facts.mjs');
const { runFactsVerb, downgradeLine } = await import('../../src/cli/facts.mjs');
const { SessionError, spawnSession } = await import('../../src/session/spawn.mjs');
const { exec: realExec } = await import('../../src/util/exec.mjs');
const { registerSecret, clearSecrets } = await import('../../src/util/redact.mjs');

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const BRIEF = path.join(REPO, 'test', 'fixtures', 'briefs', 'tool-brief.md');
const ANSWER = JSON.parse(readFileSync(path.join(REPO, 'test', 'fixtures', 'briefs', 'tool-brief.answer.json'), 'utf8'));
const CFG = { provider: 'anthropic', levels: Object.fromEntries(['L0', 'L1', 'L2', 'L3'].map((l) => [l, { model: `fake-${l}` }])) };

const ONE = 'one command per row: no pipe or list (| ; && || & or a newline)';

/** The delegate's snapshot as the fixture answer expects it: a `src` folder (its flag rows grep it). */
const SNAP = freshDir('fixture-snap');
mkdirSync(path.join(SNAP, 'src'));
const IN_SNAP = { cwd: SNAP };

/**
 * A project whose `src` folder is passed as a source, so the facts snapshot holds it (the fixture
 * answer's flag rows grep `src`).
 * @param {string} name
 */
function srcProject(name) {
  const projectDir = freshDir(name);
  mkdirSync(path.join(projectDir, 'src'));
  writeFileSync(path.join(projectDir, 'src', 'reviewer.mjs'), "argv.push('--json-schema', schema);\n");
  return { projectDir, sources: [path.join(projectDir, 'src')], runRoot: freshDir(`${name}-root`) };
}

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
  mkdirSync(path.join(ws, 'src'));
  writeFileSync(path.join(ws, 'src', 'reviewer.mjs'), "argv.push('--json-schema', schema);\n");
  const { deps, records } = fakeDeps({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  const stdout = sink();
  const code = await runFactsVerb(['--brief', 'brief.md', '--out', 'plans/brief.facts.md', '--sources', 'src'], { ...deps, stdout, cwd: ws });
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
  // B50: nothing is pre-approved for the delegate
  assert.equal(argv.filter((/** @type {string} */ t) => t === '--allowedTools' || t === '--allowed-tools').length, 0);
  assert.equal(Buffer.from(spawned[0].stdin_b64, 'base64').toString('utf8').includes('- F7 env: DO_NOT_TRACK\n'), true);
});

test('B50 grep options are an exact allow-list (-n -c -i -w -F -rn -nr before --): 3 source-grep forms and 2 plain greps pass, 30 variants are refused', () => {
  const opts = 'grep: options only -n -c -i -w -F before --, or the source grep grep -rn -- <flag> <path>';
  const only = 'grep -rn: only grep -rn -- <--long-flag> <path> (one relative subdirectory or file, never . or the root)';
  const secret = 'reads a secret-looking file';
  const cases = [
    ['grep -rn -- --max src', null],
    ['grep -nr -- --max-rounds ./src/cli', null],
    ['grep -rn -- --max src/cli/review.mjs', null],
    ['grep -c -- -r file.txt', null], // the pattern after -- is no option
    ['grep -n -i -w -F plugin README.md', null],
    ['grep -h foo a.txt', opts], // -h is not in the list; never a help check
    ['grep -2r -- --max src', opts],
    ['grep -i2r -- --max src', opts],
    ['grep --recur -- --max src', opts],
    ['grep --dir=recurse -- --max src', opts],
    ['grep -r -n -- --max src', opts],
    ['grep -d recurse -- --max src', opts],
    ['grep -Rn -- --max src', opts],
    ['grep -rln -- --max src', opts],
    ['grep -l -- --max src', opts],
    ['grep -L -- --max src', opts],
    ['grep -rn -e --max src', opts],
    ['grep -q plugin README.md', opts],
    ['grep foo src -rn', only],
    ['grep -rn -- -a src', only],
    ['grep -rn -- -e src', only],
    ['grep -rn -- --ab src', only],
    ['grep -rn -- --max .', only],
    ['grep -rn -- --max ./', only],
    ["grep -rn -- --max ''", only],
    ['grep -rn -- --max src bin', only],
    ['grep -rn -- --max', only],
    ['grep -rn -i -- --max src', only],
    ['grep -rn -- --max src/..', 'paths with a .. segment are not allowed'],
    ['grep -rn -- --token .env', secret],
    ['grep -rn -- --token config/.npmrc', secret],
    ['grep -rn -- --max ~', 'paths under ~ are not allowed'],
    ['grep -rn -- --max /', 'absolute paths outside /usr, /opt/homebrew and /bin are not allowed'],
    ['grep -rn -- --max src | head', ONE],
    ['ls ; grep -rn -- --max src', ONE],
  ];
  assert.deepEqual(cases.map(([cmd]) => [cmd, readOnlyViolation(cmd)]), cases);
});

test('B50 a flag claim is proved only by grep -rn -- <flag> <existing source dir>: VERIFIED needs a file:line: hit under it in a source file (never docs, plans, tests, node_modules, text or the brief); NOT-FOUND needs a whole top-level folder; a plain grep never counts (3 counted, 20 downgraded)', () => {
  const cwd = freshDir('grep-snapshot');
  for (const d of ['src/cli', 'lib', 'dist', '.hidden', 'docs', 'notes']) mkdirSync(path.join(cwd, d), { recursive: true });
  writeFileSync(path.join(cwd, 'package.json'), '{}');
  const tokens = ['--max', '--force', '--until', 'code-forge review', '--wait', '--deny', '--plan', '--brief', '--tests', '--scope', '--lane', '--line', '--pkg', '--dist', '--hid', '--other', '--vend', '--doc', '--ok', '--tst', '--brf', '--mds', '--nest'];
  const claims = /** @type {Array<{token: string, kind: any}>} */ (tokens.map((t) => ({ token: t, kind: t.startsWith('--') ? 'flag' : 'command' })));
  const row = (/** @type {number} */ i, /** @type {string} */ command, /** @type {string} */ excerpt, tag = 'VERIFIED') => ({ fact_id: `F${i}`, claim: claims[i - 1].token, kind: claims[i - 1].kind, command, output_excerpt: excerpt, tag, why: null });
  const { facts, downgraded } = validateFacts({
    facts: [
      row(1, 'grep -rn -- --max src', "docs/a.md:1: --max\nsrc/cli/review.mjs:12:  '--max': { value: true },"),
      row(2, 'grep -rn -- --force src', "src/cli/git.mjs:3:  '--force-push': true,"),
      row(3, 'grep -rn -- --stop-at src', 'src/cli/autopilot.mjs:9: --until --stop-at'),
      row(4, 'grep -rn -- --review src', 'src/cli/code-forge review --review'),
      row(5, 'grep -rn -- --wait src', '', 'NOT-FOUND'),
      row(6, 'grep -rn -- --denied src', '', 'NOT-FOUND'),
      row(7, 'grep -rn -- --plan plans', 'plans/x.plan.md:3: run with --plan\ndocs/guide.mjs:2: --plan'),
      row(8, 'grep -rn -- --brief notes', 'notes/spec.rst:1: pass --brief'),
      row(9, 'grep -rn -- --tests src', "src/test/x.test.mjs:4: '--tests'"),
      row(10, 'grep -rn -- --scope src/cli', '', 'NOT-FOUND'),
      row(11, 'grep -rn -- --lane docs', '', 'NOT-FOUND'),
      row(12, 'grep -rn -- --line src/cli/review.mjs', "12:  '--line': true,"),
      row(13, 'grep -rn -- --pkg package.json', '', 'NOT-FOUND'),
      row(14, 'grep -rn -- --dist dist', '', 'NOT-FOUND'),
      row(15, 'grep -rn -- --hid .hidden', '', 'NOT-FOUND'),
      row(16, 'grep -rn -- --other src', 'lib/x.mjs:1: --other'),
      row(17, 'grep -rn -- --vend src', 'src/node_modules/a/index.js:1: --vend'),
      row(18, 'grep -n -- --doc README.md', '3: --doc'),
      row(19, 'grep -n -- --ok src/a.mjs', "3:  '--ok': true,"),
      row(20, 'grep -n -- --tst test/a.test.mjs', '', 'NOT-FOUND'),
      row(21, 'grep -c -- --brf notes/spec.rst', '0', 'NOT-FOUND'),
      row(22, 'grep -n -- --mds src/a.mjs docs/a.md', 'src/a.mjs:1: --mds'),
      row(23, 'grep -rn -- --nest src/cli', 'src/cli/x.mjs:1: --nest'),
    ],
  }, claims, { briefRel: 'notes/spec.rst', cwd });
  const hit = 'VERIFIED source grep without a file:line: hit in a source file naming the claimed flag (docs, plans, tests, fixtures, text files and the brief do not count); not counted';
  const scope = 'NOT-FOUND source grep must search one whole top-level source folder (not docs, plans, test, tests, fixtures or node_modules); not counted';
  const own = 'a source grep counts only for a flag claim, with that flag as its pattern; not counted';
  const proof = 'a flag claim is proved only by grep -rn -- <flag> <source folder>; not counted';
  assert.deepEqual(facts.filter((f) => f.tag !== 'UNVERIFIABLE').map((f) => [f.fact_id, f.tag]), [['F1', 'VERIFIED'], ['F5', 'NOT-FOUND'], ['F23', 'VERIFIED']]);
  assert.deepEqual(downgraded.map((d) => [d.fact_id, d.reason]), [
    ['F2', hit], ['F3', own], ['F4', own], ['F6', own], ['F7', proof], ['F8', hit], ['F9', hit], ['F10', scope], ['F11', proof], ['F12', proof],
    ['F13', proof], ['F14', proof], ['F15', proof], ['F16', hit], ['F17', hit], ['F18', proof], ['F19', proof], ['F20', proof], ['F21', proof], ['F22', proof],
  ]);
  // with no known cwd (no snapshot), no source grep counts — VERIFIED or NOT-FOUND
  const noCwd = validateFacts({ facts: [row(1, 'grep -rn -- --max src', "src/cli/review.mjs:12:  '--max': { value: true },"), row(5, 'grep -rn -- --wait src', '', 'NOT-FOUND')] }, claims.slice(0, 5));
  assert.deepEqual(noCwd.downgraded, [{ fact_id: 'F1', reason: proof }, { fact_id: 'F5', reason: proof }]);
});

test('B50 a command claim written without its CLI: `run reload` becomes `code-forge run reload` from the brief and package.json alone (never PATH); 4 cases keep it as written', () => {
  const text = 'Autopilot extends `code-forge`.\n- `run reload` swaps config but has no revert.\n- `hub issue` lists issues.\n';
  const kept = (/** @type {string} */ t, /** @type {any} */ opts) => extractClaims(t, opts).filter((c) => c.kind === 'command').map((c) => c.token);
  const bins = { projectBins: ['code-forge'] };
  const savedPath = process.env.PATH;
  /** @type {string[][]} */
  const underPaths = [];
  try {
    for (const p of ['', savedPath ?? '']) {
      process.env.PATH = p;
      underPaths.push(kept(text, bins));
    }
  } finally {
    process.env.PATH = savedPath;
  }
  assert.deepEqual(underPaths, [['code-forge run reload', 'code-forge hub issue'], ['code-forge run reload', 'code-forge hub issue']]);
  assert.deepEqual(
    [
      kept(`${text}Install \`hub\` first.\n`, bins)[1], // the brief uses hub as a CLI of its own
      kept(`${text}Run \`hub --repo x\`.\n`, bins)[1], // hub followed by an option
      kept('- `run reload` swaps config.\n', bins)[0], // the brief never names the project CLI
      kept(text, {})[0], // no project bins
    ],
    ['hub issue', 'hub issue', 'run reload', 'run reload'],
  );
  // a well-known CLI is never prefixed
  assert.deepEqual(kept('Built on `code-forge`: `git status`, `npm view`, `ls src`, `gh issue`, `run reload`.\n', bins), ['git status', 'npm view', 'ls src', 'gh issue', 'code-forge run reload']);
  // only the BRIEF can name the project CLI, and only as a whole word — never a source, a path or a file name
  const claim = '- `run reload` swaps config.\n';
  const opts = (/** @type {string} */ brief) => ({ projectBins: ['code-forge'], briefText: brief });
  assert.deepEqual(
    [
      kept(`${claim}Uses code-forge.\n`, opts(claim))[0], // named in a source only
      kept(`${claim}Edit .code-forge.yml first.\n`, opts(`${claim}Edit .code-forge.yml first.\n`))[0],
      kept(`${claim}See code-forge.config and ./bin/code-forge.\n`, opts(`${claim}See code-forge.config and ./bin/code-forge.\n`))[0],
      kept(`${claim}Built on code-forge.\n`, opts(`${claim}Built on code-forge.\n`))[0], // a sentence's full stop is fine
    ],
    ['run reload', 'run reload', 'run reload', 'code-forge run reload'],
  );
});

test('B50 facts verb on a project whose package.json bin has code-forge: the packet asks about `code-forge run reload`, the argv has no --allowedTools, the sheet shows the full claim', async () => {
  const repo = freshDir('facts-bin');
  writeFileSync(path.join(repo, 'package.json'), JSON.stringify({ name: '@codedology/code-forge', bin: { 'code-forge': 'bin/code-forge.mjs' } }));
  const brief = writeIn(repo, 'brief.md', 'Autopilot builds on `code-forge`.\n- `run reload` (`./src/state/config-snapshot.mjs`) swaps config but has no revert or expiry.\n');
  const row = { fact_id: 'F1', claim: 'code-forge run reload', kind: 'command', command: 'which code-forge', output_excerpt: '/usr/local/bin/code-forge', tag: 'VERIFIED', why: null };
  const { deps, records } = fakeDeps({ FAKE_ANSWER: JSON.stringify({ facts: [row] }) });
  const root = freshDir('facts-bin-root');
  const result = await buildFacts({ cfg: CFG, briefPath: brief, outPath: path.join(root, 'b.facts.md'), runRoot: root, projectDir: repo }, deps);
  assert.equal(result.status, 'ok');
  assert.deepEqual(result.claims, [{ token: 'code-forge run reload', kind: 'command' }, { token: './src/state/config-snapshot.mjs', kind: 'path' }]);
  const spawned = readRecords(records);
  const packet = Buffer.from(spawned[0].stdin_b64, 'base64').toString('utf8');
  assert.equal(packet.split('\n').filter((l) => l === '- F1 command: code-forge run reload').length, 1);
  assert.equal(packet.includes('command: run reload'), false);
  assert.equal(spawned[0].argv.filter((t) => t === '--allowedTools' || t === '--allowed-tools').length, 0);
  assert.deepEqual(result.facts?.map((f) => [f.fact_id, f.claim, f.tag]), [['F1', 'code-forge run reload', 'VERIFIED'], ['F2', './src/state/config-snapshot.mjs', 'UNVERIFIABLE']]);
  assert.deepEqual(readProjectBins(repo), ['code-forge']);
});

test('B50 facts verb from a subfolder: the root config\'s project.slug gets the row, the sheet lands in the root plans dir', async () => {
  const ws = freshDir('facts-sub');
  mkdirSync(path.join(ws, '.git'));
  writeFileSync(path.join(ws, '.code-forge.yml'), `${readFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), 'utf8')}project:\n  slug: root-slug\n`);
  copyFileSync(BRIEF, path.join(ws, 'brief.md'));
  const sub = path.join(ws, 'docs', 'notes');
  mkdirSync(sub, { recursive: true });
  mkdirSync(path.join(ws, 'src'));
  writeFileSync(path.join(ws, 'src', 'reviewer.mjs'), "argv.push('--json-schema', schema);\n");
  const { deps } = fakeDeps({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  const stdout = sink();
  assert.equal(await runFactsVerb(['--brief', '../../brief.md', '--sources', '../../src'], { ...deps, stdout, cwd: sub }), 0);
  assert.deepEqual(JSON.parse(stdout.text()), { out: path.join(ws, 'plans', 'brief.facts.md'), claims: 7, verified: 3 });
  const { readAllRows } = await import('../../src/ledger/write.mjs');
  assert.deepEqual([(await readAllRows('root-slug')).map((r) => r.event), (await readAllRows('notes')).length], [['session', 'facts.built'], 0]);
});

test('a delegate answer with a write verb in command: that row alone is UNVERIFIABLE (not counted), the sheet is written, 1 downgrade', async () => {
  const ws = freshDir('facts-write');
  const out = path.join(ws, 'brief.facts.md');
  const { deps } = fakeDeps({ FAKE_ANSWER: JSON.stringify(answerWith(1, { command: 'touch notes.txt' })) });
  const result = await buildFacts({ cfg: CFG, briefPath: BRIEF, outPath: out, ...srcProject('facts-write-project') }, deps);
  const why = 'check command not allowed: not an allowed read-only form: "touch"; not counted';
  assert.deepEqual(result.downgraded, [{ fact_id: 'F2', reason: why }]);
  assert.deepEqual([result.facts?.[1].tag, result.facts?.[1].why, result.facts?.filter((f) => f.tag === 'VERIFIED').length], ['UNVERIFIABLE', why, 2]);
  const sheet = readFileSync(out, 'utf8');
  assert.equal(sheet.split('\n').filter((l) => l === '- claims: 7 (VERIFIED 2 · NOT-FOUND 2 · UNVERIFIABLE 3)').length, 1);
});

test('readOnlyViolation is an allow-list: 7 escapes are refused, plain reads pass', () => {
  const cases = [
    ['ls\nrm -rf x', ONE],
    ['rm -rf x', 'not an allowed read-only form: "rm"'],
    ['git -C . commit -m x', 'git: only log, show, status, rev-parse, ls-files or cat-file, with no global option'],
    ['npm --prefix . install', 'npm: only view or ls'],
    ['node -p 1', 'not an allowed read-only form: "node"'],
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
  const set = validateFacts(answerWith(6, { tag: 'VERIFIED', output_excerpt: 'exit 0' }), claims, IN_SNAP).facts;
  assert.deepEqual([set[6].tag, set[6].output_excerpt], ['VERIFIED', '']);
  const bad = validateFacts(answerWith(6, { command: 'printenv DO_NOT_TRACK', output_excerpt: '1', tag: 'VERIFIED' }), claims, IN_SNAP);
  const why = 'an env claim may only be checked with printenv DO_NOT_TRACK >/dev/null; not counted';
  assert.deepEqual([bad.facts[6].tag, bad.facts[6].why, bad.facts[6].output_excerpt, bad.downgraded], ['UNVERIFIABLE', why, '', [{ fact_id: 'F7', reason: why }]]);
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

test('a VERIFIED row with an empty excerpt, or with no command, is downgraded to UNVERIFIABLE (2 reasons); the fixture answer has 0 downgrades', () => {
  const claims = extractClaims(readFileSync(BRIEF, 'utf8'));
  const empty = validateFacts(answerWith(0, { output_excerpt: '' }), claims, IN_SNAP);
  assert.deepEqual([empty.facts[0].tag, empty.facts[0].why, empty.downgraded], ['UNVERIFIABLE', 'VERIFIED with an empty output excerpt; not counted', [{ fact_id: 'F1', reason: 'VERIFIED with an empty output excerpt; not counted' }]]);
  const none = validateFacts(answerWith(2, { command: '' }), claims, IN_SNAP);
  assert.deepEqual(none.downgraded, [{ fact_id: 'F3', reason: 'NOT-FOUND without a check command; not counted' }]);
  const ok = validateFacts(ANSWER, claims, IN_SNAP);
  assert.deepEqual([ok.facts.length, ok.downgraded], [7, []]);
});

test('echoedClaim strips only a leading "<kind>: " and surrounding backticks (8 cases)', () => {
  const cases = [
    ['path: ./src/x.mjs', 'path', './src/x.mjs'],
    ['`./src/x.mjs`', 'path', './src/x.mjs'],
    ['path: `./src/x.mjs`', 'path', './src/x.mjs'],
    ['`path: ./src/x.mjs`', 'path', './src/x.mjs'],
    ['command: code-forge review', 'command', 'code-forge review'],
    ['./src/x.mjs', 'path', './src/x.mjs'],
    ['flag: --max', 'path', 'flag: --max'],
    ['path: path: ./x', 'path', 'path: ./x'],
  ];
  assert.deepEqual(cases.map(([claim, kind]) => [claim, kind, echoedClaim(claim, kind)]), cases);
});

test('rows match by echoed claim + kind or by fact_id + echoed claim; the canonical token and kind are shown; a row matching neither is "no answer"', () => {
  const claims = /** @type {Array<{token: string, kind: 'path'|'command'|'flag'}>} */ ([{ token: './src/worker/loop.mjs', kind: 'path' }, { token: 'code-forge review', kind: 'command' }, { token: '--max', kind: 'flag' }]);
  const row = (/** @type {Record<string, unknown>} */ r) => ({ command: 'ls ./src/worker/loop.mjs', output_excerpt: './src/worker/loop.mjs', tag: 'VERIFIED', why: null, ...r });
  const { facts, downgraded } = validateFacts({
    facts: [
      row({ fact_id: 'F9', claim: 'path: `./src/worker/loop.mjs`', kind: 'path' }),
      row({ fact_id: 'F2', claim: '`code-forge review`', kind: 'path', command: 'which code-forge', output_excerpt: '/usr/local/bin/code-forge' }),
      row({ fact_id: 'F7', claim: '--max', kind: 'path', command: 'claude --help | grep -c -- --max', output_excerpt: '1' }),
    ],
  }, claims);
  assert.deepEqual(facts.map((f) => [f.fact_id, f.claim, f.kind, f.tag, f.command]), [
    ['F1', './src/worker/loop.mjs', 'path', 'VERIFIED', 'ls ./src/worker/loop.mjs'],
    ['F2', 'code-forge review', 'command', 'VERIFIED', 'which code-forge'],
    ['F3', '--max', 'flag', 'UNVERIFIABLE', ''],
  ]);
  assert.deepEqual([facts[2].why, downgraded], ['no answer from the delegate', []]);
});

test('facts verb on the real answer shape ("<kind>: <token>" echoes): exact sheet rows, header counts, 2 downgrades on stderr, exit 0', async () => {
  const ws = freshDir('facts-real-shape');
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), path.join(ws, '.code-forge.yml'));
  writeIn(ws, 'brief.md', 'The loop lives in ./src/worker/loop.mjs. Run `code-forge review` with --max-rounds or --max. Notes: ./docs/a.md.\n');
  const r = (/** @type {string} */ id, /** @type {string} */ claim, /** @type {string} */ kind, /** @type {string} */ command, /** @type {string} */ excerpt) => ({ fact_id: id, claim, kind, command, output_excerpt: excerpt, tag: 'VERIFIED', why: null });
  const answer = {
    facts: [
      r('F1', 'path: ./src/worker/loop.mjs', 'path', 'ls ./src/worker/loop.mjs', './src/worker/loop.mjs'),
      r('F2', 'command: code-forge review', 'command', 'which code-forge', '/usr/local/bin/code-forge'),
      r('F3', 'flag: `--max-rounds`', 'flag', 'grep -rn -- --max-rounds src', "src/cli/review.mjs:40:  '--max-rounds': { value: true },"),
      r('F4', 'flag: --max', 'flag', 'grep -rn -- --max src', "src/cli/review.mjs:40:  '--max-rounds': { value: true },"),
      r('F5', 'path: ./docs/a.md', 'path', 'test -e ./docs/a.md', 'exists'),
    ],
  };
  mkdirSync(path.join(ws, 'src', 'cli'), { recursive: true });
  writeFileSync(path.join(ws, 'src', 'cli', 'review.mjs'), "'--max-rounds': { value: true },\n");
  const { deps, stderr } = fakeDeps({ FAKE_ANSWER: JSON.stringify(answer) });
  const stdout = sink();
  const code = await runFactsVerb(['--brief', 'brief.md', '--out', 'brief.facts.md', '--sources', 'src'], { ...deps, stdout, cwd: ws });
  assert.equal(code, 0);
  const out = path.join(ws, 'brief.facts.md');
  assert.deepEqual(JSON.parse(stdout.text()), { out, claims: 5, verified: 3 });
  // F4: the hit line holds `--max-rounds`, not `--max` as a whole token
  const notAllowed = 'VERIFIED source grep without a file:line: hit in a source file naming the claimed flag (docs, plans, tests, fixtures, text files and the brief do not count); not counted';
  const silent = 'silent command; excerpt cannot be its output; not counted';
  assert.deepEqual(stderr.text().split('\n').filter((l) => l.startsWith('facts:')), [`facts: F4 not counted — ${notAllowed}`, `facts: F5 not counted — ${silent}`]);
  const sheet = readFileSync(out, 'utf8');
  assert.deepEqual(sheet.split('\n').filter((l) => l.startsWith('| F') || l.startsWith('- claims:')), [
    '- claims: 5 (VERIFIED 3 · NOT-FOUND 0 · UNVERIFIABLE 2)',
    '| F1 | VERIFIED | path | `./src/worker/loop.mjs` | `ls ./src/worker/loop.mjs` | ./src/worker/loop.mjs | — |',
    '| F2 | VERIFIED | command | `code-forge review` | `which code-forge` | /usr/local/bin/code-forge | — |',
    "| F3 | VERIFIED | flag | `--max-rounds` | `grep -rn -- --max-rounds src` | src/cli/review.mjs:40:  '--max-rounds': { value: true }, | — |",
    `| F4 | UNVERIFIABLE | flag | \`--max\` | \`grep -rn -- --max src\` | src/cli/review.mjs:40:  '--max-rounds': { value: true }, | ${notAllowed} |`,
    `| F5 | UNVERIFIABLE | path | \`./docs/a.md\` | \`test -e ./docs/a.md\` | exists | ${silent} |`,
  ]);
  assert.deepEqual(parseSheet(sheet)?.facts.map((f) => f.claim), ['./src/worker/loop.mjs', 'code-forge review', '--max-rounds', '--max', './docs/a.md']);
});

test('B50 no --help, -h or --version form anywhere and one command per row: 14 forms are refused (pipes and lists included, ls | wc too)', () => {
  const no = (/** @type {string} */ cli) => `not an allowed read-only form: "${cli}"`;
  const cases = [
    ['claude --help', no('claude')],
    ['code-forge -h', no('code-forge')],
    ['gh --version', no('gh')],
    ['rm foo --help', no('rm')],
    ['node --version', no('node')],
    ['node -v', no('node')],
    ['npm --version', 'npm: only view or ls'],
    ['git --version', 'git: only log, show, status, rev-parse, ls-files or cat-file, with no global option'],
    ['make --help', 'make: build and task runners are not allowed'],
    ['code-forge review --help | grep -c -- --max-rounds', ONE],
    ['claude --help | grep -c plugin', ONE],
    ['gh -h && ls', ONE],
    ['ls src | wc -l', ONE],
    ['ls src || ls lib', ONE],
    ['ls src & ls lib', ONE],
  ];
  assert.deepEqual(cases.map(([cmd]) => [cmd, readOnlyViolation(cmd)]), cases);
  const answer = { facts: [{ fact_id: 'F1', claim: '--max', kind: 'flag', command: 'code-forge review --help | grep -c -- --max', output_excerpt: '1', tag: 'VERIFIED', why: null }] };
  assert.deepEqual(validateFacts(answer, [{ token: '--max', kind: 'flag' }]).downgraded, [{ fact_id: 'F1', reason: `check command not allowed: ${ONE}; not counted` }]);
});

test('a row the delegate already tagged UNVERIFIABLE keeps its own why and is not listed as downgraded, whatever its command', () => {
  const claims = extractClaims(readFileSync(BRIEF, 'utf8'));
  const { facts, downgraded } = validateFacts(answerWith(1, { command: 'rm notes.txt', tag: 'UNVERIFIABLE', why: 'the help page is ambiguous' }), claims, IN_SNAP);
  assert.deepEqual([facts[1].tag, facts[1].why, downgraded], ['UNVERIFIABLE', 'the help page is ambiguous', []]);
});

test('the packet suggests only forms the allow-list admits (4), lists the literal-output rule, and asks for the token alone', () => {
  const claims = [{ token: 'code-forge review', kind: /** @type {'command'} */ ('command') }];
  const packet = buildFactsPacket(claims).replace(/\n/g, ' ');
  const list = /run the cheapest READ-ONLY check \((.*?)\)\. Quote/.exec(packet);
  const forms = [...(list?.[1] ?? '').matchAll(/`([^`]+)`/g)].map((m) => m[1]);
  const concrete = forms.map((f) => f.replace('<cli>', 'code-forge').replace('<subcommand>', 'review').replace('<flag>', '--max').replace('<path>', './src/x.mjs').replace('<name>', '@types/node'));
  assert.deepEqual(concrete, ['grep -rn -- --max ./src/x.mjs', 'which code-forge', 'ls ./src/x.mjs', 'npm view @types/node version']);
  assert.deepEqual(concrete.map((c) => readOnlyViolation(c)), [null, null, null, null]);
  // B50: no `--help` form is suggested, no pipe anywhere in the packet; flags are checked in the source text
  assert.deepEqual([packet.split('--help').length - 1, packet.includes('| grep'), packet.includes('-h|'), packet.includes('node --version')], [1, false, false, false]);
  assert.equal(packet.includes('Never run a program with `--help`, `-h` or `--version`: a version is checked with `npm view <name> version`, `which` or `ls`.'), true);
  assert.equal(packet.includes('A flag is checked in the source text of the project, never by running a program: `grep -rn -- <flag> <path>`'), true);
  assert.equal(packet.includes('VERIFIED only with a hit line (`file:line:text`) from a source file (not Markdown or text, not docs, plans, tests or fixtures) that contains the flag, as the excerpt.'), true);
  assert.equal(packet.includes("Never infer, never guess: the excerpt is the command's literal output, never your reading of it."), true);
  assert.equal(packet.includes('For a path use `ls <path>` (it prints the path); `test` prints nothing, so a `test` check can never be VERIFIED.'), true);
});

test('a reworded claim with the right fact_id and kind matches its claim; the sheet shows the canonical token', () => {
  const claims = /** @type {Array<{token: string, kind: 'path'|'flag'}>} */ ([{ token: './src/worker/loop.mjs', kind: 'path' }, { token: '--max', kind: 'flag' }]);
  const { facts } = validateFacts({
    facts: [
      { fact_id: 'F1', claim: 'the worker loop file', kind: 'path', command: 'ls ./src/worker/loop.mjs', output_excerpt: './src/worker/loop.mjs', tag: 'VERIFIED', why: null },
      { fact_id: 'F2', claim: 'max flag', kind: 'path', command: 'ls x', output_excerpt: 'x', tag: 'VERIFIED', why: null },
    ],
  }, claims);
  assert.deepEqual(facts.map((f) => [f.fact_id, f.claim, f.kind, f.tag, f.why]), [
    ['F1', './src/worker/loop.mjs', 'path', 'VERIFIED', null],
    ['F2', '--max', 'flag', 'UNVERIFIABLE', 'no answer from the delegate'],
  ]);
});

test('the sheet claim token is redacted: a registered secret inside a claim shows 0 times (answered and unanswered rows)', () => {
  const secret = 'sk-ant-FAKE0123456789abcdef';
  registerSecret(secret);
  try {
    const claims = /** @type {Array<{token: string, kind: 'path'}>} */ ([{ token: `./${secret}/a.md`, kind: 'path' }, { token: `./${secret}/b.md`, kind: 'path' }]);
    const { facts } = validateFacts({ facts: [{ fact_id: 'F1', claim: `path: ./${secret}/a.md`, kind: 'path', command: 'ls ./docs', output_excerpt: 'a.md', tag: 'VERIFIED', why: null }] }, claims);
    assert.deepEqual(facts.map((f) => [f.claim, f.tag]), [['./[REDACTED]/a.md', 'VERIFIED'], ['./[REDACTED]/b.md', 'UNVERIFIABLE']]);
    assert.equal(JSON.stringify(facts).split(secret).length - 1, 0);
  } finally {
    clearSecrets();
  }
});

test('downgradeLine: the reason is redacted and stripped of control characters, a bad fact_id prints as F?', () => {
  const secret = 'sk-ant-FAKE0123456789abcdef';
  registerSecret(secret);
  try {
    assert.deepEqual(
      [downgradeLine({ fact_id: 'F1\nfacts: F2', reason: `bad\ncheck ${secret}\r` }), downgradeLine({ fact_id: 'F12', reason: 'silent\u0007 command' })],
      ['facts: F? not counted — bad check [REDACTED] \n', 'facts: F12 not counted — silent  command\n'],
    );
  } finally {
    clearSecrets();
  }
});

test('each answer row answers at most one claim: row F1 echoing F2\'s claim stays with F1 (by id); F2 is "no answer"', () => {
  const claims = /** @type {Array<{token: string, kind: 'path'}>} */ ([{ token: './a.md', kind: 'path' }, { token: './b.md', kind: 'path' }]);
  const { facts } = validateFacts({ facts: [{ fact_id: 'F1', claim: 'path: ./b.md', kind: 'path', command: 'ls ./b.md', output_excerpt: './b.md', tag: 'VERIFIED', why: null }] }, claims);
  assert.deepEqual(facts.map((f) => [f.fact_id, f.claim, f.tag, f.command, f.why]), [
    ['F1', './a.md', 'VERIFIED', 'ls ./b.md', null],
    ['F2', './b.md', 'UNVERIFIABLE', '', 'no answer from the delegate'],
  ]);
});

test('launchers with a subcommand and --help are refused alone too (npx, env, sudo, xargs: 4)', () => {
  const cases = [
    ['npx tsc --help', 'npx: build and task runners are not allowed'],
    ['env foo --help', 'not an allowed read-only form: "env"'],
    ['sudo rm --help', 'not an allowed read-only form: "sudo"'],
    ['xargs rm --help', 'not an allowed read-only form: "xargs"'],
  ];
  assert.deepEqual(cases.map(([cmd]) => [cmd, readOnlyViolation(cmd)]), cases);
});

test('silent commands: 9 allow-listed forms that print nothing on success are silent, 5 that print are not', () => {
  const cases = [
    ['test -f x', true],
    ['grep -q x f', true],
    ['grep -cq x f', true],
    ['grep --quiet x f', true],
    ['grep --silent x f', true],
    ['rg -q x', true],
    ['rg --quiet x', true],
    ['which -s code-forge', true],
    ['ls x | grep -q x', true],
    ['ls x', false],
    ['grep -c -- -q f', false],
    ['grep -s x f', false],
    ['test -e x | wc -l', false],
    ['ls x; test -e x', false],
  ];
  assert.deepEqual(cases.map(([cmd]) => [cmd, isSilentCommand(String(cmd))]), cases);
});

test('a VERIFIED test -e row is downgraded as silent (exact reason, 1 downgrade)', () => {
  const claims = /** @type {Array<{token: string, kind: 'path'}>} */ ([{ token: './a.md', kind: 'path' }]);
  const { facts, downgraded } = validateFacts({ facts: [{ fact_id: 'F1', claim: './a.md', kind: 'path', command: 'test -e ./a.md', output_excerpt: 'found', tag: 'VERIFIED', why: null }] }, claims);
  const why = 'silent command; excerpt cannot be its output; not counted';
  assert.deepEqual([facts[0].tag, facts[0].why, downgraded], ['UNVERIFIABLE', why, [{ fact_id: 'F1', reason: why }]]);
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
    ['grep -rn secret src', 'grep -rn: only grep -rn -- <--long-flag> <path> (one relative subdirectory or file, never . or the root)'],
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

test('the <cli> --help case is gone: path verbs, the help subcommand, build runners and every bare <cli> --help|-h|--version are refused (9)', () => {
  const cases = [
    ['./x --help', 'a verb may not be a path'],
    ['/tmp/x --version', 'a verb may not be a path'],
    ['claude help', 'not an allowed read-only form: "claude"'],
    ['make help', 'make: build and task runners are not allowed'],
    ['just --help', 'just: build and task runners are not allowed'],
    ['rake --version', 'rake: build and task runners are not allowed'],
    ['claude --help', 'not an allowed read-only form: "claude"'],
    ['codex -h', 'not an allowed read-only form: "codex"'],
    ['grok --version', 'not an allowed read-only form: "grok"'],
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
