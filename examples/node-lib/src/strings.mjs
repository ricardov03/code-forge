/** Small string helpers. */

import { readFileSync } from 'node:fs';

/**
 * @param {string} text @param {number} width @param {{side?: 'left' | 'right', fill?: string}} [opts]
 * @returns {string} `text` padded to `width` characters
 */
export function pad(text, width, { side = 'left', fill = ' ' } = {}) {
  return side === 'left' ? text.padStart(width, fill) : text.padEnd(width, fill);
}

/** @param {string} text @returns {string} */
export function capitalize(text) {
  return text.length === 0 ? text : text[0].toUpperCase() + text.slice(1);
}

/**
 * `GREETING` from a dotenv-style file (default `.env` in the current directory), or `hi`.
 * @param {string} name @param {string} [envFile]
 * @returns {string}
 */
export function greet(name, envFile = '.env') {
  let word = 'hi';
  try {
    const line = readFileSync(envFile, 'utf8')
      .split('\n')
      .find((l) => l.startsWith('GREETING='));
    if (line) word = line.slice('GREETING='.length).trim();
  } catch {
    // no env file: the default greeting
  }
  return `${capitalize(word)}, ${name}!`;
}
