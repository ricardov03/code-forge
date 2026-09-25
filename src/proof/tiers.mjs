/**
 * Proof tiers (plan §7.1, D14): two tiers, decided per file at `review-file` time and enforced
 * by the block gate as the UNION of its files' tiers.
 *
 *   high  ⇐ risk ≥ 2, or the path matches `proof.tiers.high.paths`, or `security_sensitive`
 *   light ⇐ everything else (the review-depth band `1 ≤ risk < 2` does NOT raise the proof tier)
 *
 * The light tier's proof is red→green (B10b). The high tier's extra proof — tool-made mutants on
 * changed lines — is held on Q16 (block B10c), so `proofRequirements('high')` reports it as
 * `held` and the gate enforces red→green only until Ricardo answers. Pure: no I/O.
 */

import { ownsFile } from '../state/registry.mjs';

/** @typedef {'light' | 'high'} Tier */
/** @typedef {'risk' | 'path' | 'security' | 'default'} TierReason */

export const TIERS = Object.freeze(/** @type {const} */ (['light', 'high']));

/** `risk ≥ HIGH_RISK` ⇒ high tier (§7.1). */
export const HIGH_RISK = 2;

/**
 * @param {{file: string, risk: number, securitySensitive?: boolean, highPaths?: ReadonlyArray<string>}} input
 *   `risk` is the S1 `risk` answer (0–3); `highPaths` is `proof.tiers.high.paths` (exact paths or
 *   globs in the registry's syntax: `*`, `?`, `**`, `{a,b}`).
 * @returns {{tier: Tier, reason: TierReason}} the FIRST reason that applies, in the order
 *   security, risk, path — every one of them yields `high`, so the order only picks the label.
 * @throws {TypeError} on a non-string file, a risk outside 0–3, or a non-array `highPaths`.
 */
export function tierFor({ file, risk, securitySensitive = false, highPaths = [] }) {
  if (typeof file !== 'string' || file.length === 0) throw new TypeError('tierFor: file must be a non-empty repo-relative path');
  if (typeof risk !== 'number' || !Number.isFinite(risk) || risk < 0 || risk > 3) {
    throw new TypeError('tierFor: risk must be a number from 0 to 3');
  }
  if (!Array.isArray(highPaths)) throw new TypeError('tierFor: proof.tiers.high.paths must be an array');
  if (securitySensitive === true) return { tier: 'high', reason: 'security' };
  if (risk >= HIGH_RISK) return { tier: 'high', reason: 'risk' };
  if (highPaths.length > 0 && ownsFile(highPaths, file)) return { tier: 'high', reason: 'path' };
  return { tier: 'light', reason: 'default' };
}

/**
 * The block's tier: the union of its files' tiers (`high` when any file is `high`).
 * @param {ReadonlyArray<{tier: Tier}>} fileTiers
 * @returns {Tier}
 */
export function blockTier(fileTiers) {
  if (!Array.isArray(fileTiers)) throw new TypeError('blockTier: fileTiers must be an array');
  return fileTiers.some((t) => t?.tier === 'high') ? 'high' : 'light';
}

/**
 * What the gate requires for a tier. `mutants: 'held'` = the high tier's mutation proof is
 * specified but not enforced until Q16 is answered (B10c).
 * @param {Tier} tier
 * @returns {{red_green: true, mutants: 'none' | 'held'}}
 */
export function proofRequirements(tier) {
  if (!TIERS.includes(tier)) throw new TypeError(`proofRequirements: tier must be one of ${TIERS.join(', ')}`);
  return { red_green: true, mutants: tier === 'high' ? 'held' : 'none' };
}
