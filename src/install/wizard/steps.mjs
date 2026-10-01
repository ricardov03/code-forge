/**
 * The interactive half of `code-forge init` (plan §2, block B13a): one question per answer that
 * no flag already fixed, default on Enter, each with a one-line hint (B24). Gates and proof
 * settings come from the project and are shown in a summary (B24); they are asked only when the
 * person picks "Customize now" at {@link askSettingsChoice}. `ui` has `@clack/prompts`' shape (`select`,
 * `multiselect`, `text`, `confirm`, `password`, `isCancel`), so the real module is passed in
 * production and a scripted fake in tests. Step 5's key question lives in {@link askJevSource}
 * because it is asked only when the chain resolves nothing.
 */

import { isOpRef, OP_MESSAGES } from '../../keys/onepassword.mjs';
import { HARNESSES } from '../harnesses.mjs';
import { effectiveProvider, judgeCollision, proposeJudge } from './answers.mjs';
import { ENGINE_CHOICES, GATE_NAMES, PROVIDERS, parseLevel, shellWords, UsageError } from './flags.mjs';

export class CancelledError extends Error {}

/**
 * @typedef {object} Ui
 * @property {(o: {message: string, options: Array<{value: any, label?: string, hint?: string}>, initialValue?: any}) => Promise<any>} select
 * @property {(o: {message: string, options: Array<{value: any, label?: string}>, initialValues?: any[], required?: boolean}) => Promise<any>} multiselect
 * @property {(o: {message: string, initialValue?: string, placeholder?: string}) => Promise<any>} text
 * @property {(o: {message: string, initialValue?: boolean}) => Promise<any>} confirm
 * @property {(o: {message: string}) => Promise<any>} password
 * @property {(v: unknown) => boolean} isCancel
 */

/** @param {Ui} ui @param {Promise<any>} pending @returns {Promise<any>} */
async function answer(ui, pending) {
  const value = await pending;
  if (ui.isCancel(value)) throw new CancelledError('cancelled — nothing written');
  return value;
}

/** The 1Password option's hint: where to find the item ID, link or reference in the app. */
export const OP_REF_HINT = "Paste the item ID (from `op item list`), the item's link, or its op:// secret reference.";

/** The 1Password question (B25): an item ID or link is resolved to a full `op://` reference. */
export const OP_REF_QUESTION = '1Password item ID, item link, or op:// reference';

/** The question after "Enter a full op:// reference": op:// only, no lookup. */
export const OP_FULL_REF_QUESTION = 'Full op://vault/item/field reference';

/** The menu message when the resolver itself threw (its message is never shown). */
export const OP_RESOLVE_FAILED = '1Password CLI failed (unexpected error); run `op item get <item-id>` yourself to see why';

/** Printed when the Jev key is left unset, so the person knows how to set it later. */
export const JEV_LATER_TEXT = 'keys: jev not set — set it later with: code-forge keys set jev --op <item-id>';

/** The one-sentence hint line under each question (B24). */
export const HINTS = Object.freeze({
  tools: 'Recommended adds Solo and the Codex, Grok, Gemini and 1Password CLIs, each only after its own yes.',
  harnesses: 'The skill is linked into each harness you pick; detected ones are marked.',
  scope: 'project puts the link inside this repository; global puts it under your home folder.',
  method: 'A symlink follows package updates; a copy stays as installed.',
  provider: 'The model family every level uses unless a level names its own provider.',
  levels: 'L0 is the cheapest model and L3 the strongest; Enter keeps all four.',
  level: 'Enter keeps this level as shown.',
  refresh_models: 'Reads only local cache files; nothing is sent anywhere.',
  multimodel: 'Two providers review each block and a third one judges; it costs more per block.',
  second_provider: 'This provider reviews each block next to the L2 reviewer.',
  judge: 'The judge settles disagreements between the two reviewers.',
  engine: 'auto uses Solo when present, otherwise the subagents of the current harness.',
  solo_project: 'The Solo project that coder processes run in; leave blank for none.',
  jev: 'Jev is the small fast classifier behind System 1; skip uses rules only.',
  env: 'That variable must hold the key whenever code-forge runs.',
  paste: 'The key goes to the OS key store; the config keeps only a reference.',
  settings: 'Pick how to treat the gate and proof values shown above.',
  apply: 'Only the listed keys change; your other edits stay.',
});

