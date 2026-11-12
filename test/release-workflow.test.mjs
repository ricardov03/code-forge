// The release workflow, the published package and the README's Latest line (B574). The release,
// release-site, docs-block and changelog tools themselves are tested in Go
// (native/cmd/cf-release: byte-for-byte goldens recorded from the retired Node scripts).
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { parse } from 'yaml';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

test('release.yml: one github-release job with exactly {contents: write}; notes come from the Go tool; it never publishes to npm', () => {
  const raw = readFileSync(path.join(REPO, '.github', 'workflows', 'release.yml'), 'utf8');
  assert.equal(existsSync(path.join(REPO, '.github', 'workflows', 'publish.yml')), false);
  const wf = parse(raw);
  assert.deepEqual(wf.on, { push: { tags: ['v*'] } });
  assert.deepEqual(Object.keys(wf.jobs), ['github-release']);
  const job = wf.jobs['github-release'];
  assert.deepEqual(job.permissions, { contents: 'write' });
  for (const word of ['npm publish', 'NPM_TOKEN', 'id-token', 'NODE_AUTH_TOKEN', 'setup-node', 'node scripts/']) {
    assert.equal(raw.includes(word), false, word);
  }
  const goSetup = job.steps.find((st) => typeof st.uses === 'string' && st.uses.startsWith('actions/setup-go@'));
  assert.ok(goSetup, 'the job sets up Go');
  assert.equal(goSetup.with['go-version-file'], 'native/go.mod');
  const runs = job.steps.filter((st) => st.run).map((st) => st.run);
  assert.equal(runs.length, 3);
  assert.match(runs[0], /if \[ "v\$\{version\}" != "\$\{GITHUB_REF_NAME\}" \]; then/);
  assert.equal(runs[1], 'sh scripts/cf-release.sh release notes "$GITHUB_REF_NAME" --out notes.md');
  assert.match(runs[2], /gh release create "\$GITHUB_REF_NAME" --title "\$GITHUB_REF_NAME" --notes-file notes\.md --verify-tag/);
  assert.match(runs[2], /gh release edit "\$GITHUB_REF_NAME"/);
  assert.equal(job.steps.find((st) => st.run === runs[2]).env.GH_TOKEN, '${{ github.token }}');
});

test('npm pack --dry-run ships no release tool but still ships scripts/first-use.mjs, scripts/install.sh (B569) and scripts/setup.mjs (B463)', () => {
  const res = spawnSync('npm', ['pack', '--dry-run', '--json'], { cwd: REPO, encoding: 'utf8', timeout: 120000 });
  assert.equal(res.status, 0, res.stderr);
  const paths = JSON.parse(res.stdout)[0].files.map((/** @type {{path: string}} */ f) => f.path);
  assert.deepEqual(paths.filter((p) => p.startsWith('scripts/')).sort(), ['scripts/first-use.mjs', 'scripts/gen-config-doc.mjs', 'scripts/install.sh', 'scripts/setup.mjs']);
});

test('README has zero version-history lists and one linked Latest line in its first ten lines', () => {
  const text = readFileSync(path.join(REPO, 'README.md'), 'utf8');
  assert.equal([...text.matchAll(/^#+ What's new in \d+\.\d+/gm)].length, 0, 'version-history headings');
  assert.equal([...text.matchAll(/^In (?:\d+\.\d+\.\d+|the next release):/gm)].length, 0, 'release lists');
  const lines = text.split('\n');
  const latest = lines.filter((line) => line.startsWith('Latest:'));
  const version = JSON.parse(readFileSync(path.join(REPO, 'package.json'), 'utf8')).version;
  assert.deepEqual(latest, [`Latest: ${version} - see [CHANGELOG.md](CHANGELOG.md)`]);
  assert.ok(lines.indexOf(latest[0]) < 10, 'Latest belongs in the first ten lines');
});
