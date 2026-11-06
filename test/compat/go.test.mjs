import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { isolatedEnv } from '../helpers/parity.mjs';

function goEnv(parentEnv = process.env) {
  // The test preload moves HOME into a temporary directory; keep Go's cache outside it.
  const home = parentEnv.CODE_FORGE_REAL_HOME ?? os.homedir();
  const cache = path.join(home, process.platform === 'darwin' ? 'Library/Caches' : '.cache', 'go-build');
  return isolatedEnv({ ...parentEnv, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off', GOCACHE: cache });
}

test('Go commands disable CGO for unset and enabled caller environments', async (t) => {
  for (const cgo of [undefined, '1']) {
    await t.test(`caller CGO_ENABLED=${cgo ?? 'unset'}`, (t) => {
      const parentEnv = { ...process.env };
      if (cgo === undefined) delete parentEnv.CGO_ENABLED;
      else parentEnv.CGO_ENABLED = cgo;
      const result = spawnSync('go', ['env', 'CGO_ENABLED'], { encoding: 'utf8', env: goEnv(parentEnv) });
      if (result.error?.['code'] === 'ENOENT') return t.skip('Go compatibility tests skipped: go is not on PATH');
      assert.equal(result.status, 0, `go env CGO_ENABLED failed: ${result.error ?? result.stderr}`);
      assert.equal(result.stdout.trim(), '0', `Go must disable CGO when the caller has CGO_ENABLED=${cgo ?? 'unset'}`);
    });
  }
});

test('Go canon, sign, ticket and ledger match every shared golden case', (t) => {
  const version = spawnSync('go', ['version'], { encoding: 'utf8', env: goEnv() });
  if (version.error?.['code'] === 'ENOENT') {
    t.skip('Go compatibility tests skipped: go is not on PATH');
    return;
  }
  assert.equal(version.status, 0, `go version failed: ${version.error ?? version.stderr}`);
  const result = spawnSync('go', ['test', '-count=1', '-json', './...'], {
    cwd: fileURLToPath(new URL('../../native/', import.meta.url)),
    encoding: 'utf8', timeout: 300_000, env: goEnv(),
  });
  const events = (result.stdout ?? '').split('\n').filter(Boolean).map((line) => JSON.parse(line));
  const failures = events.filter((event) => event.Action === 'fail' || event.Action === 'output' && /got |want |FAIL/.test(event.Output));
  assert.equal(result.status, 0, `Go golden assertions failed:\n${JSON.stringify(failures, null, 2)}\n${result.error ?? ''}\n${result.stderr ?? ''}`);
  const packages = events.filter((event) => event.Action === 'pass' && !event.Test).map((event) => event.Package.split('/').at(-1)).sort();
  // Every package that has test files must run and pass; the core ten must always be among them.
  const listed = spawnSync('go', ['list', '-f', '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}', './...'], {
    cwd: fileURLToPath(new URL('../../native/', import.meta.url)), encoding: 'utf8', timeout: 300_000, env: goEnv(),
  });
  assert.equal(listed.status, 0, `go list failed: ${listed.stderr}`);
  const tested = listed.stdout.split('\n').filter(Boolean).map((p) => p.split('/').at(-1)).sort();
  assert.deepEqual(packages, tested, 'every Go package with tests must execute and pass');
  for (const core of ['canon', 'code-forge-native', 'config', 'daemon', 'daemonclient', 'ledger', 'queue', 'rebase', 'sign', 'ticket']) {
    assert.ok(packages.includes(core), `core Go package ${core} must execute and pass`);
  }
  assert.equal(events.filter((event) => event.Action === 'pass' && event.Test === 'TestGolden').length, 4, 'all four golden tests must execute');
});
