import { createHash } from 'node:crypto';
import { readFileSync, realpathSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { Ajv2020 } from 'ajv/dist/2020.js';

const schema = JSON.parse(readFileSync(new URL('../../schema/outcome.schema.json', import.meta.url), 'utf8'));
const validate = new Ajv2020({ allErrors: true }).compile(schema);

/** @typedef {{ok: boolean, problems: string[]}} OutcomeResult */

/** @param {unknown} obj @returns {OutcomeResult} */
export function validateOutcome(obj) {
  const ok = validate(obj);
  const problems = ok ? [] : (validate.errors ?? []).map((err) => {
    const extra = err.keyword === 'additionalProperties' ? ` (${JSON.stringify(err.params.additionalProperty)})` : '';
    return `${err.instancePath || '/'} ${err.message}${extra}`.replace(/[\r\n]/g, ' ');
  });
  return { ok, problems };
}

/** Read JSON without changing it. Invalid JSON or an unreadable file throws a one-line error.
 * @param {string} file @returns {any}
 */
export function loadOutcome(file) {
  let text;
  try {
    text = readFileSync(file, 'utf8');
  } catch (err) {
    throw new Error(`Cannot read outcome ${JSON.stringify(file)} (${err.code ?? 'error'})`);
  }
  try {
    return JSON.parse(text);
  } catch {
    throw new Error(`Outcome ${JSON.stringify(file)} is not valid JSON`);
  }
}

/** @param {string} root @param {string} target */
function outside(root, target) {
  const relative = path.relative(root, target);
  return relative === '..' || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative);
}

/** Hash only files contained in root, including when a path traverses a symlink.
 * @param {string} file @param {string} root @returns {string}
 */
function hashFile(file, root) {
  const label = `file ${JSON.stringify(file)}`;
  if (path.isAbsolute(file) || path.win32.isAbsolute(file) || file.split(/[\\/]/).includes('..')) {
    throw new Error(`${label}: path must be relative to root without ..`);
  }
  const target = path.resolve(root, file);
  if (outside(root, target)) throw new Error(`${label}: path leaves root`);
  try {
    if (outside(realpathSync(root), realpathSync(target))) {
      throw new Error(`${label}: path leaves root through a symlink`);
    }
    return createHash('sha256').update(readFileSync(target)).digest('hex');
  } catch (err) {
    if (!err.code) throw err;
    throw new Error(`${label}: ${err.code === 'ENOENT' ? 'missing file' : `cannot read (${err.code})`}`);
  }
}

/** Fill hashes only after every file has been read successfully. Key order is preserved.
 * @param {string} file @param {{root?: string}} [options] @returns {OutcomeResult}
 */
export function sealOutcome(file, { root = process.cwd() } = {}) {
  try {
    const obj = loadOutcome(file);
    const result = validateOutcome(obj);
    if (!result.ok) return result;
    const hashes = obj.files.map((entry) => hashFile(entry.path, path.resolve(root)));
    obj.files.forEach((entry, index) => { entry.sha256 = hashes[index]; });
    writeFileSync(file, `${JSON.stringify(obj, null, 2)}\n`);
    return { ok: true, problems: [] };
  } catch (err) {
    return { ok: false, problems: [err.message] };
  }
}

/** Validate the contract, approval, checks, and the exact approved file bytes.
 * @param {string} file @param {{root?: string}} [options] @returns {OutcomeResult}
 */
export function checkOutcome(file, { root = process.cwd() } = {}) {
  let obj;
  try {
    obj = loadOutcome(file);
  } catch (err) {
    return { ok: false, problems: [err.message] };
  }
  const result = validateOutcome(obj);
  if (!result.ok) return result;
  const problems = [];
  if (obj.status !== 'approved') problems.push(`status ${JSON.stringify(obj.status)}: must be approved`);
  for (const check of obj.checks ?? []) {
    if (check.result === 'fail') problems.push(`check ${JSON.stringify(check.name)}: result is fail`);
  }
  for (const entry of obj.files) {
    try {
      const hash = hashFile(entry.path, path.resolve(root));
      if (entry.sha256 !== hash) problems.push(`file ${JSON.stringify(entry.path)}: sha256 does not match`);
    } catch (err) {
      problems.push(err.message);
    }
  }
  return { ok: problems.length === 0, problems };
}
