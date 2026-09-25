/** Small number helpers. */

/** @param {number} a @param {number} b @returns {number} */
export function add(a, b) {
  return a + b;
}

/** @param {number} a @param {number} b @returns {number} */
export function sub(a, b) {
  return a - b;
}

/**
 * @param {number[]} values
 * @returns {number} the arithmetic mean
 * @throws {RangeError} on an empty list
 */
export function mean(values) {
  if (values.length === 0) throw new RangeError('mean of an empty list');
  return values.reduce(add, 0) / values.length;
}

/**
 * @param {number} value @param {number} min @param {number} max
 * @returns {number} `value` limited to the closed range [min, max]
 * @throws {RangeError} when `min > max`
 */
export function clamp(value, min, max) {
  if (min > max) throw new RangeError('clamp: min is above max');
  return Math.min(Math.max(value, min), max);
}
