import assert from 'node:assert/strict';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { runLedger } from '../../src/cli/ledger.mjs';
import { runReport } from '../../src/cli/report.mjs';
import { SECTION_NAMES } from '../../src/ledger/report.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { assertCleanError, captureStream, withTempHome } from './helpers.mjs';

test('ledger tail prints the last N rows, IN ORDER, as one JSON object per line', async () => {
  await withTempHome(async () => {
    await appendRow({ event: 'run.start' }, { slug: 'cli-proj' });
    await appendRow({ event: 'dispatch', block: 'b1' }, { slug: 'cli-proj' });
    await appendRow({ event: 'run.end' }, { slug: 'cli-proj' });
    const stdout = captureStream();
    const code = await runLedger(['tail', '--slug', 'cli-proj', '--n', '2'], { stdout });
    assert.equal(code, 0);
    const lines = stdout.text.trim().split('\n');
    assert.equal(lines.length, 2);
    assert.deepEqual(lines.map((l) => JSON.parse(l).event), ['dispatch', 'run.end']);
  });
});

test('ledger tail --n 0 prints nothing (not the whole ledger)', async () => {
  await withTempHome(async () => {
    await appendRow({ event: 'run.start' }, { slug: 'cli-zero' });
    const stdout = captureStream();
    const code = await runLedger(['tail', '--slug', 'cli-zero', '--n', '0'], { stdout });
    assert.equal(code, 0);
    assert.equal(stdout.text, '');
  });
});

test('ledger tail rejects a non-integer (including a fraction like "1.5"), or a negative, --n with usage and exit 2', async () => {
  await withTempHome(async () => {
    for (const bad of ['foo', '-1', '1.5']) {
      const stderr = captureStream();
      assert.equal(await runLedger(['tail', '--slug', 'cli-bad', '--n', bad], { stderr }), 2, `--n ${bad} should exit 2`);
      assert.ok(stderr.text.includes('--n'), `--n ${bad}: expected the error to mention --n`);
    }
  });
});

test('ledger with no --slug prints usage and exits 2, under a temp HOME', async () => {
  await withTempHome(async () => {
    const stderr = captureStream();
    const code = await runLedger(['tail'], { stderr });
    assert.equal(code, 2);
    assert.ok(stderr.text.includes('usage:'));
  });
});

test('a --slug value that looks like another flag (e.g. "--slug --scan-git") is rejected, not silently used as the slug — and no ledger file is created for it', async () => {
  await withTempHome(async () => {
    const stderr = captureStream();
    const code = await runLedger(['outcome', '--slug', '--scan-git'], { stderr });
    assert.equal(code, 2);
    assert.ok(stderr.text.includes('usage:'));
    // '--scan-git' is not a valid slug (SLUG_PATTERN forbids '-' as the first char... actually it
    // allows '-' after the first char but the string starts with '-', which SLUG_PATTERN also
    // rejects) — readAllRows would throw on it, proving nothing was silently written under it.
    await assert.rejects(() => readAllRows('--scan-git'));
  });
});

test('optional flags given with NO value (not absent — genuinely dangling) are rejected, not silently defaulted', async () => {
  await withTempHome(async () => {
    const cases = [
      ['outcome', '--scan-git', '--slug', 's', '--cwd'],
      ['outcome', '--scan-git', '--slug', 's', '--days'],
      ['tail', '--slug', 's', '--n'],
      ['calibration', '--slug', 's', '--question'],
    ];
    for (const args of cases) {
      const stderr = captureStream();
      const code = await runLedger(args, { stderr });
      assert.equal(code, 2, `${args.join(' ')} should exit 2`);
      assertCleanError(stderr.text);
    }
  });
});

