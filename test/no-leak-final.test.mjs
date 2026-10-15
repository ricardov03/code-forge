import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { existsSync, mkdtempSync, rmSync, symlinkSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { test } from 'node:test';
import { TEST_BASE } from './helpers/isolate.mjs';

const ROOT = fileURLToPath(new URL('../', import.meta.url));

test('B115: final leak failure collects both reports before baseline and marker assertions', () => {
  const cases = ['missing', 'invalid', 'valid'].map((baseline) => ({ baseline, suite: `b115-final-fixture-${randomUUID()}` }));
  cases.push({ baseline: 'missing', suite: '' });
  for (const { baseline, suite } of cases) {
    const baselineFile = path.join(TEST_BASE, `no-leak-${createHash('sha256').update(suite).digest('hex')}.json`);
    const fixtureDir = mkdtempSync(path.join(os.tmpdir(), 'b115 final #'));
    try {
      const fixtureUrl = pathToFileURL(path.join(fixtureDir, 'no leak #.test.mjs'));
      symlinkSync(fileURLToPath(new URL('./no-leak.test.mjs', import.meta.url)), fileURLToPath(fixtureUrl));
      // The empty-marker case shares one baseline name across runs: remove a stale one first.
      if (suite === '') rmSync(baselineFile, { force: true });
      const script = `
        import assert from 'node:assert/strict';
        import { createHash } from 'node:crypto';
        import { writeFileSync } from 'node:fs';
        import path from 'node:path';
        import { mock } from 'node:test';
        import * as isolate from ${JSON.stringify(new URL('../test/helpers/isolate.mjs', import.meta.url).href)};
        import * as processes from ${JSON.stringify(new URL('../test/helpers/worker-processes.mjs', import.meta.url).href)};
        process.stdout.write('B115 fixture pid=' + process.pid + '\\n');
        const calls = [];
        const dir = path.join(isolate.GUARDED_DIRS[0], 'fixture-leak');
        mock.module(${JSON.stringify(new URL('../test/helpers/isolate.mjs', import.meta.url).href)}, { namedExports: {
          ...isolate, snapshotGuarded() { calls.push('directories'); return new Set([dir]); },
        }});
        mock.module(${JSON.stringify(new URL('../test/helpers/worker-processes.mjs', import.meta.url).href)}, { namedExports: {
          ...processes, suiteWorkers() { calls.push('workers'); return [{ pid: 31012, run: 'fixture-run' }]; },
        }});
        const baselineFile = path.join(isolate.TEST_BASE, 'no-leak-' + createHash('sha256').update(process.env.CODE_FORGE_TEST_SUITE).digest('hex') + '.json');
        const baseline = ${JSON.stringify(baseline)};
        if (baseline !== 'missing') writeFileSync(baselineFile, baseline === 'invalid' ? '{}' : '[]');
        // Skip only the three-second orphan grace period in this deterministic fixture.
        const timer = mock.method(globalThis, 'setTimeout', (fn) => { fn(); return 0; });
        const baselineError = baseline === 'missing' ? 'no pre-suite snapshot at ' + baselineFile + ': run the snapshot step first' : baseline === 'invalid' ? 'the snapshot is a JSON array of paths' : null;
        const markerError = process.env.CODE_FORGE_TEST_SUITE ? null : 'CODE_FORGE_TEST_SUITE must be non-empty before taking the snapshot or checking workers';
        const message = 'suite leaks: directories ' + JSON.stringify([dir]) + '; leaked pid/run list [{"pid":31012,"run":"fixture-run"}]; baseline: ' + (baselineError ?? 'valid') + '; suite marker: ' + (markerError ?? 'valid') + '; worker report: valid';
        let assertions = 0;
        const original = assert.deepEqual;
        mock.method(assert, 'deepEqual', (actual, expected, diagnostic) => {
          assertions++;
          assert.equal(diagnostic, message);
          original(actual, { dirs: [dir], workers: [{ pid: 31012, run: 'fixture-run' }], baselineError, markerError, workerError: null });
          original(expected, { dirs: [], workers: [], baselineError: null, markerError: null, workerError: null });
        });
        process.once('beforeExit', () => {
          original(calls, ['directories', 'workers']);
          assert.equal(assertions, 1);
          timer.mock.restore();
        });
      `;
      /** @type {NodeJS.ProcessEnv} */
      const env = { ...process.env, CODE_FORGE_NO_LEAK: 'final', CODE_FORGE_TEST_SUITE: suite };
      delete env.NODE_TEST_CONTEXT;
      const result = spawnSync(process.execPath, ['--import', new URL('./helpers/isolate.mjs', import.meta.url).href, '--experimental-test-module-mocks', '--import', 'data:text/javascript,' + encodeURIComponent(script), '--test', '--experimental-test-isolation=none', '--test-name-pattern=^final step', fileURLToPath(fixtureUrl)], {
        cwd: ROOT, encoding: 'utf8', timeout: 20000,
        env,
      });
      assert.equal(result.status, 0, `${baseline}/${suite}: ${result.stdout}${result.stderr}`);
      assert.deepEqual([...result.stdout.matchAll(/B115 fixture pid=(\d+)/g)].map((match) => Number(match[1])), [result.pid], 'the preload and fixture run once in the spawned process');
      assert.equal(existsSync(baselineFile), false, 'final step removes its baseline');
    } finally {
      rmSync(baselineFile, { force: true });
      rmSync(fixtureDir, { recursive: true, force: true });
    }
  }
});
