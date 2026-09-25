/**
 * Default level matrix for `provider: openai` (Codex CLI; plan brief §3, 2026-09-24). `caps.coders`
 * (v1.3/B1.1, R10: "at most 2 coders at a time") is not provider-specific, but is carried on every
 * provider default so the `init` wizard writes it regardless of which provider a project picks.
 * @type {{provider: "openai", levels: Record<"L0"|"L1"|"L2"|"L3", {model: string, effort: string}>, caps: {coders: number}}}
 */
export default Object.freeze({
  provider: 'openai',
  levels: Object.freeze({
    L0: Object.freeze({ model: 'gpt-6-luna', effort: 'low' }),
    L1: Object.freeze({ model: 'gpt-6-luna', effort: 'high' }),
    L2: Object.freeze({ model: 'gpt-6-sol', effort: 'high' }),
    L3: Object.freeze({ model: 'gpt-6-astra', effort: 'high' }),
  }),
  caps: Object.freeze({ coders: 2 }),
});
