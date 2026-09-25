/** Small array helpers. */

/**
 * @template T
 * @param {T[]} items @param {number} size
 * @returns {T[][]} consecutive slices of at most `size` items
 * @throws {RangeError} when `size` is below 1
 */
export function chunk(items, size) {
  if (!Number.isInteger(size) || size < 1) throw new RangeError('chunk size must be an integer >= 1');
  const out = [];
  for (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size));
  return out;
}

/**
 * @template T
 * @param {T[]} items
 * @returns {T[]} the first occurrence of each item, in order
 */
export function unique(items) {
  return [...new Set(items)];
}
