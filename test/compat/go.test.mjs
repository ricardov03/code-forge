import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

test('Go canon, sign, ticket and ledger match every shared golden case', (t) => {
  const version = spawnSync('go', ['version'], { encoding: 'utf8' });
  if (version.error?.['code'] === 'ENOENT') {
    t.skip('Go compatibility tests skipped: go is not on PATH');
    return;
  }
  assert.equal(version.status, 0, `go version failed: ${version.error ?? version.stderr}`);
  const result = spawnSync('go', ['test', '-count=1', '-json', './...'], {
    cwd: fileURLToPath(new URL('../../native/', import.meta.url)),
    encoding: 'utf8', timeout: 120_000, env: { ...process.env, GOTOOLCHAIN: 'local', GOPROXY: 'off' },
  });
  const events = (result.stdout ?? '').split('\n').filter(Boolean).map((line) => JSON.parse(line));
  const failures = events.filter((event) => event.Action === 'fail' || event.Action === 'output' && /got |want |FAIL/.test(event.Output));
  assert.equal(result.status, 0, `Go golden assertions failed:\n${JSON.stringify(failures, null, 2)}\n${result.stderr ?? result.error}`);
  const packages = events.filter((event) => event.Action === 'pass' && !event.Test).map((event) => event.Package.split('/').at(-1)).sort();
  assert.deepEqual(packages, ['canon', 'daemon', 'ledger', 'queue', 'sign', 'ticket'], 'all six Go test packages must execute and pass');
  assert.equal(events.filter((event) => event.Action === 'pass' && event.Test === 'TestGolden').length, 4, 'all four golden tests must execute');
});
