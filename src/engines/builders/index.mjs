/**
 * Generic per-provider argv-builder dispatch (plan §5.2). A later block (B9's `spawn.mjs`) calls
 * `buildArgv({provider, ...})` rather than importing all three provider builders and branching
 * itself.
 *
 * Every builder returns `{cli, role, argv, cwd, stdinFile?, outPath?, ...}` (fix round 3). The
 * spawner MUST honour two fields: when `stdinFile` is set, pipe that file's CONTENT to the child's
 * stdin (Claude and Codex closed-book roles — their argv carries no prompt pointer); when
 * `outPath` is set (Codex, always), read the result from it and delete it afterwards.
 */

import { buildClaudeArgv } from './claude.mjs';
import { buildCodexArgv } from './codex.mjs';
import { buildGrokArgv } from './grok.mjs';

export { buildClaudeArgv } from './claude.mjs';
export { buildCodexArgv } from './codex.mjs';
export { buildGrokArgv } from './grok.mjs';

/** @type {Readonly<Record<"anthropic"|"openai"|"xai", (params: any) => any>>} */
const BUILDERS = Object.freeze({ anthropic: buildClaudeArgv, openai: buildCodexArgv, xai: buildGrokArgv });

/**
 * @param {{provider: "anthropic"|"openai"|"xai"} & Record<string, any>} params
 * @returns {any} whichever provider builder's return shape (`{cli, role, argv, cwd, stdinFile?, outPath?, ...}`).
 */
export function buildArgv(params) {
  if (!params || !Object.hasOwn(BUILDERS, params.provider ?? '')) {
    // Fix round 1 (MINOR): the message is now built from BUILDERS itself — the same object the
    // check above uses — so it can never name a provider the check doesn't actually accept (the
    // old version imported a SEPARATE `PROVIDER_CLI_NAMES` map for the message, which could drift).
    throw new TypeError(`buildArgv: unknown provider "${params?.provider}" (expected one of ${Object.keys(BUILDERS).join(', ')})`);
  }
  return BUILDERS[/** @type {"anthropic"|"openai"|"xai"} */ (params.provider)](params);
}
