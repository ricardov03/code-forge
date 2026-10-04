/**
 * In-process locks (parallel review, block B40): a keyed mutex and a counting semaphore.
 *
 * Both live in this process only — no files, no cross-process coordination — and use no timers,
 * so a waiting lock never keeps the event loop alive on its own.
 *
 * `keyedLock(key, fn)`: calls for the same key run one at a time, in call order (FIFO); calls for
 * different keys run together. A throw (or rejection) in `fn` releases the key and is passed on.
 *
 * `semaphore(n)`: `acquire()` resolves to a `release` function once fewer than `n` holders are
 * active; waiters are served in order. `release` is idempotent (a second call does nothing).
 * `run(fn)` acquires, runs `fn`, and releases on every exit path.
 */

/** @type {Map<unknown, Promise<void>>} the tail of each key's queue. */
const tails = new Map();

/**
 * Run `fn` once every earlier call for `key` has finished.
 * @template T
 * @param {unknown} key
 * @param {() => T | Promise<T>} fn
 * @returns {Promise<T>}
 */
export function keyedLock(key, fn) {
  const prev = tails.get(key) ?? Promise.resolve();
  /** @type {() => void} */
  let release = () => {};
  const done = new Promise((resolve) => {
    release = () => resolve(undefined);
  });
  const tail = prev.then(() => done);
  tails.set(key, tail);
  return prev.then(async () => {
    try {
      return await fn();
    } finally {
      release();
      if (tails.get(key) === tail) tails.delete(key);
    }
  });
}

/**
 * @typedef {object} Semaphore
 * @property {() => Promise<() => void>} acquire - resolves to an idempotent `release`.
 * @property {<T>(fn: () => T | Promise<T>) => Promise<T>} run - `fn` under one slot, released on every exit path.
 * @property {number} active - holders right now.
 * @property {number} pending - waiters right now.
 */

/**
 * A counting semaphore: never more than `n` holders at once.
 * @param {number} n - an integer ≥ 1.
 * @returns {Semaphore}
 */
export function semaphore(n) {
  if (!Number.isInteger(n) || n < 1) throw new TypeError('semaphore: n must be an integer >= 1');
  let active = 0;
  /** @type {Array<(release: () => void) => void>} */
  const waiters = [];

  /** @returns {() => void} */
  function grant() {
    active += 1;
    let released = false;
    return () => {
      if (released) return;
      released = true;
      active -= 1;
      const next = waiters.shift();
      if (next) next(grant());
    };
  }

  /** @returns {Promise<() => void>} */
  function acquire() {
    if (active < n && waiters.length === 0) return Promise.resolve(grant());
    return new Promise((resolve) => {
      waiters.push(resolve);
    });
  }

  /** @template T @param {() => T | Promise<T>} fn @returns {Promise<T>} */
  async function run(fn) {
    const release = await acquire();
    try {
      return await fn();
    } finally {
      release();
    }
  }

  return {
    acquire,
    run,
    get active() {
      return active;
    },
    get pending() {
      return waiters.length;
    },
  };
}
