/**
 * The one argv item a gate uses for the block's related tests. It lives in `util` because two
 * layers need the same value: `gates/expand.mjs` expands it, and `config/validate.mjs` refuses it as
 * a gate's command (`config` may not import `gates`).
 */

/** A gate argv item that is EXACTLY this string expands to the block's test files. */
export const TEST_FILES_TOKEN = '{test_files}';
