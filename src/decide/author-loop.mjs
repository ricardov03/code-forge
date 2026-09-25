/**
 * The author loop's SHAPE (plan §3.7, O18) — types and pure loop-continuation logic only. The
 * actual `code-forge author` session runner (spawning a fresh L3 closed-book session, writing the
 * draft to `plans_dir`, printing questions to the human, collecting answers, re-invoking with
 * `--draft`/`--answers`) is B9's `src/session/author.mjs` (C3: "no `s2.mjs` here" — likewise no
 * session-spawning code here). What belongs to the DECISION layer is the deterministic question
 * of "is this loop done", which B9's runner calls after every round instead of re-deriving the
 * same rule inline.
 *
 * §3.7, verbatim: "stop when `questions[]` is empty or contains only non-blocking items the human
 * chose to skip." Read literally (fix round 1, review finding "spec deviation"): a BLOCKING
 * question in the latest round keeps the loop going, full stop — there is no "skip" path for a
 * blocking item, and `skippedIds` never clears one. Only a NON-blocking item can be cleared, and
 * only by being in `skippedIds` (the human explicitly chose to skip it — an item the human simply
 * hasn't looked at yet does NOT count). A blocking question stops being "in the latest round" only
 * because a LATER `author` call, given the human's real answer, omitted it — that happens upstream
 * of this module, in B9's loop.
 */

/**
 * @typedef {object} AuthorQuestion
 * @property {string} id
 * @property {string} question
 * @property {string[]} [options]
 * @property {string} [why]
 * @property {boolean} blocking
 */

/**
 * Validates the one shape this module actually depends on — FAILS CLOSED (review finding "fails
 * open"): `questions[]` is parsed LLM JSON, so a missing/typo'd `blocking` field must be a loud
 * `TypeError`, never a silent "treat as non-blocking" that could let the loop stop while a real
 * blocking question sits unanswered.
 * @param {any} q
 * @returns {asserts q is AuthorQuestion}
 */
function assertQuestionShape(q) {
  if (q === null || typeof q !== 'object') {
    throw new TypeError('author-loop: each question must be an object');
  }
  if (typeof q.id !== 'string' || q.id.length === 0) {
    throw new TypeError('author-loop: question.id must be a non-empty string');
  }
  if (typeof q.blocking !== 'boolean') {
    throw new TypeError(`author-loop: question ${JSON.stringify(q.id)}.blocking must be a boolean, got ${typeof q.blocking}`);
  }
}

/**
 * @param {any} questions - untrusted (parsed `author` JSON output); validated per-item, not just
 *   `Array.isArray` — see {@link assertQuestionShape}.
 * @returns {AuthorQuestion[]}
 */
export function blockingQuestions(questions) {
  if (!Array.isArray(questions)) {
    throw new TypeError('blockingQuestions: questions must be an array');
  }
  questions.forEach(assertQuestionShape);
  return questions.filter((q) => q.blocking === true);
}

/**
 * @param {any} questions - the LATEST round's `questions[]` from `author`'s output.
 * @param {Iterable<string>} [skippedIds] - NON-blocking ids the human explicitly chose to skip
 *   this round. Never clears a blocking id (§3.7 — see the module header).
 * @returns {boolean} `true` when the loop should stop.
 */
export function isAuthorLoopDone(questions, skippedIds = []) {
  if (!Array.isArray(questions)) {
    throw new TypeError('isAuthorLoopDone: questions must be an array');
  }
  if (questions.length === 0) {
    return true;
  }
  questions.forEach(assertQuestionShape);
  const skipped = skippedIds instanceof Set ? skippedIds : new Set(skippedIds);
  return questions.every((q) => !q.blocking && skipped.has(q.id));
}

/**
 * The blocking questions still standing in the way of stopping — under §3.7 that is EVERY
 * blocking question in `questions[]` (a blocking item is never "resolved" by a skip set; see the
 * module header). Kept as its own export, distinct from {@link blockingQuestions}, for the
 * call-site clarity of "what must the human still answer before this loop can finish".
 * @param {any} questions
 * @returns {AuthorQuestion[]}
 */
export function remainingBlockingQuestions(questions) {
  return blockingQuestions(questions);
}
