/**
 * The interactive half of `code-forge init` (plan §2, block B13a): one question per answer that
 * no flag already fixed, default on Enter. `ui` has `@clack/prompts`' shape (`select`,
 * `multiselect`, `text`, `confirm`, `password`, `isCancel`), so the real module is passed in
 * production and a scripted fake in tests. Step 5's key question lives in {@link askJevSource}
 * because it is asked only when the chain resolves nothing.
 */

import { isOpRef } from '../../keys/onepassword.mjs';
import { HARNESSES } from '../harnesses.mjs';
import { effectiveProvider, judgeCollision, proposeJudge } from './answers.mjs';
import { ENGINE_CHOICES, GATE_NAMES, PROVIDERS, parseLevel, shellWords, UsageError } from './flags.mjs';

export class CancelledError extends Error {}

/**
 * @typedef {object} Ui
 * @property {(o: {message: string, options: Array<{value: any, label?: string}>, initialValue?: any}) => Promise<any>} select
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
 * The `model[:effort][@provider]` text a level is pre-filled with — the exact form `parseLevel`
 * reads back, so Enter keeps the level unchanged (provider override included).
 * @param {{model: string, effort?: string, provider?: string}} level @returns {string}
 */
export function levelText(level) {
  return `${level.model}${level.effort ? `:${level.effort}` : ''}${level.provider ? `@${level.provider}` : ''}`;
}

