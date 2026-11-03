import { commitAll, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { assemblePacket, buildPacket } from '../../src/review/packet.mjs';

const fixtures = new URL('../fixtures/go-context/', import.meta.url);
const sample = (file) => readFileSync(new URL(file, fixtures), 'utf8');
const section = (text, heading) => text.split(`## ${heading}\n`)[1]?.split('\n## ')[0].trimEnd() ?? '';
const related = (text) => section(text, 'related files (not shown)').split('\n').filter(Boolean);
const definitions = (text) => section(text, 'definitions (called by changed lines)');
const entries = (text) => definitions(text).split('\n').filter((line) => line.startsWith('### '));

async function fixture() {
  const repoRoot = await makeRepo();
  writeFile(repoRoot, 'cmd/main.go', 'package main\n\nfunc main() {}\n');
  for (const file of ['router.go', 'router_test.go']) writeFile(repoRoot, `cmd/${file}`, sample(file));
  writeFile(repoRoot, 'cmd/ignored.js', 'function missing() { return "JS_BODY"; }\n');
  writeFile(repoRoot, 'other/elsewhere.go', 'package main\nfunc missing() { println("OTHER_DIR_BODY") }\n');
  await commitAll(repoRoot);
  writeFile(repoRoot, 'cmd/main.go', sample('main.go'));
  return repoRoot;
}

async function packet(repoRoot, file = 'cmd/main.go') {
  const result = await buildPacket({ repoRoot, file, lens: 'full' });
  assert.equal(result.status, 'ok', `${file}: packet is reviewable`);
  if (result.status !== 'ok') throw new Error('expected reviewable fixture');
  return result;
}

test('Go related files are sorted directory siblings and include tests only for a test caller', async () => {
  const repoRoot = await fixture();
  writeFile(repoRoot, 'cmd/alpha.go', 'package main\n');
  writeFile(repoRoot, 'cmd/alpha_test.go', 'package main\n');
  await commitAll(repoRoot);
  writeFile(repoRoot, 'cmd/main.go', sample('main.go') + '// changed\n');
  writeFile(repoRoot, 'cmd/router_test.go', sample('router_test.go') + '// changed\n');
  const ordinary = await packet(repoRoot);
  assert.deepEqual(related(ordinary.text), ['cmd/alpha.go', 'cmd/router.go'], 'exactly two sorted non-test Go siblings; no self, JS or other directory');
  const testing = await packet(repoRoot, 'cmd/router_test.go');
  assert.deepEqual(related(testing.text), ['cmd/alpha.go', 'cmd/alpha_test.go', 'cmd/main.go', 'cmd/router.go'], 'test caller lists exactly four sorted siblings, including another test');
});

test('Go packet definitions contain each called function and method body once with opaque literals and comments', async () => {
  const repoRoot = await fixture();
  const result = await packet(repoRoot);
  const router = sample('router.go').split('\n');
  const expected = ['run', 'serve'].map((name) => {
    const start = router.findIndex((line) => line.startsWith('func ') && line.includes(`${name}(`));
    const end = router.indexOf('}', start);
    return [`### cmd/router.go:${start + 1}-${end + 1}`,
      ...router.slice(start, end + 1).map((line, i) => `${start + i + 1}| ${line}`)].join('\n');
  });
  assert.deepEqual(entries(result.text), expected.map((body) => body.split('\n')[0]), 'exactly run and serve, in call order, despite the repeated run call');
  assert.equal(definitions(result.text), expected.join('\n'), 'both complete bodies retain nested blocks, opaque braces, and final closing lines');
  assert.equal((definitions(result.text).match(/UNUSED_BODY|OTHER_DIR_BODY|JS_BODY|TEST_HELPER_BODY/g) ?? []).length, 0, 'zero uncalled, undeclared, test-only or outside-directory bodies');
});

test('Go context boundaries cap related paths at twelve and ignore calls inside comments and literals', async () => {
  const repoRoot = await fixture();
  const names = Array.from({ length: 14 }, (_, i) => `cmd/a${String(i).padStart(2, '0')}.go`);
  for (const name of names) writeFile(repoRoot, name, 'package main\n');
  writeFile(repoRoot, 'cmd/unchanged.go', 'package main\nfunc unchanged() { println("UNCHANGED_BODY") }\n');
  const initial = ['package main', '', 'func main() {', '\tunchanged()', '\t// insertion', '}', ''];
  writeFile(repoRoot, 'cmd/main.go', initial.join('\n'));
  await commitAll(repoRoot);
  writeFile(repoRoot, 'cmd/main.go', initial.join('\n').replace('\t// insertion', [
    '\t// fakeComment()', '\t/* fakeBlock() */', '\tlabel := "fakeString() }"',
    '\traw := `fakeRaw() }`', "\truneBrace := '}'", '\tmissing()', '\ttestHelper()',
    '\tprintln(label, raw, runeBrace)',
  ].join('\n')));
  const result = await packet(repoRoot);
  assert.deepEqual(related(result.text), names.slice(0, 12), 'exactly the first twelve sorted Go siblings');
  assert.deepEqual(entries(result.text), [], 'zero bodies from opaque tokens, unchanged context lines, missing names or test-only declarations');
  const without = result.text.replace(`## related files (not shown)\n${related(result.text).join('\n')}\n`, '');
  const tight = assemblePacket({ diff: result.diff, lens: 'full', budget: Math.ceil(Buffer.byteLength(without) / 4) });
  assert.equal(tight.status, 'ok');
  assert.ok('text' in tight);
  assert.equal(tight.text, without, 'budget drops exactly the related section and retains the entire diff and context');
});
