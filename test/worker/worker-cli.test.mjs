// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { FAKE_JEV_KEY } from './helpers.mjs';
import assert from 'node:assert/strict';
import { after, before, describe, mock, test } from 'node:test';

const { clearSecrets, registerSecret } = await import('../../src/util/redact.mjs');

// `bootWorker` is intercepted with `mock.module`, which only exists under
// `node --experimental-test-module-mocks` (the B11 test command carries it).
if (typeof mock.module !== 'function') {
  throw new Error('test/worker/worker-cli.test.mjs requires `node --experimental-test-module-mocks --test …`; mock.module is unavailable without that flag.');
}

/** The stub `bootWorker` hands back; each test sets the one it needs. */
let stubWorker = /** @type {any} */ (null);
const loopUrl = new URL('../../src/worker/loop.mjs', import.meta.url).href;
const mocked = mock.module(loopUrl, { namedExports: { bootWorker: async () => stubWorker } });
// Cache-busting query: this instance of the verb resolves `../worker/loop.mjs` under the mock.
const { runWorker } = await import(`../../src/cli/worker.mjs?boot-mock=${process.pid}`);
after(() => mocked.restore());

/** @returns {{text: string, write: (s: string) => void}} */
const capture = () => {
  const s = { text: '', write: (/** @type {string} */ chunk) => void (s.text += chunk) };
  return s;
};

/** @param {string} text @param {string} needle */
const count = (text, needle) => text.split(needle).length - 1;

describe('worker verb: a failure inside the loop', () => {
  // B2 registers the Jev key for redaction when it resolves it; the worker resolves it at boot.
  before(() => registerSecret(FAKE_JEV_KEY));
  after(() => clearSecrets());

  test('`--once` whose drain rejects with a message carrying the key: exit 1, one redacted line, key 0 times', async () => {
    stubWorker = {
      once: async () => {
        throw new Error(`engine exploded with ${FAKE_JEV_KEY}`);
      },
      run: async () => {},
      stop: () => {},
      jevKeyResolved: true,
    };
    const stderr = capture();
    assert.equal(await runWorker(['--run', 'r-cli-once', '--once'], { stderr }), 1);
    assert.equal(stderr.text, 'worker: engine exploded with [REDACTED]\n');
    assert.equal(count(stderr.text, FAKE_JEV_KEY), 0);
  });

  test('the serving loop rejecting with the key: exit 1, key 0 times, and both signal listeners removed', async () => {
    const before = { term: process.listenerCount('SIGTERM'), int: process.listenerCount('SIGINT') };
    stubWorker = {
      once: async () => 0,
      run: async () => {
        throw new Error(`queue read failed: ${FAKE_JEV_KEY}`);
      },
      stop: () => {},
      jevKeyResolved: true,
    };
    const stderr = capture();
    assert.equal(await runWorker(['--run', 'r-cli-run'], { stderr }), 1);
    assert.equal(stderr.text, `worker: pid ${process.pid} serving run r-cli-run · jev key resolved\nworker: queue read failed: [REDACTED]\n`);
    assert.equal(count(stderr.text, FAKE_JEV_KEY), 0);
    assert.deepEqual({ term: process.listenerCount('SIGTERM'), int: process.listenerCount('SIGINT') }, before);
  });
});
