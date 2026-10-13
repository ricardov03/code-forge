import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { parseRed } from '../../src/proof/red-parse.mjs';

// Output shapes captured once from real runs (node:test 24 spec reporter; Pest collision printer),
// paths anonymised. No PHP is run here.
const CAPTURED = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'fixtures', 'repos', 'red-green', 'captured');
const captured = (/** @type {string} */ name) => readFileSync(path.join(CAPTURED, name), 'utf8');

/** @param {ReturnType<typeof parseRed>} r */
const shape = (r) => [r.verdict, r.red_kind, r.framework, r.failed];

test('red-parse: a node:test assertion failure is RED (assertion)', () => {
  assert.deepEqual(shape(parseRed({ code: 1, stdout: captured('node-assertion.txt') })), ['RED', 'assertion', 'node:test', 1]);
});

test('red-parse: a Pest assertion failure is RED (assertion)', () => {
  // Pest prints on stdout; the collision printer colours are stripped first.
  const colored = `\x1b[31m${captured('pest-assertion.txt')}\x1b[39m`;
  assert.deepEqual(shape(parseRed({ code: 1, stdout: colored })), ['RED', 'assertion', 'pest', 1]);
});

test('red-parse: a node:test import error is RED_INVALID (import), although the runner prints "test failed"', () => {
  assert.deepEqual(shape(parseRed({ code: 1, stdout: captured('node-import-error.txt') })), ['RED_INVALID', 'import', 'node:test', 1]);
});

test('red-parse boundaries: Pest class-not-found, a pass, a timeout and a failure with no assertion line', () => {
  assert.deepEqual(shape(parseRed({ code: 1, stdout: captured('pest-class-not-found.txt') })), ['RED_INVALID', 'import', 'pest', 1]);
  assert.deepEqual(shape(parseRed({ code: 0, stdout: 'ℹ tests 1\nℹ pass 1\nℹ fail 0\n' })), ['NOT_RED', 'none', 'node:test', 0]);
  // a timed-out run is never RED, even with an assertion line in its output
  assert.deepEqual(shape(parseRed({ code: null, signal: 'SIGTERM', timedOut: true, stdout: 'AssertionError [ERR_ASSERTION]' })), ['RED_INVALID', 'fatal', 'unknown', null]);
  assert.deepEqual(shape(parseRed({ code: 1, stderr: 'TypeError: clamp is not a function\n' })), ['RED_INVALID', 'error', 'unknown', null]);
});

/**
 * B89: the console of `php artisan test <file> --filter <case> --log-junit <report>` as the B89 live check
 * captured it (Laravel Framework 13.35.0, PHPUnit 12.5.39, Pest 4.7.8; paths shortened to `/x/…`). Under an AI
 * agent (`CLAUDECODE` and the like set) Laravel prints one JSON summary line, the class name JSON-escaped; in a
 * plain terminal Collision's text, the class name as is.
 */
