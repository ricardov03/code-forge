/**
 * Which providers may run a closed-book session (B32, issue #2; security.md §4).
 *
 * A closed-book session (reviewer, judge, S2, author) must see the packet and nothing else. Claude
 * runs it with `--tools ""` and Grok with `--deny "*"`: no tools at all. Codex 0.155.1 has no such
 * mode. Its `exec` sandbox choices are `read-only | workspace-write | danger-full-access`, and
 * `read-only` still hands the model a shell that can read any absolute path (the doctor isolation
 * probe FAILs with an openai L2 reviewer). Its help text documents no flag or `-c` key that removes
 * every tool, so the openai provider is refused for those roles until one exists.
 *
 * Coders keep Codex. The facts delegate keeps it too: it gets read-only shell tools by design.
 * The scrub pass (`session/scrub.mjs`) runs as role `reviewer` at L1, so it is refused as well.
 *
 * Opt-in (default off): `review.allow_open_book_codex: true` lets Codex run those roles with its
 * read-only argv; every surface then shows {@link OPEN_BOOK_CODEX_WARNING} instead of refusing.
 */

/** The text every refusal prints: the builder, the spawner, `doctor`, `validate`, `resolve`. */
export const CODEX_CLOSED_BOOK_REFUSAL = 'codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author';

/**
 * The warning every surface prints when `review.allow_open_book_codex: true` lets Codex run those
 * roles anyway, with its `-s read-only` argv (B32 opt-in, default off).
 */
export const OPEN_BOOK_CODEX_WARNING = 'codex reviewer is not closed-book: it can read files on this machine (review.allow_open_book_codex)';

/**
 * @param {unknown} cfg - a loaded project config.
 * @returns {boolean} true only when `review.allow_open_book_codex` is exactly `true`.
 */
export function openBookCodexAllowed(cfg) {
  return /** @type {any} */ (cfg)?.review?.allow_open_book_codex === true;
}

/** The provider that cannot run closed-book (its CLI is Codex). */
export const NO_CLOSED_BOOK_PROVIDER = 'openai';

/** Roles that must run with no tools (facts is not one: it has read-only tools by design). */
export const NO_TOOL_ROLES = Object.freeze(['reviewer', 'judge', 's2', 'author']);

/** Levels whose sessions are closed-book: L2 (reviewers), L3 (judge, S2, author). */
export const CLOSED_BOOK_LEVELS = Object.freeze(['L2', 'L3']);

/** @param {unknown} value @returns {string} `value` trimmed and lowercased, `''` for a non-string. */
function norm(value) {
  return typeof value === 'string' ? value.trim().toLowerCase() : '';
}

/** @param {unknown} provider @returns {boolean} true when `provider` (any case) is the Codex provider. */
export function isNoClosedBookProvider(provider) {
  return norm(provider) === NO_CLOSED_BOOK_PROVIDER;
}

/** @param {unknown} role @returns {boolean} true when `role` (any case) must run with no tools. */
export function isNoToolRole(role) {
  return NO_TOOL_ROLES.includes(norm(role));
}

/**
 * @param {unknown} provider @param {unknown} role - compared lowercased and trimmed.
 * @returns {boolean} true when `provider` cannot run `role` closed-book.
 */
export function needsClosedBook(provider, role) {
  return isNoClosedBookProvider(provider) && isNoToolRole(role);
}

/**
 * @param {unknown} provider @param {unknown} role @param {boolean} openBookAllowed - the config's
 *   opt-in, read once by the caller with {@link openBookCodexAllowed}.
 * @returns {boolean} true when the session must be refused: `provider` cannot run `role`
 *   closed-book and the config has not opted in to open-book Codex.
 */
export function closedBookRefused(provider, role, openBookAllowed) {
  return needsClosedBook(provider, role) && openBookAllowed !== true;
}
