import assert from 'node:assert/strict';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { runLedger } from '../../src/cli/ledger.mjs';
import { runReport } from '../../src/cli/report.mjs';
import { SECTION_NAMES } from '../../src/ledger/report.mjs';
import { ledgerDir, ledgerPath } from '../../src/ledger/paths.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { assertCleanError, captureStream, withTempHome } from './helpers.mjs';

test('B135: ledger readers refuse absent slugs with sorted, unique known slugs', async () => {
  await withTempHome(async () => {
    await mkdir(ledgerDir(), { recursive: true });
    for (const name of ['z.jsonl', 'a.jsonl', 'a.1.jsonl', 'z.2.jsonl', 'ignored.txt']) {
      await writeFile(path.join(ledgerDir(), name), '');
    }
    for (const sub of ['tail', 'calibration']) {
      const stdout = captureStream(), stderr = captureStream();
      assert.equal(await runLedger([sub, '--slug', 'missing'], { stdout, stderr }), 1, sub);
      assert.deepEqual([stdout.text, stderr.text], ['', `ledger ${sub}: no ledger for slug "missing"; known slugs: a, z\n`]);
    }
  });
});

test('B135 boundary: no ledger directory names none; existing empty ledgers succeed', async () => {
  await withTempHome(async () => {
    for (const sub of ['tail', 'calibration']) {
      const stdout = captureStream(), stderr = captureStream();
      assert.equal(await runLedger([sub, '--slug', 'missing'], { stdout, stderr }), 1, sub);
      assert.equal(stderr.text, `ledger ${sub}: no ledger for slug "missing"; known slugs: none\n`);
      assert.equal(stdout.text, '');
    }
    await mkdir(ledgerDir(), { recursive: true });
    await writeFile(ledgerPath('empty'), '');
    for (const sub of ['tail', 'calibration']) {
      const stdout = captureStream(), stderr = captureStream();
      assert.equal(await runLedger([sub, '--slug', 'empty'], { stdout, stderr }), 0, sub);
      assert.equal(stderr.text, '');
      if (sub === 'tail') assert.equal(stdout.text, '');
      else assert.deepEqual(JSON.parse(stdout.text), buildEmptyCalibration());
    }
  });
});

function buildEmptyCalibration() {
  return { buckets: Object.fromEntries(['0.50-0.60', '0.60-0.70', '0.70-0.80', '0.80-0.85', '0.85-0.90', '0.90-0.95', '0.95-0.99', '0.99-1.00'].map((id) => [id, { correct: 0, wrong: 0, unknown: 0 }])), unbacked: 0, unmapped: 0 };
}

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

test('B31: ledger tail limits the rows with --n and with -n: n=1 -> 1, n=3 -> 3, n=50 on a 25-row ledger -> 25', async () => {
  await withTempHome(async () => {
    for (let i = 0; i < 25; i += 1) await appendRow({ event: 'dispatch', block: `b${i}` }, { slug: 'cli-limit' });
    /** @type {Array<[string, string[]]>} */
    const cases = [
      ['1', ['b24']],
      ['3', ['b22', 'b23', 'b24']],
      ['50', Array.from({ length: 25 }, (_, i) => `b${i}`)],
    ];
    for (const flag of ['--n', '-n']) {
      for (const [n, expected] of cases) {
        const stdout = captureStream();
        const code = await runLedger(['tail', '--slug', 'cli-limit', flag, n], { stdout });
        assert.equal(code, 0, `${flag} ${n}`);
        const lines = stdout.text.split('\n');
        assert.equal(lines.pop(), '', `${flag} ${n}: output ends with a newline`);
        assert.equal(lines.length, expected.length, `${flag} ${n}: row count`);
        assert.deepEqual(lines.map((l) => JSON.parse(l).block), expected, `${flag} ${n}: rows`);
      }
    }
    // No flag keeps the default of 20 rows.
    const stdout = captureStream();
    assert.equal(await runLedger(['tail', '--slug', 'cli-limit'], { stdout }), 0);
    assert.equal(stdout.text.trim().split('\n').length, 20);
  });
});

