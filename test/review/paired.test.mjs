import './helpers.mjs'; // Sets HOME before src imports; one per-file temp parent.
import assert from 'node:assert/strict';
import { test } from 'node:test';

const { pairsFor, summarize } = await import('../../src/review/paired.mjs');
const { clearSecrets, registerSecret } = await import('../../src/util/redact.mjs');

test('B97 pairs: exact stems in both directions across six language extensions', () => {
  const candidates = [
    'src/widget.mjs', 'src/widget.test.mjs', 'test/widget.spec.mjs', 'test/widget.mjs',
    'src/gadget.ts', 'test/gadget.test.ts', 'src/Widget.php', 'tests/WidgetTest.php',
    'src/widget.go', 'src/widget_test.go', 'src/widget.py', 'tests/test_widget.py',
    'src/Widget.swift', 'tests/WidgetTests.swift', 'test/other.test.mjs', 'src/widget.txt',
    'test/widget.spec.mjs', '../widget.test.mjs',
  ];
  assert.deepEqual([
    pairsFor('src/widget.mjs', candidates), pairsFor('src/gadget.ts', candidates),
    pairsFor('src/Widget.php', candidates), pairsFor('src/widget.go', candidates),
    pairsFor('src/widget.py', candidates), pairsFor('src/Widget.swift', candidates),
    pairsFor('test/widget.spec.mjs', candidates), pairsFor('tests/WidgetTest.php', candidates),
    pairsFor('src/widget_test.go', candidates), pairsFor('tests/test_widget.py', candidates),
    pairsFor('tests/WidgetTests.swift', candidates), pairsFor('test/gadget.test.ts', candidates),
    pairsFor('src/absent.mjs', candidates), pairsFor('src/widget.txt', candidates),
  ], [
    ['src/widget.test.mjs', 'test/widget.spec.mjs', 'test/widget.mjs'], ['test/gadget.test.ts'],
    ['tests/WidgetTest.php'], ['src/widget_test.go'], ['tests/test_widget.py'], ['tests/WidgetTests.swift'],
    ['src/widget.mjs'], ['src/Widget.php'], ['src/widget.go'], ['src/widget.py'], ['src/Widget.swift'],
    ['src/gadget.ts'], [], [],
  ]);
});

test('B97 summaries: test names and exported symbols, with no body text or unsupported names', () => {
  assert.deepEqual([
    summarize("test('handles empty', () => {});\nit.only(\"handles large\", () => {});\nconst body = 'unused';", 'test/widget.test.mjs'),
    summarize("test.skip('skipped case', () => {});", 'test/widget.spec.ts'),
    summarize("public function testEmpty() {}\nit('handles zero', function () {});", 'tests/WidgetTest.php'),
    summarize('func TestEmpty(t *testing.T) {}\nfunc helper() {}', 'widget_test.go'),
    summarize('def test_empty():\n    pass\nasync def test_large():\n    pass', 'test_widget.py'),
    summarize('func testEmpty() {}', 'WidgetTests.swift'),
    summarize('export const widget = 1;\nexport async function load() {}\nexport { widget as alias };\nconst privateName = 1;', 'src/widget.mjs'),
    summarize('export interface Shape {}\nexport type Size = number;\nexport default class Widget {}', 'src/widget.ts'),
    summarize('class Widget {}\nfunction load() {}', 'src/Widget.php'),
    summarize('func Load() {}\nfunc (w Widget) Reset() {}\ntype Widget struct {}\nfunc privateName() {}', 'widget.go'),
    summarize('def load():\n    pass\nclass Widget:\n    pass\ndef _private():\n    pass', 'widget.py'),
    summarize('public struct Widget {}\npublic func load() {}\nfunc privateName() {}', 'Widget.swift'),
    summarize('const privateName = 1;', 'src/empty.mjs'), summarize("test('unused', () => {});", 'widget.txt'),
  ], [
    ['handles empty', 'handles large'], ['skipped case'], ['testEmpty', 'handles zero'],
    ['TestEmpty'], ['test_empty', 'test_large'], ['testEmpty'], ['widget', 'load', 'alias'],
    ['Shape', 'Size', 'Widget'], ['Widget', 'load'], ['Load', 'Reset', 'Widget'],
    ['load', 'Widget'], ['Widget', 'load'], [], [],
  ]);
});

test('B97 mixed quotes: test names close on the opening quote kind', () => {
  assert.deepEqual(summarize(`test("handles it's empty", () => {});
it('say "hi"', () => {});
test(\`handles 'single' and "double" quotes\`, () => {});`, 'test/widget.test.mjs'),
  ["handles it's empty", 'say "hi"', `handles 'single' and "double" quotes`]);
});

test('B97 type export lists: names omit type and preserve aliases', () => {
  assert.deepEqual(summarize('export { type Foo, type Bar as Baz, widget, value as alias };', 'src/widget.ts'),
    ['Foo', 'Baz', 'widget', 'alias']);
});

test('B97 unique bounds: duplicates do not consume the 40-name limit', () => {
  const names = ['case 0', 'case 0', ...Array.from({ length: 42 }, (_, i) => `case ${i}`)];
  const summary = summarize(names.map((name) => `test('${name}', () => {});`).join('\n'), 'test/widget.test.mjs');
  assert.equal(summary.length, 40);
  assert.equal(new Set(summary).size, 40);
  assert.deepEqual(summary, Array.from({ length: 40 }, (_, i) => `case ${i}`));
});

test('B97 PHP modifiers: methods with visibility, static, final and abstract in any order', () => {
  assert.deepEqual(summarize(`public function foo() {}
public static function bar() {}
final public function baz() {}
protected function guarded() {}
private static function hidden() {}
static public function reversed() {}
abstract protected function pending();
protected abstract function pendingReversed();
private final static function combined() {}`, 'src/Widget.php'),
  ['foo', 'bar', 'baz', 'guarded', 'hidden', 'reversed', 'pending', 'pendingReversed', 'combined']);
});

test('B97 summary bounds: 40 entries, 120 characters, and zero secret occurrences', () => {
  const long = 'é'.repeat(121);
  const fake = 'sk-ant-FAKE01234567890123456789';
  const registered = 'FAKE-registered-value';
  const crossing = 'long case '.padEnd(112, ' ') + fake;
  registerSecret(registered);
  try {
    const names = [long, `handles ${fake}`, `handles ${registered}`, crossing, ...Array.from({ length: 42 }, (_, i) => `case ${i}`)];
    const summary = summarize(names.map((name) => `test('${name}', () => {});`).join('\n'), 'test/widget.test.mjs');
    assert.equal(summary.length, 40);
    assert.deepEqual(summary, ['é'.repeat(120), '[REDACTED]', 'handles [REDACTED]', '[REDACTED]', ...Array.from({ length: 36 }, (_, i) => `case ${i}`)]);
    assert.equal(summary.filter((name) => Array.from(name).length > 120).length, 0);
    assert.equal(summary.join('\n').split(fake).length - 1, 0);
    assert.equal(summary.join('\n').split(registered).length - 1, 0);
    assert.equal(summary.join('\n').split(fake.slice(0, 8)).length - 1, 0);
  } finally { clearSecrets(); }
});
