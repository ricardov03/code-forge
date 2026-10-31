#!/usr/bin/env node
// Release builds include every supported target. --host keeps local compatibility tests fast.
import { spawnSync } from 'node:child_process';
import { chmodSync, mkdirSync, readFileSync, renameSync, rmSync, statSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const targets = [
  ['darwin', 'arm64'], ['darwin', 'x64'], ['linux', 'x64'], ['linux', 'arm64'],
];

try {
  const args = process.argv.slice(2);
  if (args.length && (args.length !== 1 || args[0] !== '--host')) throw new Error('usage: node scripts/build-native.mjs [--host]');
  const selected = args.length ? targets.filter(([platform, arch]) => platform === process.platform && arch === process.arch) : targets;
  if (!selected.length) throw new Error(`unsupported native host: ${process.platform}-${process.arch}`);
  const go = spawnSync('go', ['version'], { encoding: 'utf8' });
  if (go.error?.['code'] === 'ENOENT') throw new Error('Go is required to build native release binaries: install Go and put go on PATH (npm users do not need Go).');
  if (go.error || go.status !== 0) throw new Error(`go version failed: ${go.error?.message ?? go.stderr}`);
  const { version } = JSON.parse(readFileSync(path.join(root, 'package.json'), 'utf8'));
  for (const [platform, arch] of selected) {
    const dir = path.join(root, 'native/bin', `${platform}-${arch}`);
    mkdirSync(dir, { recursive: true });
    const binary = path.join(dir, 'code-forge-native');
    const pending = `${binary}.${process.pid}.tmp`;
    try {
      const built = spawnSync('go', ['build', '-trimpath', '-ldflags', `-s -w -X main.version=${version}`, '-o', pending, './cmd/code-forge-native'], {
        cwd: path.join(root, 'native'), encoding: 'utf8',
        env: { ...process.env, CGO_ENABLED: '0', GOOS: platform, GOARCH: arch === 'x64' ? 'amd64' : arch, GOTOOLCHAIN: 'local', GOPROXY: 'off' },
      });
      if (built.error || built.status !== 0) throw new Error(`${platform}-${arch} build failed: ${built.error?.message ?? built.stderr}`);
      chmodSync(pending, 0o755);
      renameSync(pending, binary);
      console.log(`native ${platform}-${arch}: ${statSync(binary).size} bytes (v${version})`);
    } finally { rmSync(pending, { force: true }); }
  }
} catch (err) {
  console.error(`build-native: ${err.message}`);
  process.exitCode = 1;
}