/** The one-line hints of the "Customize now" questions, in the order they are asked (B24). */
export const SETTING_HINTS = Object.freeze({
  test: 'the command that runs your tests; the block cannot close if it fails (blank = no test gate)',
  lint: 'the command that runs your linter; the block cannot close if it fails (blank = no lint gate)',
  types: 'the command that runs your type checker; the block cannot close if it fails (blank = no types gate)',
  format: 'the command that runs your format check; the block cannot close if it fails (blank = no format gate)',
  high: 'globs where mistakes are costly (auth, billing); these files get deeper reviews and need red→green proof to close (blank = none)',
  isolation: 'export = run proof tests in a temporary copy (safe with parallel coders); lock = run in your folder and pause other coders',
  link_dirs: 'big folders the copy links to instead of copying, e.g. node_modules, vendor',
  copy_untracked: 'files git does not track but your tests need, e.g. .env, .env.testing',
});

/**
 * A comma list split only at brace depth 0, so a glob like `app/**\/*.{php,vue}` stays one item
 * and Enter on a pre-filled list gives the list back unchanged.
 * @param {string} text @returns {string[]}
 */
function csv(text) {
  const items = [];
  let current = '';
  let depth = 0;
  for (const ch of String(text ?? '')) {
    if (ch === '{') depth += 1;
    else if (ch === '}' && depth > 0) depth -= 1;
    if (ch === ',' && depth === 0) {
      items.push(current);
      current = '';
    } else {
      current += ch;
    }
  }
  items.push(current);
  return items.map((s) => s.trim()).filter((s) => s.length > 0);
}

/**
 * A question with its hint on the line below (clack prints a multi-line message as is).
 * @param {string} message @param {string} hint @returns {string}
 */
export function withHint(message, hint) {
  return `${message}\n${hint}`;
}

/**
 * The `model[:effort][@provider]` text a level is pre-filled with — the exact form `parseLevel`
 * reads back, so Enter keeps the level unchanged (provider override included).
 * @param {{model: string, effort?: string, provider?: string}} level @returns {string}
 */
export function levelText(level) {
  return `${level.model}${level.effort ? `:${level.effort}` : ''}${level.provider ? `@${level.provider}` : ''}`;
}

/**
 * Asks every answer whose source is not `flag`, in step order — gates and proof excepted (B24:
 * see {@link askProjectSettings}). Mutates and returns `values`.
 * @param {import('./answers.mjs').Answers} values
 * @param {Record<string, string>} sources
 * @param {import('./answers.mjs').WizardContext} ctx
 * @param {Ui} ui
 * @returns {Promise<import('./answers.mjs').Answers>}
 */
