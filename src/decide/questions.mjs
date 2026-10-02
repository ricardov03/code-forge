/**
 * The S1 question registry (plan §3.2) — the seven typed questions Jev (or a fallback source)
 * answers, verbatim from the plan's table: `lane`, `risk`, `security_sensitive`, `next`, `scope`,
 * `defect`, `resolved`. Each entry's `type`/`instructions`/`criteria` is exactly what
 * `jev-client.mjs`'s `questions.<id>` argument needs; `fires` and `state` are documentation only
 * (they describe WHEN a caller asks and WHAT it puts in `state` — no code here builds `state`,
 * since that is facts the caller already has, e.g. the block record or a diff stat).
 *
 * `buildQuestionPayload` is the one function callers actually need: it renders one or more
 * registry entries into the `{<id>: {type, instructions, criteria}}` shape `askJev` sends,
 * honouring two config hooks from `system1` (§1.3, §8.3):
 *   - `system1.criteria_extra.<id>` — merges (never replaces) extra criteria labels into a
 *     question, and may override `instructions` with a project-specific phrasing.
 *   - `system1.disable` — a question id in this list is never sent to Jev at all;
 *     `buildQuestionPayload` throws rather than silently rendering it, so a caller can't forget to
 *     check `isQuestionDisabled` first and accidentally leak a diff hunk to Jev anyway.
 */

/**
 * @typedef {object} QuestionSpec
 * @property {'choice'|'score'|'noul'} type
 * @property {string} fires - documentation: when this question is asked.
 * @property {string} state - documentation: what `state` looks like for this question.
 * @property {string} instructions
 * @property {Record<string, string>} criteria
 */

/** @type {Readonly<Record<string, QuestionSpec>>} */
export const QUESTIONS = Object.freeze({
  lane: Object.freeze({
    type: 'choice',
    fires: 'plan: per block; code: on re-decomposition',
    state: '{block:{task, owned_files, acceptance}, facts_excerpt}',
    instructions: 'Which is the lowest model level that can reliably complete `block.task` within `block.owned_files`?',
    criteria: Object.freeze({
      L0: 'trivial',
      L1: 'plain feature, following an existing pattern',
      L2: 'hard (money, dates, tenancy, migrations, concurrency, unknown failure)',
      split: 'the block is too large or mixes two concerns — re-decompose before dispatch',
    }),
  }),
  risk: Object.freeze({
    type: 'score',
    fires: 'plan: per block; code: per finished file',
    state: '{block or file, diff_stat, paths}',
    instructions: 'How much damage could a subtle mistake here cause in production?',
    criteria: Object.freeze({
      0: 'cosmetic',
      1: 'wrong behaviour, easy to notice and revert',
      2: 'silent wrong data for some users',
      3: 'silent wrong money or cross-tenant leak',
    }),
  }),
  security_sensitive: Object.freeze({
    type: 'noul',
    fires: 'plan: per block; code: per file when path floors did not already force high',
    state: '{paths, diff_stat, symbols_touched}',
    instructions:
      'Does this change touch authentication, authorization, tenancy boundaries, secrets, money movement or webhook verification?',
    criteria: Object.freeze({ true: 'yes', false: 'no' }),
  }),
  next: Object.freeze({
    type: 'choice',
    fires: "code: after each attempt's tool gate",
    state: '{evidence:{tests, lint, types, format, attempt, previous_attempts, new_tests_added, no_new_tests_reason, forecast_vs_actual}}',
    instructions: 'Given `evidence` from deterministic checks after a coding attempt, what should the orchestrator do next?',
    criteria: Object.freeze({
      complete: 'all green, tests added or no_new_tests_reason given',
      retry: 'contained failure the same coder can fix in one more attempt',
      escalate: 'repeated or spreading failures, or risky area',
      stop: 'the evidence contradicts the brief (wrong base, missing dependency, acceptance impossible)',
    }),
  }),
  scope: Object.freeze({
    type: 'noul',
    fires: 'code: block gate, once, on the hunks outside the acceptance-named tests',
    state: '{acceptance, owned_files, hunks_outside_named_tests}',
    instructions: 'Do these hunks contain a change unrelated to `acceptance`?',
    criteria: Object.freeze({ true: 'yes', false: 'no' }),
  }),
  defect: Object.freeze({
    type: 'noul',
    fires: 'review: per finding that has not passed a judge',
    state: '{file_diff_hunk, finding}',
    instructions: 'Is `finding` a real defect in `file_diff_hunk` that must be fixed before merge (not a style preference)?',
    criteria: Object.freeze({ true: 'real correctness, safety or rule-violation defect', false: 'style, naming or preference only' }),
  }),
  resolved: Object.freeze({
    type: 'noul',
    fires: 'fix loop: per open finding after a fix',
    state: '{finding, fix_hunk}',
    instructions: 'Does `fix_hunk` resolve `finding` without introducing a new problem in the shown lines?',
    criteria: Object.freeze({ true: 'yes', false: 'no' }),
  }),
});

