import { availableParallelism, loadavg } from 'node:os';

/**
 * Host scheduling allowance for real-process test deadlines. Sample the one-minute
 * runnable load once per test file and divide by the CPUs available to this process.
 * Round up, with a floor of one: load 25 on 10 CPUs gives a factor of three.
 * This adds no calibration sleep. Use only for harness deadlines, never injected
 * clocks, backoff counts, or product timeout values whose behavior is under test.
 */
export const loadFactor = Math.max(1, Math.ceil(loadavg()[0] / availableParallelism()));

/** @param {number} ms */
export const loadedMs = (ms) => Math.ceil(ms * loadFactor);