export async function askAnswers(values, sources, ctx, ui) {
  const open = (/** @type {string} */ key) => sources[key] !== 'flag';

  if (open('tools')) {
    values.tools = await answer(ui, ui.select({
      message: withHint('Install the recommended tools, or use only the current harness?', HINTS.tools),
      options: [{ value: 'current', label: 'current harness' }, { value: 'recommended', label: 'recommended tools' }],
      initialValue: values.tools,
    }));
  }
  if (open('harnesses')) {
    values.harnesses = await answer(ui, ui.multiselect({
      message: withHint('Install the skill into:', HINTS.harnesses),
      options: HARNESSES.map((h) => ({ value: h.id, label: `${h.label}${ctx.detectedHarnesses.includes(h.id) ? ' (detected)' : ''}` })),
      initialValues: values.harnesses,
      required: false,
    }));
  }
  if (open('scope')) {
    values.scope = await answer(ui, ui.select({ message: withHint('Scope', HINTS.scope), options: [{ value: 'project' }, { value: 'global' }], initialValue: values.scope }));
  }
  if (open('method')) {
    values.method = await answer(ui, ui.select({ message: withHint('Method', HINTS.method), options: [{ value: 'symlink' }, { value: 'copy' }], initialValue: values.method }));
  }
  if (open('provider')) {
    values.provider = await answer(ui, ui.select({ message: withHint('Default provider', HINTS.provider), options: PROVIDERS.map((p) => ({ value: p })), initialValue: values.provider }));
  }
  if (open('levels')) {
    const summary = ['L0', 'L1', 'L2', 'L3'].map((l) => `${l}=${levelText(values.levels[l])}`).join(' ');
    const keep = await answer(ui, ui.confirm({ message: withHint(`Keep the level matrix? ${summary}`, HINTS.levels), initialValue: true }));
    if (!keep) {
      for (const level of ['L0', 'L1', 'L2', 'L3']) {
        const shown = levelText(values.levels[level]);
        const typed = await answer(ui, ui.text({ message: withHint(`${level} model[:effort][@provider]`, HINTS.level), initialValue: shown }));
        // Enter (unchanged text) keeps the level object as it is, `fallback` included
        if (String(typed).trim() !== shown) values.levels[level] = parseLevel(`${level}=${String(typed).trim()}`).spec;
      }
    }
  }
  if (open('refresh_models')) {
    values.refresh_models = await answer(ui, ui.confirm({ message: withHint('Read model ids from the Codex/Grok CLI caches on this machine?', HINTS.refresh_models), initialValue: false }));
  }
  if (open('multimodel')) {
    values.multimodel = await answer(ui, ui.confirm({ message: withHint('Multimodel review (consensus)?', HINTS.multimodel), initialValue: values.multimodel }));
  }
  if (values.multimodel && open('second_provider')) {
    // R5: the second reviewer must differ from the provider L2 actually runs on, not the default one
    const reviewer1 = effectiveProvider(values, 'L2');
    const others = PROVIDERS.filter((p) => p !== reviewer1);
    values.second_provider = await answer(ui, ui.select({
      message: withHint(`Second provider (must differ from the effective L2 provider, ${reviewer1})`, HINTS.second_provider),
      options: others.map((p) => ({ value: p })),
      initialValue: others.includes(values.second_provider) ? values.second_provider : others[0],
    }));
  }
  // Consensus judge (§4.4, validator rule 7): the effective L3 must share a provider with neither
  // reviewer. Proposes the third provider's default L3; asked again until the answer differs. Not
  // asked when a flag-supplied second provider equals the effective L2 — `runInit` refuses that.
  const reviewersDiffer = values.second_provider !== effectiveProvider(values, 'L2');
  for (let collision = reviewersDiffer ? judgeCollision(values) : null; collision; collision = judgeCollision(values)) {
    const proposal = collision.third ? levelText(proposeJudge(collision.third)) : '';
    const typed = await answer(ui, ui.text({
      message: withHint(`L3 judge — must come from a third provider (reviewers: ${collision.reviewers.join(', ')}); model[:effort][@provider]`, HINTS.judge),
      initialValue: proposal,
    }));
    values.levels.L3 = parseLevel(`L3=${String(typed).trim()}`).spec;
  }
  // `subprocess` is never offered (R2); an existing `engine: subprocess` is kept, not asked.
  if (open('engine') && values.engine !== 'subprocess') {
    values.engine = await answer(ui, ui.select({ message: withHint('Engine', HINTS.engine), options: ENGINE_CHOICES.map((e) => ({ value: e })), initialValue: values.engine }));
  }
  if (open('solo_project') && ctx.solo && values.engine !== 'harness') {
    const typed = await answer(ui, ui.text({ message: withHint('Solo project id (blank: none)', HINTS.solo_project), initialValue: values.solo_project === null ? '' : String(values.solo_project) }));
    values.solo_project = /^[1-9]\d{0,9}$/.test(String(typed).trim()) ? Number(String(typed).trim()) : null;
  }
  return values;
}

/**
 * The one question after the settings summary (B24), asked every time in interactive mode.
 * @param {Ui} ui
 * @returns {Promise<'use'|'customize'|'later'>}
 */