test('ledger outcome --pr --ci green records exactly 1 outcome row: pr=42, outcome=correct, tokens_source/cost_source present', async () => {
  await withTempHome(async () => {
    const stdout = captureStream();
    const code = await runLedger(['outcome', '--pr', '42', '--ci', 'green', '--slug', 'ci-proj'], { stdout });
    assert.equal(code, 0);

    const rows = await readAllRows('ci-proj');
    const outcomeRows = rows.filter((r) => r.event === 'outcome');
    assert.equal(outcomeRows.length, 1);
    assert.equal(outcomeRows[0].pr, 42);
    assert.equal(outcomeRows[0].outcome, 'correct');
    assert.equal(outcomeRows[0].outcome_source, 'ci');
    assert.ok('tokens_source' in outcomeRows[0]);
    assert.ok('cost_source' in outcomeRows[0]);
  });
});

test('ledger outcome --pr rejects a non-integer PR number and writes NOTHING to the ledger', async () => {
  await withTempHome(async () => {
    const stderr = captureStream();
    const code = await runLedger(['outcome', '--pr', 'abc', '--ci', 'green', '--slug', 'ci-bad'], { stderr });
    assert.equal(code, 2);
    assert.ok(stderr.text.includes('--pr'));
    assert.deepEqual(await readAllRows('ci-bad'), []);
  });
});

test('ledger outcome --scan-git outside a git repo returns a clean error (exit 1), not a raw stack trace', async () => {
  await withTempHome(async (home) => {
    const notARepo = path.join(home, 'not-a-repo');
    await mkdir(notARepo, { recursive: true });
    const stderr = captureStream();
    const code = await runLedger(['outcome', '--scan-git', '--slug', 'no-repo', '--cwd', notARepo], { stderr });
    assert.equal(code, 1);
    assertCleanError(stderr.text);
  });
});

test('report --json prints exactly the SECTION_NAMES keys (13 of them), sorted, matching the module export', async () => {
  await withTempHome(async () => {
    assert.equal(SECTION_NAMES.length, 13, 'SECTION_NAMES itself must stay pinned at 13 — this is the §6.2 requirement, not just self-consistency');
    await appendRow({ event: 'dispatch', block: 'b1', level: 'L1', lane: 'L0' }, { slug: 'report-proj' });
    const stdout = captureStream();
    const code = await runReport(['--slug', 'report-proj', '--json'], { stdout });
    assert.equal(code, 0);
    assert.deepEqual(Object.keys(JSON.parse(stdout.text)).sort(), [...SECTION_NAMES].sort());
  });
});

test('report with no --slug prints usage and exits 2, under a temp HOME', async () => {
  await withTempHome(async () => {
    const stderr = captureStream();
    const code = await runReport([], { stderr });
    assert.equal(code, 2);
    assert.ok(stderr.text.includes('usage:'));
  });
});

test('report --slug .. is rejected with a clean error, not a raw stack trace or a read outside the ledger dir', async () => {
  await withTempHome(async () => {
    const stderr = captureStream();
    const code = await runReport(['--slug', '..'], { stderr });
    assert.equal(code, 1);
    assertCleanError(stderr.text);
  });
});

test('report on a ledger file sitting where a directory is expected (EISDIR) returns a clean error, not a raw stack trace', async () => {
  await withTempHome(async (home) => {
    // Create the ledger DIRECTORY where the file should be, so readFile fails with EISDIR.
    await mkdir(path.join(home, '.code-forge', 'ledger', 'broken.jsonl'), { recursive: true });
    const stderr = captureStream();
    const code = await runReport(['--slug', 'broken'], { stderr });
    assert.equal(code, 1);
    assertCleanError(stderr.text);
  });
});

test('report on a ledger with a malformed MIDDLE line returns a clean error, not a raw stack trace (the EISDIR case above never exercises this path)', async () => {
  await withTempHome(async (home) => {
    const slug = 'corrupt-report';
    await appendRow({ event: 'first' }, { slug });
    await appendRow({ event: 'third' }, { slug });
    const file = path.join(home, '.code-forge', 'ledger', `${slug}.jsonl`);
    const lines = (await readFile(file, 'utf8')).trim().split('\n');
    lines.splice(1, 0, '{not valid json');
    await writeFile(file, `${lines.join('\n')}\n`, 'utf8');
    const stderr = captureStream();
    const code = await runReport(['--slug', slug], { stderr });
    assert.equal(code, 1);
    assertCleanError(stderr.text);
  });
});
