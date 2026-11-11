import { BIN, cli, makeRepo } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { appendFileSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const { assemblePack, ContextError } = await import('../../src/context/pack.mjs');
const version = JSON.parse(readFileSync(new URL('../../package.json', import.meta.url), 'utf8')).version;

test('context pack names exactly the running node, absolute repo bin and installed version before commands', async () => {
  const { repo, runId } = await makeRepo();
  for (const context of ['', 'context: {}\n']) {
    appendFileSync(path.join(repo, '.code-forge.yml'), context);
    const result = await cli(['context', 'pack', '--run', runId, '--block', 'B11', '--json'], repo);
    assert.equal(result.code, 0, result.stderr);
    const lines = readFileSync(result.json.path, 'utf8').trimEnd().split('\n');
    const sections = lines.flatMap((line, index) => line === '## code-forge' ? [index] : []);
    assert.equal(sections.length, 1, 'exactly one code-forge section');
    const index = sections[0];
    const quote = (/** @type {string} */ token) => /^[A-Za-z0-9_@%+=:,./-]+$/.test(token) ? token : `'${token.replaceAll("'", "'\\''")}'`;
    assert.deepEqual(lines.slice(index, index + 4), [
      '## code-forge',
      `run code-forge verbs with exactly: ${quote(process.execPath)} ${quote(BIN)}`,
      `version: ${version}`,
      'do not use another code-forge from PATH',
    ]);
    assert.equal(path.isAbsolute(BIN), true, 'bin path is absolute');
    assert.deepEqual(lines.slice(index - 2, index), ['owned files:', '- src/**'], 'section follows the block header');
    assert.equal(lines[index + 4], context ? '## commands (never run by code-forge)' : '## proof rule (code-forge)');
  }
});

test('assemblePack renders only supplied install values, shell-quotes spaces, and omits the section for legacy callers', () => {
  const options = { settings: null, sources: { rules: [], lessons: null }, block: { run: 'example', block: 'B1', owned: [] } };
  const legacy = assemblePack(options);
  assert.equal(legacy.text.split('\n').filter((line) => line === '## code-forge').length, 0);
  const codeForge = { node: '/node install/bin/node', bin: "/forge's install/bin/code-forge.mjs", version: '9.8.7' };
  const supplied = assemblePack({ ...options, block: { ...options.block, codeForge } });
  const section = [
    '## code-forge',
    "run code-forge verbs with exactly: '/node install/bin/node' '/forge'\\''s install/bin/code-forge.mjs'",
    'version: 9.8.7',
    'do not use another code-forge from PATH',
    '',
  ].join('\n');
  assert.equal(supplied.text.split(section).length - 1, 1, 'exactly one section with the supplied values');
  assert.equal(supplied.text.replace(section, ''), legacy.text, 'legacy header and proof remain identical');
  assert.deepEqual([supplied.lines - legacy.lines, supplied.cut], [4, 0]);
});

test('assemblePack refuses control characters in each code-forge install value', () => {
  for (const field of ['node', 'bin', 'version']) {
    for (const control of ['\n', '\u007f', '\u0085']) {
      const codeForge = { node: '/node', bin: '/bin/code-forge.mjs', version: '9.8.7', [field]: `bad${control}value` };
      assert.throws(() => assemblePack({
        settings: null,
        sources: { rules: [], lessons: null },
        block: { run: 'example', block: 'B1', codeForge },
      }), (error) => error instanceof ContextError && error.code === 'control-character'
        && error.message === `block B1: code-forge ${field} ${JSON.stringify(codeForge[field])} contains a control character`, `${field}: ${JSON.stringify(control)} must be refused`);
    }
  }
});

const FACTS_LINE = '- Facts, not guesses: every cause, pass or fail you report names its proof (the error text, the log path, or the command and its exit code). No proof means "not known yet": get the proof now; never name load, flakiness or old tests as a cause without it. No work stops because of a guess.';

test('B362: Node and Go packs are byte-equal and end the proof rule with the facts-not-guesses bullet', (t) => {
  const root = fileURLToPath(new URL('../../', import.meta.url));
  const cliPath = path.join(root, 'bin/code-forge.mjs');
  const scratch = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'b362-pack-')));
  t.after(() => rmSync(scratch, { recursive: true, force: true }));
  const env = (/** @type {Record<string, string>} */ extra) => {
    /** @type {Record<string, string | undefined>} */
    const out = { ...process.env, ...extra };
    for (const key of ['CODE_FORGE_RUN_ID', 'CODE_FORGE_OWNER_PID', 'CODE_FORGE_OWNER_START_TIME', 'CODE_FORGE_RUN', 'CODE_FORGE_BLOCK']) delete out[key];
    return out;
  };
  // A private Go binary laid out as in the npm package, so it names the same node and bin as Node.
  const pkg = path.join(scratch, 'package');
  const binary = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
  mkdirSync(path.dirname(binary), { recursive: true });
  mkdirSync(path.join(pkg, 'bin'));
  symlinkSync(cliPath, path.join(pkg, 'bin/code-forge.mjs'));
  const build = spawnSync('go', ['build', '-trimpath', '-ldflags', `-X main.version=${version}`, '-o', binary, './cmd/code-forge-native'], {
    cwd: path.join(root, 'native'), encoding: 'utf8', timeout: 180_000, env: env({ CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off' }),
  });
  assert.equal(build.status, 0, 'go build: ' + build.stderr);
  const repo = path.join(scratch, 'repo');
  const home = path.join(scratch, 'home');
  mkdirSync(path.join(home, '.code-forge/runs'), { recursive: true });
  mkdirSync(repo);
  assert.equal(spawnSync('git', ['init', '-q', repo], { env: env({}), encoding: 'utf8' }).status, 0, 'git init');
  const tmpRoot = path.join(repo, '.code-forge/tmp');
  mkdirSync(path.join(tmpRoot, 'sample'), { recursive: true, mode: 0o700 });
  writeFileSync(path.join(repo, '.code-forge.yml'), JSON.stringify({ version: 1, tmp: { root: tmpRoot } }));
  writeFileSync(path.join(home, '.code-forge/runs/sample.key'), Buffer.alloc(32, 7), { mode: 0o600 });
  writeFileSync(path.join(home, '.code-forge/runs/sample.json'), JSON.stringify({ run_id: 'sample', project: 'sample', workspace: repo, blocks: { B1: {
    level: 'L2', owned_files: ['src/a.mjs'], acceptance: [{ clause: 'works', tests: ['test/a.test.mjs'] }],
  } } }));
  const args = ['context', 'pack', '--run', 'sample', '--block', 'B1'];
  const childEnv = env({ HOME: home, CODE_FORGE_NATIVE: '0', CODE_FORGE_HANDOVER_LOG: '0', CODE_FORGE_NODE: '/nonexistent-b362-node', CODE_FORGE_NO_ERROR_LOG: '1' });
  const go = spawnSync(binary, args, { cwd: repo, encoding: 'utf8', env: childEnv });
  assert.deepEqual([go.status, go.stderr], [0, ''], 'Go pack runs natively');
  const node = spawnSync(process.execPath, [cliPath, ...args], { cwd: repo, encoding: 'utf8', env: childEnv });
  assert.deepEqual([node.status, node.stderr], [0, ''], 'Node pack runs');
  assert.equal(go.stdout, node.stdout, 'Go and Node packs are byte-equal');
  for (const [name, text] of [['Node', node.stdout], ['Go', go.stdout]]) {
    const lines = text.split('\n');
    const proofAt = lines.indexOf('## proof rule (code-forge)');
    const acceptanceAt = lines.indexOf('## acceptance');
    assert.deepEqual([proofAt > 0, acceptanceAt - proofAt, lines[acceptanceAt - 1]], [true, 7, FACTS_LINE], `${name}: six bullets, facts-not-guesses last`);
    assert.equal(lines.filter((line) => line === FACTS_LINE).length, 1, `${name}: the bullet appears exactly once`);
  }
});