/**
 * Asks every answer whose source is not `flag`, in step order. Mutates and returns `values`.
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
      message: 'Install the recommended tools, or use only the current harness?',
      options: [{ value: 'current', label: 'current harness' }, { value: 'recommended', label: 'recommended tools' }],
      initialValue: values.tools,
    }));
  }
  if (open('harnesses')) {
    values.harnesses = await answer(ui, ui.multiselect({
      message: 'Install the skill into:',
      options: HARNESSES.map((h) => ({ value: h.id, label: `${h.label}${ctx.detectedHarnesses.includes(h.id) ? ' (detected)' : ''}` })),
      initialValues: values.harnesses,
      required: false,
    }));
  }
  if (open('scope')) {
    values.scope = await answer(ui, ui.select({ message: 'Scope', options: [{ value: 'project' }, { value: 'global' }], initialValue: values.scope }));
  }
  if (open('method')) {
    values.method = await answer(ui, ui.select({ message: 'Method', options: [{ value: 'symlink' }, { value: 'copy' }], initialValue: values.method }));
  }
  if (open('provider')) {
    values.provider = await answer(ui, ui.select({ message: 'Default provider', options: PROVIDERS.map((p) => ({ value: p })), initialValue: values.provider }));
  }
  if (open('levels')) {
    const summary = ['L0', 'L1', 'L2', 'L3'].map((l) => `${l}=${levelText(values.levels[l])}`).join(' ');
    const keep = await answer(ui, ui.confirm({ message: `Keep the level matrix? ${summary}`, initialValue: true }));
    if (!keep) {
      for (const level of ['L0', 'L1', 'L2', 'L3']) {
        const shown = levelText(values.levels[level]);
        const typed = await answer(ui, ui.text({ message: `${level} model[:effort][@provider]`, initialValue: shown }));
        // Enter (unchanged text) keeps the level object as it is, `fallback` included
        if (String(typed).trim() !== shown) values.levels[level] = parseLevel(`${level}=${String(typed).trim()}`).spec;
      }
    }
  }
  if (open('refresh_models')) {
    values.refresh_models = await answer(ui, ui.confirm({ message: 'Read model ids from the Codex/Grok CLI caches on this machine?', initialValue: false }));
  }
  if (open('multimodel')) {
    values.multimodel = await answer(ui, ui.confirm({ message: 'Multimodel review (consensus)?', initialValue: values.multimodel }));
  }
  if (values.multimodel && open('second_provider')) {
    // R5: the second reviewer must differ from the provider L2 actually runs on, not the default one
    const reviewer1 = effectiveProvider(values, 'L2');
    const others = PROVIDERS.filter((p) => p !== reviewer1);
    values.second_provider = await answer(ui, ui.select({
      message: `Second provider (must differ from the effective L2 provider, ${reviewer1})`,
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
      message: `L3 judge — must come from a third provider (reviewers: ${collision.reviewers.join(', ')}); model[:effort][@provider]`,
      initialValue: proposal,
    }));
    values.levels.L3 = parseLevel(`L3=${String(typed).trim()}`).spec;
  }
  // `subprocess` is never offered (R2); an existing `engine: subprocess` is kept, not asked.
  if (open('engine') && values.engine !== 'subprocess') {
    values.engine = await answer(ui, ui.select({ message: 'Engine', options: ENGINE_CHOICES.map((e) => ({ value: e })), initialValue: values.engine }));
  }
  if (open('solo_project') && ctx.solo && values.engine !== 'harness') {
    const typed = await answer(ui, ui.text({ message: 'Solo project id (blank: none)', initialValue: values.solo_project === null ? '' : String(values.solo_project) }));
    values.solo_project = /^[1-9]\d{0,9}$/.test(String(typed).trim()) ? Number(String(typed).trim()) : null;
  }
  if (open('gates')) {
    for (const name of GATE_NAMES) {
      const current = values.gates[name];
      const typed = await answer(ui, ui.text({ message: `Gate ${name} (blank or "none": no gate)`, initialValue: current ? current.join(' ') : '' }));
      const text = String(typed ?? '').trim();
      values.gates[name] = text === '' || text === 'none' ? null : shellWords(text);
    }
  }
  if (open('proof')) {
    values.proof.high = csv(await answer(ui, ui.text({ message: 'High-risk paths (comma list of globs)', initialValue: values.proof.high.join(',') })));
    values.proof.isolation = await answer(ui, ui.select({ message: 'Proof isolation', options: [{ value: 'export' }, { value: 'lock' }], initialValue: values.proof.isolation }));
    values.proof.link_dirs = csv(await answer(ui, ui.text({ message: 'Export: directories to link', initialValue: values.proof.link_dirs.join(',') })));
    values.proof.copy_untracked = csv(await answer(ui, ui.text({ message: 'Export: untracked files to copy', initialValue: values.proof.copy_untracked.join(',') })));
  }
  return values;
}

/**
 * Step 5 when the chain resolved nothing: where is the Jev key? A pasted key is read with hidden
 * input and handed straight to `store`; it is never returned, echoed or logged.
 * @param {Ui} ui
 * @param {() => Promise<{put: (name: string, value: string, meta: {source: string, exp: number|null}) => Promise<void>}>} getStore
 * @returns {Promise<{ref: string|null, source: string}>}
 */
export async function askJevSource(ui, getStore) {
  const choice = await answer(ui, ui.select({
    message: 'Where is the Jev key?',
    options: [
      { value: 'op', label: '1Password reference' },
      { value: 'env', label: 'environment variable name' },
      { value: 'paste', label: 'paste now (hidden)' },
      { value: 'skip', label: 'skip (rules-only System 1)' },
    ],
    initialValue: 'skip',
  }));
  if (choice === 'op') {
    const ref = String(await answer(ui, ui.text({ message: '1Password reference (op://vault/item/field)' }))).trim();
    if (!isOpRef(ref)) throw new UsageError('not an op://vault/item/field reference');
    return { ref, source: 'op' };
  }
  if (choice === 'env') {
    const name = String(await answer(ui, ui.text({ message: 'Environment variable name' }))).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) throw new UsageError('not an environment variable name');
    return { ref: `env:${name}`, source: 'env' };
  }
  if (choice === 'paste') {
    const typed = await answer(ui, ui.password({ message: 'Jev key (input hidden)' }));
    if (typeof typed !== 'string' || typed.length === 0) return { ref: null, source: 'none' };
    const store = await getStore();
    await store.put('jev', typed, { source: 'user', exp: null });
    return { ref: 'user', source: 'keychain' };
  }
  return { ref: null, source: 'none' };
}