test('B31: ledger tail with a bad -n (non-integer, negative, missing, or given twice) prints a usage error and exits 2 with no rows', async () => {
  await withTempHome(async () => {
    await appendRow({ event: 'run.start' }, { slug: 'cli-bad-short' });
    /** @type {Array<[string[], string]>} */
    const cases = [
      [['tail', '--slug', 'cli-bad-short', '-n', 'foo'], 'ledger tail: --n/-n must be a non-negative integer, got "foo"\n'],
      [['tail', '--slug', 'cli-bad-short', '-n', '-1'], 'ledger tail: --n/-n must be a non-negative integer, got "-1"\n'],
      [['tail', '--slug', 'cli-bad-short', '-n', '2.5'], 'ledger tail: --n/-n must be a non-negative integer, got "2.5"\n'],
      [['tail', '--slug', 'cli-bad-short', '-n'], 'ledger tail: -n requires a value\n'],
      [['tail', '--slug', 'cli-bad-short', '-n', '--slug'], 'ledger tail: -n requires a value\n'],
      [['tail', '--slug', 'cli-bad-short', '--n', '1', '-n', '2'], 'ledger tail: give the count once, as --n <count> or -n <count>\n'],
    ];
    for (const [args, message] of cases) {
      const stdout = captureStream();
      const stderr = captureStream();
      assert.equal(await runLedger(args, { stdout, stderr }), 2, args.join(' '));
      assert.equal(stderr.text, message, args.join(' '));
      assert.equal(stdout.text, '', args.join(' '));
    }
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

test('report --json prints exactly the SECTION_NAMES keys (16 of them), with run_time appended', async () => {
  await withTempHome(async () => {
    assert.equal(SECTION_NAMES.length, 16, 'SECTION_NAMES must append run_time to the original 15');
    await appendRow({ event: 'dispatch', block: 'b1', level: 'L1', lane: 'L0' }, { slug: 'report-proj' });
    const stdout = captureStream();
    const code = await runReport(['--slug', 'report-proj', '--json'], { stdout });
    assert.equal(code, 0);
    assert.deepEqual(Object.keys(JSON.parse(stdout.text)).sort(), [...SECTION_NAMES].sort());
    assert.deepEqual(JSON.parse(stdout.text).run_time, {
      runs: [], outside_runs: { usd: 0, rows: 1 },
      total: { runs: 0, wallMs: 0, usd: 0, blocks_closed: 0 },
    });
  });
});

test('B147 text: block dollars have readable wall and human wait times; run table includes totals', async () => {
  await withTempHome(async () => {
    const slug = 'report-times';
    const at = (event, time, extra = {}) => ({ event, run: 'r1', ts: `2026-10-20T${time}.000Z`, ...extra });
    for (const row of [
      at('run.start', '00:00:00'),
      at('dispatch', '00:00:00', { block: 'b1' }),
      at('session', '00:01:00', { block: 'b1', usd: 2 }),
      at('block.close', '00:02:00', { block: 'b1' }),
      at('block.awaiting_human_check', '00:02:30', { block: 'b1' }),
      at('block.human_pass', '00:12:30', { block: 'b1' }),
      at('run.end', '00:15:00'),
    ]) await appendRow(row, { slug });
    const stdout = captureStream();
    assert.equal(await runReport(['--slug', slug], { stdout }), 0);
    assert.equal(stdout.text.split('== cost_per_block ==\n')[1].split('== ')[0],
      'run | block | USD | wall | human wait | status\nr1 | b1 | $2.0000 | 12m 30s | 10m 0s | completed\n' +
      '  level - · lane - · coder $0.0000 · review $0.0000 · S1+S2 $0.0000 · facts $0.0000 · other $2.0000 · proof 0s · 0 unknown\n');
    assert.equal(stdout.text.split('== run_time ==\n')[1].split('== ')[0].split('spend per run')[0],
      'run | started | ended | wall | USD | closed | stopped\n' +
      'r1 | 2026-10-20T00:00:00.000Z | 2026-10-20T00:15:00.000Z | 15m 0s | $2.0000 | 1 | 0\n' +
      'total (1 run): 15m 0s | $2.0000 | 1 closed\n');
  });
});

test('B149 clause 3 boundary: untimed stopped blocks and outside-only rows print without a fake run or wall time', async () => {
  await withTempHome(async () => {
    const slug = 'report-times-legacy';
    await mkdir(ledgerDir(), { recursive: true });
    await writeFile(ledgerPath(slug), [
      { event: 'dispatch', run: 'old', block: 'b1', ts: 'bad-ts' },
      { event: 'session', run: 'old', block: 'b1', usd: 0.25 },
      { event: 'block.stop', run: 'old', block: 'b1', ts: 'bad-ts' },
      { event: 'note', run: null, ts: 'bad-ts' },
    ].map((r) => JSON.stringify(r)).join('\n') + '\n');
    const stdout = captureStream();
    assert.equal(await runReport(['--slug', slug], { stdout }), 0);
    assert.equal(stdout.text.split('== cost_per_block ==\n')[1].split('== ')[0],
      'run | block | USD | wall | human wait | status\nold | b1 | $0.2500 | - | - | stopped\n' +
      '  level - · lane - · coder $0.0000 · review $0.0000 · S1+S2 $0.0000 · facts $0.0000 · other $0.2500 · proof 0s · 0 unknown\n');
    assert.equal(stdout.text.split('== run_time ==\n')[1].split('spend per run')[0],
      'run | started | ended | wall | USD | closed | stopped\nold | - | open | - | $0.2500 | 0 | 1\n' +
      'outside runs: $0.0000 (1 rows, no wall time)\n' +
      'total (1 run): 0s | $0.2500 | 0 closed\n');
  });
});

test('B150 clause 1: spend prints one outside-runs line after real runs and before the total', async () => {
  await withTempHome(async () => {
    const slug = 'report-outside-spend';
    for (const row of [
      { event: 'session', usd: 1.0836 },
      { event: 'session', run: 'z-run', usd: 2 },
      { event: 'session', run: null, usd: 0 },
      { event: 'session', run: 'a-run', usd: 0.5 },
    ]) await appendRow(row, { slug });
    const stdout = captureStream(), stderr = captureStream();
    assert.equal(await runReport(['--slug', slug], { stdout, stderr }), 0);
    assert.equal(stderr.text, '');
    assert.equal(stdout.text.split('spend per run (estimated USD, open blocks included):\n')[1].split('run mode:')[0],
      '  run z-run: $2.0000 · 1 session(s)\n' +
      '  run a-run: $0.5000 · 1 session(s)\n' +
      '  outside runs: $1.0836 · 2 session(s)\n' +
      '  total: $3.5836 · 4 session(s)\n');
    assert.equal(stdout.text.split('run (none)').length - 1, 0, 'no fake run name anywhere in the report');
  });
});

test('B150 clause 1 boundary: outside-only unknown spend keeps the suffix; no outside spend adds no line', async () => {
  await withTempHome(async () => {
    const slug = 'report-outside-boundary';
    await mkdir(ledgerDir(), { recursive: true });
    const cases = [
      { rows: [{ event: 'session.background' }, { event: 'session', run: null, usd: null }],
        expected: '  outside runs: $0.0000 · 2 session(s) · 2 with unknown price (not counted)\n' +
          '  total: $0.0000 · 2 session(s) · 2 with unknown price (not counted)\n' },
      { rows: [], expected: '  total: $0.0000 · 0 session(s)\n' },
      { rows: [{ event: 'session', run: 'r1', usd: 0 }],
        expected: '  run r1: $0.0000 · 1 session(s)\n  total: $0.0000 · 1 session(s)\n' },
    ];
    for (const { rows, expected } of cases) {
      await writeFile(ledgerPath(slug), rows.map((r) => JSON.stringify(r)).join('\n'));
      const stdout = captureStream(), stderr = captureStream();
      assert.equal(await runReport(['--slug', slug], { stdout, stderr }), 0);
      assert.equal(stderr.text, '');
      assert.equal(stdout.text.split('spend per run (estimated USD, open blocks included):\n')[1].split('run mode:')[0], expected);
      assert.equal(stdout.text.split('run (none)').length - 1, 0);
    }
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
