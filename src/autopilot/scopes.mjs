/**
 * The autopilot scope vocabulary (issue #5, plan autopilot §1): the ONE place that says what a
 * grant may allow and what it never may. Every other autopilot module reads these lists; none
 * keeps its own copy.
 *
 *  - {@link ALLOWABLE_SCOPES}: what `autopilot start --allow` accepts.
 *  - {@link FIXED_DENY}: always denied. A grant that names one of these in `--allow` is refused at
 *    `start` with the scope named; `grantFor` answers `denied` for them whatever the grant says.
 *    The list is code, not prompt text: the delegate cannot argue its way past it.
 *  - {@link LIMIT_KEYS}: the config key paths behind `limits:change` (a change to any of them, or
 *    to a key under one, relaxes a rule). B48 checks `run reload` against it.
 *  - {@link BUDGET_CATEGORIES}: the categories `--budget <category>=<usd>` accepts (B48 meters them).
 */

/** Scopes an owner may delegate. */
export const ALLOWABLE_SCOPES = Object.freeze(['waive:warning', 'waive:nit', 'round:extra', 'model:choose']);

/** What each allowable scope lets the delegate do (status output and docs). */
export const ALLOWABLE_WHAT = Object.freeze({
  'waive:warning': 'waive a warning finding',
  'waive:nit': 'waive a nit finding',
  'round:extra': 'one fix round past the cap for a file',
  'model:choose': 'choose the coder level for a block',
});

/**
 * Config key paths behind `limits:change`. A key path is covered when it equals one of these,
 * sits under one (`budget.usd` under `budget`), or is a parent of one (`review`, and the root
 * `''`): replacing a parent replaces the limit inside it.
 */
export const LIMIT_KEYS = Object.freeze([
  'budget',
  'review.budgets',
  'review.block_budget_tokens',
  'review.block_budget_usd',
  'review.max_rounds_per_file',
  'review.single_reviewer_max_risk',
  'review.multimodel',
  'review.closed_book',
  'review.allow_open_book_codex',
  'thresholds',
  'escalation',
  'proof',
  'system1',
  'production',
]);

/** @param {string} keyPath @returns {boolean} whether changing `keyPath` is a `limits:change`. */
export function isLimitKey(keyPath) {
  if (keyPath === '') return true;
  return LIMIT_KEYS.some((k) => keyPath === k || keyPath.startsWith(`${k}.`) || k.startsWith(`${keyPath}.`));
}

/** {@link LIMIT_KEYS} as text: a top-level group as `name.*`, a nested key as itself. */
export const LIMIT_KEYS_TEXT = LIMIT_KEYS.map((k) => (k.includes('.') ? k : `${k}.*`)).join(', ');

/** The fixed deny list: scope → what it would have allowed. Never delegable. */
export const FIXED_DENY = Object.freeze({
  'waive:critical': 'waive a critical finding',
  'waive:proof': 'waive a proof finding',
  'reviews:skip': 'close a block without its reviews (--no-require-reviews)',
  'limits:change': `change a limit or rule key (${LIMIT_KEYS_TEXT})`,
  'plan:approve': 'approve a plan',
  'design:approve': 'approve a design',
  'pr:merge': 'merge a pull request',
  destructive: 'run a destructive action',
  'budget:raise': 'raise a budget',
});

/** @type {ReadonlyArray<string>} */
export const FIXED_DENY_SCOPES = Object.freeze(Object.keys(FIXED_DENY));

/** Every scope name the vocabulary knows. */
export const ALL_SCOPES = Object.freeze([...ALLOWABLE_SCOPES, ...FIXED_DENY_SCOPES]);

/** Budget categories `--budget` accepts. */
export const BUDGET_CATEGORIES = Object.freeze(['coding', 'review']);

/** The delegate levels `--delegate` accepts. */
export const DELEGATE_LEVELS = Object.freeze(['L2', 'L3']);

/** @param {string} scope @returns {boolean} */
export const isAllowable = (scope) => ALLOWABLE_SCOPES.includes(scope);

/** @param {string} scope @returns {boolean} */
export const isFixedDeny = (scope) => Object.hasOwn(FIXED_DENY, scope);