/** The 7 registered question ids, in table order. */
export const QUESTION_IDS = Object.freeze(Object.keys(QUESTIONS));

/**
 * @param {string} id
 * @returns {boolean}
 */
export function isKnownQuestion(id) {
  return typeof id === 'string' && Object.hasOwn(QUESTIONS, id);
}

/**
 * @param {string} id
 * @param {Record<string, any>} [cfg]
 * @returns {boolean} `true` when `system1.disable` lists `id` — this question must never be sent
 *   to Jev (§8.3: "for projects that will not send diff hunks to Jev").
 */
export function isQuestionDisabled(id, cfg = {}) {
  const disabled = cfg?.system1?.disable;
  return Array.isArray(disabled) && disabled.includes(id);
}

/**
 * Merge a question's shipped `criteria` with `system1.criteria_extra.<id>.criteria` (extra keys
 * win on a collision — a project override is deliberate) and pick `instructions` (the extra one
 * wins when given).
 * @param {string} id
 * @param {Record<string, any>} [cfg]
 * @returns {{type: 'choice'|'score'|'noul', instructions: string, criteria: Record<string, string>}}
 * @throws {TypeError} for an unknown id, or one `system1.disable` lists.
 */
export function renderQuestion(id, cfg = {}) {
  if (!isKnownQuestion(id)) {
    throw new TypeError(`renderQuestion: unknown question id ${JSON.stringify(id)}`);
  }
  if (isQuestionDisabled(id, cfg)) {
    throw new TypeError(`renderQuestion: question ${JSON.stringify(id)} is disabled by system1.disable`);
  }
  const spec = QUESTIONS[id];
  const extra = cfg?.system1?.criteria_extra?.[id] ?? {};
  return {
    type: spec.type,
    instructions: typeof extra.instructions === 'string' && extra.instructions.length > 0 ? extra.instructions : spec.instructions,
    criteria: { ...spec.criteria, ...(extra.criteria ?? {}) },
  };
}

/**
 * Render one or more question ids into the `{<id>: {type, instructions, criteria}}` shape
 * `askJev`'s `questions` argument takes.
 * @param {string[]} ids
 * @param {Record<string, any>} [cfg]
 * @returns {Record<string, {type: 'choice'|'score'|'noul', instructions: string, criteria: Record<string, string> | string[]}>}
 */
export function buildQuestionPayload(ids, cfg = {}) {
  if (!Array.isArray(ids) || ids.length === 0) {
    throw new TypeError('buildQuestionPayload: ids must be a non-empty array');
  }
  /** @type {Record<string, {type: 'choice'|'score'|'noul', instructions: string, criteria: Record<string, string> | string[]}>} */
  const out = {};
  for (const id of ids) {
    out[id] = toWire(renderQuestion(id, cfg));
  }
  return out;
}

/**
 * Jev's wire shape: a `score` question takes `criteria` as a LIST whose index is the score (an
 * object keyed `0..n` is refused with 422 "Input should be a valid list"); `choice` and `noul`
 * keep the keyed object. The answer's `legend`/`probabilities` come back keyed `"0".."n"`.
 * @param {{type: 'choice'|'score'|'noul', instructions: string, criteria: Record<string, string>}} q
 * @returns {{type: 'choice'|'score'|'noul', instructions: string, criteria: Record<string, string> | string[]}}
 */
function toWire(q) {
  if (q.type !== 'score') return q;
  const keys = Object.keys(q.criteria).sort((a, b) => Number(a) - Number(b));
  return { ...q, criteria: keys.map((k) => q.criteria[k]) };
}
