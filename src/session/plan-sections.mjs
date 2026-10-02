/**
 * The plan sections `forge plan check` requires, by their exact headings (B36). One source for
 * the author's prompt (`author.mjs` lists these headings in the Plan job's rules), the check
 * (`plan-check.mjs` finds the sections by them) and the template (`skill/templates/plan.md`
 * carries them; a test keeps the three in step). Callers address a section by its id, never by
 * its place in a list.
 *
 * A section is found by its exact heading: the heading's text after its number equals `heading`
 * (case-insensitive, trimmed). A heading that misses the wording is accepted, with a WARN naming
 * the exact heading, only when its number is the section's expected one (`position`), its text
 * names one of the section's distinctive `keywords`, and its text is not another required
 * section's heading. Anything else is a missing section.
 */

/**
 * @typedef {'unbackable' | 'callerMap'} SectionId
 * @typedef {object} RequiredSection
 * @property {SectionId} id
 * @property {string} heading - the exact heading text (without its number).
 * @property {string} label - the expected position as the template numbers it.
 * @property {RegExp} position - matches the heading's number (`§0.x`, `0.6` ⇒ `0.x`, `0.6`).
 * @property {string[]} keywords - distinctive lowercase words a near-miss heading must name one of.
 * @property {string} purpose - what the author writes under it (the prompt's one line per section).
 */

/** @type {Readonly<Record<SectionId, Readonly<RequiredSection>>>} */
export const REQUIRED_SECTIONS = Object.freeze({
  unbackable: Object.freeze({
    id: 'unbackable',
    heading: 'Acceptance clauses the facts sheet cannot back',
    label: '§0.x',
    position: /^0\.\w+$/,
    // distinctive words only: a plain '§0.1 Facts sheet' heading must not pass as this section
    keywords: ['unbackable', 'cannot back'],
    purpose: 'every clause that cites a claim the sheet marks NOT-FOUND or UNVERIFIABLE, each with a tolerance naming the block; `none` when there is none',
  }),
  callerMap: Object.freeze({
    id: 'callerMap',
    heading: 'Caller map',
    label: '§3',
    position: /^3$/,
    keywords: ['caller map', 'callers'],
    purpose: 'mechanism → the block that ships it → the block that calls it; every block id appears',
  }),
});

/** @returns {Readonly<RequiredSection>[]} every required section, in template order. */
export const requiredSections = () => Object.values(REQUIRED_SECTIONS);

/**
 * @param {string} title - a heading's text (markdown emphasis already stripped).
 * @returns {{number: string | null, text: string}} its leading number (`§0.x`, `0.6.`, `3.` ⇒
 *   `0.x`, `0.6`, `3`) and the rest.
 */
export function splitHeading(title) {
  const m = /^§?\s*(\d+(?:\.\w+)*)\.?\s+(.*)$/.exec(title.trim());
  return m ? { number: m[1], text: m[2].trim() } : { number: null, text: title.trim() };
}

/**
 * @template {{title: string}} S
 * @param {S[]} sections - the plan's headings, in order.
 * @param {RequiredSection} spec
 * @returns {{section: S, exact: boolean} | null} the first exact match, else the first near miss
 *   at the expected position, else null.
 */
export function findSection(sections, spec) {
  const textOf = (/** @type {S} */ s) => splitHeading(s.title).text.toLowerCase();
  const want = spec.heading.toLowerCase();
  const exact = sections.find((s) => textOf(s) === want);
  if (exact) return { section: exact, exact: true };
  const others = requiredSections()
    .filter((o) => o.id !== spec.id)
    .map((o) => o.heading.toLowerCase());
  const near = sections.find((s) => {
    const { number } = splitHeading(s.title);
    const text = textOf(s);
    return number !== null && spec.position.test(number) && !others.includes(text) && spec.keywords.some((k) => text.includes(k));
  });
  return near ? { section: near, exact: false } : null;
}

/**
 * @param {string} found - the near-miss heading as written.
 * @param {RequiredSection} spec
 * @returns {string} the WARN line `plan check` prints for it.
 */
export function nearMissWarning(found, spec) {
  return `WARN section "${found}" at ${spec.label} read as "${spec.heading}" — use the exact heading "${spec.heading}"`;
}

/** @returns {string[]} the Plan job's section rule lines for the author packet: each heading once. */
export function authorHeadingRules() {
  return [
    'Use these section headings exactly, word for word, at these positions (plan check finds the sections by them):',
    ...requiredSections().map((s) => `- "${s.label} ${s.heading}" — ${s.purpose}.`),
  ];
}
