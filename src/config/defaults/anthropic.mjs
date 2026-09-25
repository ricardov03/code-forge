/**
 * Default level matrix for `provider: anthropic` (plan brief §3, 2026-09-24). Effort is omitted
 * where the brief's matrix leaves the cell blank — the Claude Agent tool takes no effort anyway
 * (plan §5.2: "`effort` cannot be passed to the Agent tool").
 * @type {{provider: "anthropic", levels: Record<"L0"|"L1"|"L2"|"L3", {model: string}>}}
 */
export default Object.freeze({
  provider: 'anthropic',
  levels: Object.freeze({
    L0: Object.freeze({ model: 'claude-haiku-4-5-20251001' }),
    L1: Object.freeze({ model: 'claude-sonnet-5' }),
    L2: Object.freeze({ model: 'claude-opus-5-5' }),
    L3: Object.freeze({ model: 'claude-fable-5-1' }),
  }),
});
