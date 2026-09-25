/**
 * Default level matrix for `provider: xai` (Grok CLI; plan brief §3, 2026-09-24). xAI has no
 * cheap small model, so L0 uses the same `grok-4.7` id as every other level, at `low` effort.
 * `caps.coders` (v1.3/B1.1, R10: "at most 2 coders at a time") is not provider-specific, but is
 * carried on every provider default so the `init` wizard writes it regardless of which provider a
 * project picks.
 * @type {{provider: "xai", levels: Record<"L0"|"L1"|"L2"|"L3", {model: string, effort: string}>, caps: {coders: number}}}
 */
export default Object.freeze({
  provider: 'xai',
  levels: Object.freeze({
    L0: Object.freeze({ model: 'grok-4.7', effort: 'low' }),
    L1: Object.freeze({ model: 'grok-4.7', effort: 'medium' }),
    L2: Object.freeze({ model: 'grok-4.7', effort: 'high' }),
    L3: Object.freeze({ model: 'grok-4.7', effort: 'xhigh' }),
  }),
  caps: Object.freeze({ coders: 2 }),
});