export async function askSettingsChoice(ui) {
  return answer(ui, ui.select({
    message: withHint('Gates and proof settings:', HINTS.settings),
    options: [
      { value: 'use', label: 'Use these', hint: 'keep the detected values; blanks stay blank' },
      { value: 'customize', label: 'Customize now', hint: 'ask each gate and proof value, pre-filled with what was detected' },
      { value: 'later', label: 'Leave for later', hint: 'keep the detected values and print the keys to edit later' },
    ],
    initialValue: 'use',
  }));
}

/**
 * "Customize now": the gate and proof questions, each pre-filled with the value shown in the
 * summary (Enter keeps it), in the order test, lint, types, format, high-risk paths, isolation,
 * link_dirs, copy_untracked. A value a flag set is not asked (flags override). Mutates `values`
 * and returns the config keys whose value changed.
 * @param {import('./answers.mjs').Answers} values
 * @param {import('./answers.mjs').Origins} origins
 * @param {Ui} ui
 * @returns {Promise<string[]>}
 */
export async function askProjectSettings(values, origins, ui) {
  /** @type {string[]} */
  const changed = [];
  for (const name of GATE_NAMES) {
    if (origins.gates[name] === 'flag') continue;
    const gates = /** @type {Record<string, string[]|null>} */ (values.gates);
    const current = gates[name];
    const typed = await answer(ui, ui.text({
      message: withHint(`Gate ${name} (blank or "none": no gate)`, SETTING_HINTS[/** @type {keyof typeof SETTING_HINTS} */ (name)]),
      initialValue: current ? current.join(' ') : '',
    }));
    const text = String(typed ?? '').trim();
    const next = text === '' || text === 'none' ? null : shellWords(text);
    if (JSON.stringify(next) !== JSON.stringify(current)) changed.push(`gates.${name}`);
    gates[name] = next;
  }
  const proof = values.proof;
  /** @param {'high'|'link_dirs'|'copy_untracked'} key @param {string} message @param {string} path */
  const list = async (key, message, path) => {
    if (origins.proof[key] === 'flag') return;
    const next = csv(await answer(ui, ui.text({ message: withHint(message, SETTING_HINTS[key]), initialValue: proof[key].join(',') })));
    if (JSON.stringify(next) !== JSON.stringify(proof[key])) changed.push(path);
    proof[key] = next;
  };
  await list('high', 'High-risk paths (comma list of globs)', 'proof.tiers.high.paths');
  if (origins.proof.isolation !== 'flag') {
    const next = await answer(ui, ui.select({
      message: withHint('Proof isolation', SETTING_HINTS.isolation),
      options: [{ value: 'export' }, { value: 'lock' }],
      initialValue: proof.isolation,
    }));
    if (next !== proof.isolation) changed.push('proof.isolation');
    proof.isolation = next;
  }
  await list('link_dirs', 'Export: directories to link', 'proof.export.link_dirs');
  await list('copy_untracked', 'Export: untracked files to copy', 'proof.export.copy_untracked');
  return changed;
}

/**
 * @typedef {(input: string) => Promise<{ref: string|null, error?: string, kind?: string, title?: string, vault?: string, field?: string}>} OpResolver
 * @typedef {{ref: string|null, source: string, found?: {title?: string, vault?: string, field?: string}}} JevAnswer
 */

/**
 * The 1Password branch: ask for an item ID, link or reference, resolve it, and on a failure offer
 * Try again · Enter a full op:// reference · Choose another key source · Skip for now. Never throws
 * on a 1Password failure. Returns null for "choose another key source".
 * @param {Ui} ui
 * @param {OpResolver} resolve
 * @param {{input: string, error: string}|null} pending - start at the failure menu (a flag value that failed).
 * @returns {Promise<JevAnswer|null>}
 */
