/**
 * Default level matrix for `provider: openai` (Codex CLI; plan brief §3, 2026-09-24).
 * @type {{provider: "openai", levels: Record<"L0"|"L1"|"L2"|"L3", {model: string, effort: string}>}}
 */
export default Object.freeze({
  provider: 'openai',
  levels: Object.freeze({
    L0: Object.freeze({ model: 'gpt-6-luna', effort: 'low' }),
    L1: Object.freeze({ model: 'gpt-6-luna', effort: 'high' }),
    L2: Object.freeze({ model: 'gpt-6-sol', effort: 'high' }),
    L3: Object.freeze({ model: 'gpt-6-astra', effort: 'high' }),
  }),
});
