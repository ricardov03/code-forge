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