const LARAVEL = {
  agentNewClass: {
    phpunit:
      '{"tool":"phpunit","result":"failed","tests":1,"passed":0,"assertions":0,"duration_ms":3,"errors":1,"error_details":[{"test":"Tests\\\\Unit\\\\GreetingTest::test_greeting_comes_from_the_owned_class","file":"/x/tests/Unit/GreetingTest.php","line":12,"message":"Class \\"App\\\\Support\\\\Greeting\\" not found"}]}\n',
    pest: '{"tool":"pest","result":"failed","tests":1,"passed":0,"assertions":0,"duration_ms":10,"errors":1,"error_details":[{"test":"P\\\\Tests\\\\Unit\\\\GreetingTest::__pest_evaluable_greeting_comes_from_the_owned_class","file":"/x/tests/Unit/GreetingTest.php","line":6,"message":"Class \\"App\\\\Support\\\\Greeting\\" not found"}]}\n',
  },
  agentAssertion: {
    phpunit:
      '{"tool":"phpunit","result":"failed","tests":1,"passed":0,"assertions":1,"duration_ms":3,"failed":1,"failures":[{"test":"Tests\\\\Unit\\\\ClampTest::test_caps_high","file":"/x/tests/Unit/ClampTest.php","line":12,"message":"Failed asserting that 12 is identical to 10."}]}\n',
    pest: '{"tool":"pest","result":"failed","tests":1,"passed":0,"assertions":1,"duration_ms":12,"failed":1,"failures":[{"test":"P\\\\Tests\\\\Unit\\\\ClampTest::__pest_evaluable_caps_high","file":"/x/tests/Unit/ClampTest.php","line":6,"message":"Failed asserting that 12 is identical to 10."}]}\n',
  },
  // Lines of each runner's own plain capture (`<runner>/03-new-class/console.txt`), in order; the code frame
  // line shows the source: a PHPUnit class method, a Pest `test(…)` closure. `php artisan test` prints both
  // through Collision, so both give red-parse's framework `pest` (its name for the Collision `Tests:` format).
  plainNewClass: {
    phpunit:
      '  \u001b[41;1m FAILED \u001b[49;22m \u001b[1mTests\\Unit\\GreetingTest\u001b[22m \u001b[90m>\u001b[39m greeting comes from the owned cl… \u001b[41;1m Error \u001b[49;22m  \n\u001b[39;1m  Class "App\\Support\\Greeting" not found\u001b[39;22m\n  at \u001b[32mtests/Unit/GreetingTest.php\u001b[39m:\u001b[32m12\u001b[39m\n      8▕ class GreetingTest extends TestCase\n  \u001b[90mTests:\u001b[39m    \u001b[31;1m1 failed\u001b[39;22m\u001b[90m (0 assertions)\u001b[39m\n',
    pest:
      '  \u001b[41;1m FAILED \u001b[49;22m \u001b[1mTests\\Unit\\GreetingTest\u001b[22m \u001b[90m>\u001b[39m greeting comes from the owned cl… \u001b[41;1m Error \u001b[49;22m  \n\u001b[39;1m  Class "App\\Support\\Greeting" not found\u001b[39;22m\n  at \u001b[32mtests/Unit/GreetingTest.php\u001b[39m:\u001b[32m6\u001b[39m\n      5▕ test(\'greeting comes from the owned class\', function () {\n  \u001b[90mTests:\u001b[39m    \u001b[31;1m1 failed\u001b[39;22m\u001b[90m (0 assertions)\u001b[39m\n',
  },
};

test('B89 (1): a Laravel class-not-found is RED_INVALID (import) in both console forms: the JSON-escaped agent line and the plain text', () => {
  for (const runner of /** @type {const} */ (['phpunit', 'pest'])) {
    // the escaped form really is in the captured line: `Class \"App\\Support\\Greeting\" not found`
    assert.ok(LARAVEL.agentNewClass[runner].includes('Class \\"App\\\\Support\\\\Greeting\\" not found'), runner);
    assert.deepEqual(shape(parseRed({ code: 2, stdout: LARAVEL.agentNewClass[runner] })), ['RED_INVALID', 'import', 'unknown', null], runner);
    // the agent line of a failed assertion stays an assertion
    assert.deepEqual(shape(parseRed({ code: 1, stdout: LARAVEL.agentAssertion[runner] })), ['RED', 'assertion', 'unknown', null], runner);
    // each plain excerpt is the runner's own: the PHPUnit one shows a class method, the Pest one a test() closure
    assert.equal(LARAVEL.plainNewClass[runner].includes('class GreetingTest extends TestCase'), runner === 'phpunit', runner);
    assert.equal(LARAVEL.plainNewClass[runner].includes("test('greeting comes from the owned class'"), runner === 'pest', runner);
    assert.deepEqual(shape(parseRed({ code: 2, stdout: LARAVEL.plainNewClass[runner] })), ['RED_INVALID', 'import', 'pest', 1], runner);
  }
  // the escaped form of an interface too; a JSON-escaped class that was found but failed otherwise is no import
  assert.equal(parseRed({ code: 2, stdout: '{"message":"Interface \\"App\\\\Contracts\\\\Clock\\" not found"}' }).red_kind, 'import');
  assert.equal(parseRed({ code: 2, stdout: '{"message":"Class \\"App\\\\Support\\\\Greeting\\" is abstract"}' }).red_kind, 'error');
  assert.equal(parseRed({ code: 2, stdout: '{"message":"Class \\"\\" not found"}' }).red_kind, 'error');
});
