/**
 * From answers to `.code-forge.yml` (plan §1.3 keys, §2 "Writes" column; block B13a). The config
 * holds key REFERENCES only (`keys.jev: env:NAME | op://… | user`), never a value. A re-run merges
 * the wizard's keys into the existing document: keys the wizard does not manage are kept as they
 * are, and when nothing changed the file is not rewritten (0 bytes changed).
 */

import { isDeepStrictEqual } from 'node:util';
import { stringify } from 'yaml';
import { defaultsForProvider } from '../../config/defaults/index.mjs';

/**
 * @param {import('./answers.mjs').Answers} a
 * @param {{project: {name: string, slug: string}}} ctx
 * @param {string|null} jevRef - the resolved `keys.jev` reference, or null for "no Jev key".
 * @returns {Record<string, any>} the wizard-managed part of the config, in documented key order.
 */
export function generatedConfig(a, ctx, jevRef) {
  const defaults = defaultsForProvider(a.provider);
  /** @type {Record<string, any>} */
  const review = { multimodel: a.multimodel };
  if (a.multimodel && a.second_provider) {
    const second = /** @type {Record<string, any>} */ (defaultsForProvider(a.second_provider).levels);
    review.second_provider = a.second_provider;
    review.second_levels = { L2: { ...second.L2, provider: a.second_provider }, L3: { ...second.L3, provider: a.second_provider } };
  }
  /** @type {Record<string, any>} */
  const cfg = {
    version: 1,
    project: { name: ctx.project.name, slug: ctx.project.slug },
    provider: a.provider,
    levels: a.levels,
    caps: { coders: defaults.caps.coders },
    review,
    engine: a.engine,
    harnesses: [...a.harnesses],
  };
  if (jevRef) cfg.keys = { jev: jevRef };
  else cfg.system1 = { fallback: 'rules' };
  cfg.gates = { test: a.gates.test, lint: a.gates.lint, types: a.gates.types, format: a.gates.format };
  cfg.proof = {
    tiers: { high: { paths: [...a.proof.high] } },
    isolation: a.proof.isolation,
    export: { link_dirs: [...a.proof.link_dirs], copy_untracked: [...a.proof.copy_untracked] },
  };
  return cfg;
}

/** @param {unknown} v @returns {v is Record<string, any>} */
function isMap(v) {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
}

/**
 * `base` with `over` merged in: maps merge key by key (base order kept, new keys appended),
 * anything else (arrays, scalars, null) is replaced by `over`'s value.
 * @param {Record<string, any>} base @param {Record<string, any>} over
 * @returns {Record<string, any>}
 */
export function mergeInto(base, over) {
  /** @type {Record<string, any>} */
  const out = { ...base };
  for (const [key, value] of Object.entries(over)) {
    out[key] = isMap(value) && isMap(base[key]) ? mergeInto(base[key], value) : value;
  }
  return out;
}

/**
 * `generatedConfig` writes ONE of `keys.jev` and `system1.fallback: rules`; on a re-run the side it
 * did not write is removed from the existing document, so the two never contradict each other.
 * @param {Record<string, any>|null} existing
 * @param {Record<string, any>} generated
 * @param {{dropJev: boolean}} opts - `--no-jev` (or "skip") removes an existing `keys.jev`; the
 *   same follows from `generated` having no `keys.jev`, so the two never disagree.
 * @returns {Record<string, any>}
 */
export function mergeConfig(existing, generated, { dropJev }) {
  const merged = existing ? mergeInto(existing, generated) : mergeInto({}, generated);
  // multimodel off: a second reviewer from an earlier run must not survive the merge
  if (isMap(merged.review) && !Object.hasOwn(generated.review ?? {}, 'second_provider')) {
    const { second_provider: _p, second_levels: _l, ...review } = merged.review;
    merged.review = review;
  }
  const hasJev = typeof generated.keys?.jev === 'string';
  if ((dropJev || !hasJev) && isMap(merged.keys) && Object.hasOwn(merged.keys, 'jev')) {
    const { jev: _drop, ...rest } = merged.keys;
    if (Object.keys(rest).length > 0) merged.keys = rest;
    else delete merged.keys;
  }
  // a key is referenced: the rules-only fallback of an earlier keyless run goes
  if (hasJev && isMap(merged.system1) && Object.hasOwn(merged.system1, 'fallback')) {
    const { fallback: _drop, ...rest } = merged.system1;
    if (Object.keys(rest).length > 0) merged.system1 = rest;
    else delete merged.system1;
  }
  return merged;
}

/**
 * Dotted key paths whose value differs between `a` and `b` (leaf level; arrays compared whole).
 * @param {unknown} a @param {unknown} b @param {string} [prefix]
 * @returns {string[]}
 */
export function changedPaths(a, b, prefix = '') {
  if (isMap(a) && isMap(b)) {
    const keys = [...new Set([...Object.keys(a), ...Object.keys(b)])];
    return keys.flatMap((k) => changedPaths(a[k], b[k], prefix ? `${prefix}.${k}` : k));
  }
  return isDeepStrictEqual(a, b) ? [] : [prefix || '(root)'];
}

/** @param {Record<string, any>} cfg @returns {string} the YAML text written to disk. */
export function serializeConfig(cfg) {
  return stringify(cfg, { lineWidth: 0 });
}
