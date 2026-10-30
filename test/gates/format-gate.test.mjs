import './support.mjs';
import assert from 'node:assert/strict';
import { chmod, mkdir, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { runGates } from '../../src/gates/run.mjs';
import { detectGates } from '../../src/gates/detect.mjs';
import { runGatesVerb } from '../../src/cli/gates.mjs';
import { runReport } from '../../src/cli/report.mjs';
import { buildReport } from '../../src/ledger/report.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { captureStream, git, withTempDir } from './support.mjs';

async function cli(args) {
  const stdout = captureStream(), stderr = captureStream();
  const code = await runGatesVerb(args, { stdout, stderr });
  return { code, stdout: stdout.text, stderr: stderr.text };
}

test('null exit gates explain signals and timeouts with both output tails, and the CLI prints them', async () => {
  await withTempDir(async (dir) => {
    for (const [reason, ending] of [
      ['SIGTERM', "process.kill(process.pid, 'SIGTERM')"],
      ['timeout', 'setInterval(() => {}, 1000)'],
    ]) {
      const script = `console.log('stdout tail'); console.error('stderr tail'); ${ending}`;
      await writeFile(path.join(dir, '.code-forge.yml'), [
        'version: 1', 'project: {slug: format-exits}', 'provider: anthropic',
        'gates:', `  format: ${JSON.stringify([process.execPath, '-e', script])}`, '',
      ].join('\n'));
      const ran = await cli(['run', '--cwd', dir, '--timeout-ms', '1000']);
      assert.equal(ran.code, 1, ran.stderr);
      const results = JSON.parse(ran.stdout).results.filter((r) => !r.skipped);
      assert.equal(results.length, 1, 'exactly the format command ran');
      const [result] = results;
      assert.equal(result.code, null, `${reason} produces a null exit code`);
      assert.equal(result.reason, reason, `${reason} is retained`);
      assert.match(result.message, new RegExp(reason));
      assert.match(result.message, /stdout tail/);
      assert.match(result.message, /stderr tail/);
      assert.match(ran.stderr, new RegExp(`format:.*${reason}`));
      assert.match(ran.stderr, /stdout tail/);
      assert.match(ran.stderr, /stderr tail/);
    }
  });
});

test('a null exit without signal or output explains a missing executable; numeric failures stay numeric', async () => {
  await withTempDir(async (dir) => {
    const ran = await runGates({
      format: [path.join(dir, 'missing-formatter')],
      lint: [process.execPath, '-e', 'process.exit(2)'],
    }, { cwd: dir });
    const format = ran.results.find((r) => r.gate === 'format');
    assert.equal(format.ok, false);
    assert.equal(format.code, null);
    assert.equal(format.reason, 'spawn-error');
    assert.match(format.message, /missing-formatter.*ENOENT/);
    const lint = ran.results.find((r) => r.gate === 'lint');
    assert.equal(lint.code, 2);
    assert.equal(lint.reason, undefined);
    assert.equal(ran.allOk, false);
  });
});

async function formatterFixture(dir, tool) {
  git(['init', '-q'], dir);
  await mkdir(path.join(dir, 'vendor/bin'), { recursive: true });
  await mkdir(path.join(dir, 'fake-bin'));
  const binary = path.join(dir, tool === 'pint' ? 'vendor/bin/pint' : 'fake-bin/npx');
  await writeFile(binary, `#!${process.execPath}\nconsole.log(JSON.stringify(process.argv.slice(2)));\n`);
  await chmod(binary, 0o755);
  await writeFile(path.join(dir, 'vendor/bin/pest'), 'fixture');
  await writeFile(path.join(dir, tool === 'pint' ? 'composer.json' : 'package.json'),
    JSON.stringify({ scripts: { test: 'exit 0' }, devDependencies: { prettier: '*' } }));
  await writeFile(path.join(dir, '.gitignore'), 'vendor/\nfake-bin/\nignored.*\n');
  const ext = tool === 'pint' ? 'php' : 'js';
  const tracked = [`-option.${ext}`, `space name.${ext}`, `unchanged.${ext}`];
  for (const file of [...tracked, `deleted.${ext}`, 'image.bin']) await writeFile(path.join(dir, file), 'fixture');
  git(['add', '.'], dir);
  git(['-c', 'commit.gpgsign=false', 'commit', '-qm', 'base'], dir);
  await rm(path.join(dir, `deleted.${ext}`));
  await writeFile(path.join(dir, `new.${ext}`), 'new fixture');
  await writeFile(path.join(dir, `ignored.${ext}`), 'must not check');
  return { ext, tracked, binary };
}

test('detected Pint and Prettier check tracked files, or only supported changed files, excluding ignored and deleted files', async () => {
  for (const tool of ['pint', 'prettier']) await withTempDir(async (dir) => {
    const { ext, tracked } = await formatterFixture(dir, tool);
    const oldPath = process.env.PATH;
    process.env.PATH = `${path.join(dir, 'fake-bin')}${path.delimiter}${oldPath}`;
    try {
      const format = detectGates(dir).format;
      const prefix = tool === 'pint' ? ['--test'] : ['prettier', '--check', '--ignore-unknown'];
      const full = await runGates({ format }, { cwd: dir });
      const fullResult = full.results.find((r) => r.gate === 'format');
      assert.equal(full.allOk, true, fullResult.stderr);
      const supported = tool === 'pint' ? tracked : [...tracked, 'package.json'].sort();
      assert.deepEqual(JSON.parse(fullResult.stdout), [...prefix, ...supported.map((f) => `./${f}`)], `${tool}: exact tracked paths, never a tree scan`);
      const changed = await runGates({ format }, { cwd: dir, scope: {
        scope: 'changed', files: [],
        changedFiles: [`space name.${ext}`, `new.${ext}`, `ignored.${ext}`, `deleted.${ext}`, 'image.bin'],
      } });
      assert.deepEqual(JSON.parse(changed.results.find((r) => r.gate === 'format').stdout),
        [...prefix, `./new.${ext}`, `./space name.${ext}`], `${tool}: exactly two existing supported changed paths`);
    } finally { process.env.PATH = oldPath; }
  });
});

test('detected formatters batch argv by bytes and retain a middle batch failure, output and total time', async () => {
  for (const tool of ['pint', 'prettier']) await withTempDir(async (dir) => {
    const { ext, tracked, binary } = await formatterFixture(dir, tool);
    const unicode = `unicode-🧪.${ext}`;
    await writeFile(path.join(dir, unicode), 'fixture');
    git(['add', '--', unicode], dir);
    const oldPath = process.env.PATH;
    process.env.PATH = `${path.join(dir, 'fake-bin')}${path.delimiter}${oldPath}`;
    try {
      const format = detectGates(dir).format;
      assert.ok(format && !Array.isArray(format));
      const prefix = format.argv;
      const files = [...tracked, unicode, ...(tool === 'prettier' ? ['package.json'] : [])].sort().map((f) => `./${f}`);
      const argvBytes = (argv) => argv.reduce((sum, arg) => sum + Buffer.byteLength(arg) + 1, 0);
      // Exactly enough for the prefix plus the longest UTF-8 path: one file per batch.
      const formatArgvBudgetBytes = argvBytes(prefix) + Math.max(...files.map((f) => argvBytes([f])));
      for (const changed of [false, true]) for (const fail of [false, true]) {
        await writeFile(binary, `#!${process.execPath}
const args = process.argv.slice(2);
console.log(JSON.stringify(args));
console.error('checked ' + args.at(-1));
setTimeout(() => process.exit(${fail ? "args.includes('./space name." + ext + "') ? 2 : 0" : '0'}), 30);
`);
        const ran = await runGates({ format }, {
          cwd: dir, formatArgvBudgetBytes,
          ...(changed ? { scope: { scope: 'changed', files: [], changedFiles: files.map((f) => f.slice(2)) } } : {}),
        });
        const result = ran.results.find((r) => r.gate === 'format');
        const calls = result.stdout.trim().split('\n').map((line) => JSON.parse(line));
        assert.equal(calls.length, files.length, `${tool}: multiple formatter processes ran`);
        assert.deepEqual(calls.flatMap((args) => args.slice(prefix.length - 1)), files, 'every file checked once in order, including after failure');
        for (const args of calls) {
          assert.deepEqual(args.slice(0, prefix.length - 1), prefix.slice(1), 'every batch repeats the formatter flags');
          assert.ok(argvBytes([prefix[0], ...args]) <= formatArgvBudgetBytes, 'full argv respects the byte budget');
        }
        assert.equal(result.ok, !fail);
        assert.equal(ran.allOk, !fail, 'a later successful batch cannot hide a failure');
        assert.equal(result.code, fail ? 2 : 0, 'retain the failed batch exit status');
        for (const file of files) assert.ok(result.stderr.includes(`checked ${file}\n`), 'stderr tails from every batch are merged');
        assert.ok(result.duration_ms >= 30 * calls.length, 'duration includes every batch');
        assert.equal(ran.duration_ms, result.duration_ms);
      }
    } finally { process.env.PATH = oldPath; }
  });
});

test('an empty detected format scope skips the command while configured commands retain their exact argv', async () => {
  await withTempDir(async (dir) => {
    await formatterFixture(dir, 'pint');
    const format = detectGates(dir).format;
    const empty = await runGates({ format }, { cwd: dir, scope: { scope: 'changed', files: [], changedFiles: ['image.bin', 'ignored.php', 'deleted.php'] } });
    const result = empty.results.find((r) => r.gate === 'format');
    assert.equal(result.skipped, true, 'no supported changed files must never invoke bare pint --test');
    assert.equal(result.reason, 'no_changes');
    assert.equal(result.stdout, '', 'the fake formatter was not executed');
    await writeFile(path.join(dir, '.code-forge.yml'), [
      'version: 1', 'project: {slug: format-configured}', 'provider: anthropic', 'gates:',
      '  test: null', '  lint: null', '  types: null', '  format: [vendor/bin/pint, --test]', '',
    ].join('\n'));
    const configured = await cli(['run', '--cwd', dir]);
    assert.equal(configured.code, 0, configured.stderr);
    const configuredResult = JSON.parse(configured.stdout).results.find((r) => r.gate === 'format');
    assert.equal(configuredResult.skipped, false);
    assert.deepEqual(JSON.parse(configuredResult.stdout), ['--test'], 'user-configured Pint argv is unchanged');
  });
});

test('gate results, red and done rows retain wall milliseconds and totals, and report sums gate time per block', async () => {
  await withTempDir(async (dir) => {
    const rows = [];
    for (const exit of [1, 0]) {
      const slug = `format-timing-${exit}`;
      await writeFile(path.join(dir, '.code-forge.yml'), [
        'version: 1', `project: {slug: ${slug}}`, 'provider: anthropic', 'gates:',
        `  test: ${JSON.stringify([process.execPath, '-e', 'setTimeout(() => {}, 30)'])}`,
        `  format: ${JSON.stringify([process.execPath, '-e', `setTimeout(() => process.exit(${exit}), 60)`])}`,
        `  custom: [{name: audit, argv: ${JSON.stringify([process.execPath, '-e', 'setTimeout(() => {}, 20)'])}}]`, '',
      ].join('\n'));
      const ran = await cli(['run', '--cwd', dir, '--slug', slug]);
      assert.equal(ran.code, exit, ran.stderr);
      const parsed = JSON.parse(ran.stdout);
      assert.equal(parsed.results.length, 5, 'four fixed gates and one custom gate');
      for (const result of parsed.results) {
        assert.equal(Number.isInteger(result.duration_ms), true, `${result.gate} has integer milliseconds`);
        if (result.skipped) assert.equal(result.duration_ms, 0, `${result.gate} was not run`);
        else assert.ok(result.duration_ms >= ({ test: 30, format: 60, audit: 20 })[result.gate], `${result.gate} includes the command wall time`);
      }
      const total = parsed.results.reduce((sum, r) => sum + r.duration_ms, 0);
      assert.equal(parsed.duration_ms, total, 'CLI total equals all per-gate durations');
      const written = (await readAllRows(slug)).filter((r) => r.event.startsWith('gate.'));
      const done = written.filter((r) => r.event === 'gate.done');
      const red = written.filter((r) => r.event === 'gate.red');
      assert.equal(done.length, 1, 'one completion row per invocation');
      assert.equal(done[0].duration_ms, total);
      assert.deepEqual(done[0].results, parsed.results.map(({ gate, ok, skipped, duration_ms }) => ({ gate, ok, skipped, duration_ms })));
      assert.equal(red.length, exit, 'only the failed format gate produces a red row');
      if (exit) {
        assert.equal(red[0].gate, 'format');
        assert.equal(red[0].duration_ms, parsed.results.find((r) => r.gate === 'format').duration_ms);
        assert.equal(red[0].total_duration_ms, total);
      }
      rows.push(...written.map((r) => ({ ...r, run: 'timing-run', block: 'B1' })));
    }
    const blocks = buildReport(rows).sections.cost_per_block;
    assert.equal(blocks.length, 1, 'a block with gate activity appears before spending or close');
    assert.equal(blocks[0].gateTimeMs, rows.filter((r) => r.event === 'gate.done').reduce((sum, r) => sum + r.duration_ms, 0), 'red rows do not double count completed gate runs');
  });
});

test('skipped gates take zero milliseconds and report gate timing excludes shadow, invalid and legacy rows across runs', async () => {
  await withTempDir(async (dir) => {
    const skipped = await runGates({}, { cwd: dir });
    assert.deepEqual(skipped.results.map((r) => [r.gate, r.duration_ms]), [['test', 0], ['lint', 0], ['types', 0], ['format', 0]]);
    assert.equal(skipped.duration_ms, 0);
    const row = (event, run, duration_ms, extra = {}) => ({ event, run, block: 'same', duration_ms, ...extra });
    const blocks = buildReport([
      row('gate.done', 'first', 12), row('gate.red', 'first', 5),
      row('gate.done', 'first', 900, { source: 'shadow' }), row('gate.done', 'first', -1),
      row('gate.done', 'first', Infinity), row('gate.done', 'first', '20'),
      row('gate.done', 'second', 7), row('gate.done', 'second', 0),
      row('block.close', 'legacy', undefined),
    ]).sections.cost_per_block;
    assert.deepEqual(blocks.map((b) => [b.run, b.block, b.gateTimeMs]),
      [['first', 'same', 12], ['second', 'same', 7], ['legacy', 'same', undefined]]);
  });
});

test('report prints summed gate time per block, including zero and unavailable legacy timing', async () => {
  const slug = 'format-timing-report';
  for (const row of [
    { event: 'gate.done', block: 'measured', duration_ms: 2300 },
    { event: 'gate.red', block: 'measured', duration_ms: 1000 },
    { event: 'gate.done', block: 'measured', duration_ms: 1700 },
    { event: 'gate.done', block: 'skipped', duration_ms: 0 },
    { event: 'block.close', block: 'legacy' },
  ]) await appendRow({ ...row, run: 'timing-run' }, { slug });
  const stdout = captureStream(), stderr = captureStream();
  assert.equal(await runReport(['--slug', slug], { stdout, stderr }), 0);
  assert.equal(stderr.text, '');
  const blocks = stdout.text.split('== cost_per_block ==\n')[1].split('== ')[0];
  const details = blocks.split('\n').filter((line) => line.startsWith('  level '));
  assert.deepEqual(details, ['4s', '0s', '-'].map((time) =>
    `  level - · lane - · coder $0.0000 · review $0.0000 · S1+S2 $0.0000 · facts $0.0000 · other $0.0000 · gates ${time} · proof 0s · 0 unknown`));
});