async function askOpRef(ui, resolve, pending) {
  let input = pending?.input ?? '';
  let error = pending?.error ?? null;
  let question = OP_REF_QUESTION;
  for (;;) {
    if (error === null) {
      input = String(await answer(ui, ui.text({ message: withHint(question, OP_REF_HINT), ...(input ? { initialValue: input } : {}) }))).trim();
      /** @type {Awaited<ReturnType<OpResolver>>} */
      let res;
      if (question !== OP_REF_QUESTION && !isOpRef(input)) {
        // "full reference" mode takes op:// only; nothing is looked up
        res = { ref: null, kind: 'op_bad_input', error: OP_MESSAGES.op_bad_input };
      } else {
        try {
          const got = await resolve(input);
          // a resolver that answers with anything but the documented shape counts as a failure
          res = got && typeof got === 'object' && (got.ref === null || typeof got.ref === 'string')
            ? got
            : { ref: null, kind: 'op_failed', error: OP_RESOLVE_FAILED };
        } catch {
          // never the thrown message: it could carry op output
          res = { ref: null, kind: 'op_failed', error: OP_RESOLVE_FAILED };
        }
      }
      if (res.ref !== null) {
        return { ref: res.ref, source: 'op', found: { title: res.title, vault: res.vault, field: res.field } };
      }
      error = res.error ?? OP_RESOLVE_FAILED;
    }
    const next = await answer(ui, ui.select({
      message: withHint('1Password lookup failed. What next?', error),
      options: [
        { value: 'retry', label: 'Try again' },
        { value: 'full', label: 'Enter a full op:// reference' },
        { value: 'other', label: 'Choose another key source' },
        { value: 'skip', label: 'Skip for now' },
      ],
      initialValue: 'retry',
    }));
    error = null;
    if (next === 'other') return null;
    if (next === 'skip') return { ref: null, source: 'none' };
    if (next === 'full') {
      question = OP_FULL_REF_QUESTION;
      input = '';
    } else {
      question = OP_REF_QUESTION;
    }
  }
}

/**
 * Step 5 when the chain resolved nothing: where is the Jev key? A pasted key is read with hidden
 * input and handed straight to `store`; it is never returned, echoed or logged. A 1Password item ID
 * or link is resolved to `op://<vaultId>/<itemId>/<fieldId>` through `opts.resolve` (B25).
 * @param {Ui} ui
 * @param {() => Promise<{put: (name: string, value: string, meta: {source: string, exp: number|null}) => Promise<void>}>} getStore
 * @param {{resolve?: OpResolver, pending?: {input: string, error: string}|null}} [opts]
 * @returns {Promise<JevAnswer>}
 */
export async function askJevSource(ui, getStore, { resolve, pending = null } = {}) {
  if (typeof resolve !== 'function') throw new TypeError('askJevSource: opts.resolve (the 1Password resolver) is required');
  if (pending) {
    const fromOp = await askOpRef(ui, resolve, pending);
    if (fromOp) return fromOp;
  }
  for (;;) {
    const choice = await answer(ui, ui.select({
      message: withHint('Where is the Jev key?', HINTS.jev),
      options: [
        { value: 'op', label: '1Password item or reference', hint: OP_REF_HINT },
        { value: 'env', label: 'environment variable name' },
        { value: 'paste', label: 'paste now (hidden)' },
        { value: 'skip', label: 'skip (rules-only System 1)' },
      ],
      initialValue: 'skip',
    }));
    if (choice === 'op') {
      const fromOp = await askOpRef(ui, resolve, null);
      if (fromOp) return fromOp;
      continue;
    }
    if (choice === 'env') {
      const name = String(await answer(ui, ui.text({ message: withHint('Environment variable name', HINTS.env) }))).trim();
      if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) throw new UsageError('not an environment variable name');
      return { ref: `env:${name}`, source: 'env' };
    }
    if (choice === 'paste') {
      const typed = await answer(ui, ui.password({ message: withHint('Jev key (input hidden)', HINTS.paste) }));
      if (typeof typed !== 'string' || typed.length === 0) return { ref: null, source: 'none' };
      const store = await getStore();
      await store.put('jev', typed, { source: 'user', exp: null });
      return { ref: 'user', source: 'keychain' };
    }
    return { ref: null, source: 'none' };
  }
}
