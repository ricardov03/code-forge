/**
 * B80: the argument forms of the human verbs `block waive` and `block reopen`, in ONE place, so the
 * check that runs before the terminal question (`src/cli/block.mjs`) and the verbs themselves
 * (`waiveFinding`, `reopenFile`) can never drift apart. Every refusal is `usage` (exit 2).
 *
 * A control character (any char below 0x20, or 0x7f) is refused before anything else: such a
 * value would reach the terminal question and the stderr messages that quote it.
 */
import path from 'node:path';
import { StateError } from '../state/paths.mjs';

/** The finding id form a waive takes. */
const FINDING_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/;
// eslint-disable-next-line no-control-regex
const CONTROL = /[\x00-\x1f\x7f]/;

/**
 * @param {unknown} finding
 * @throws {StateError} `usage` for a value with a control character or not of the finding id form.
 */
export function checkFindingId(finding) {
  if (typeof finding === 'string' && CONTROL.test(finding)) throw new StateError('usage', 'a finding id must not contain control characters');
  if (typeof finding !== 'string' || !FINDING_ID.test(finding)) throw new StateError('usage', 'a finding id is required (letters, digits, . _ : -)');
}

/**
 * The `--file` form: no control character, then a repo-root-relative path (not absolute, no `..`
 * segment, no leading `./`). `normalized` also refuses a path that `path.posix.normalize` changes
 * — the rule `block reopen` has always had; `block waive` keeps its own (B80 does not change it).
 * @param {unknown} file @param {{normalized: boolean}} rule
 * @throws {StateError} `usage`.
 */
export function checkFileForm(file, { normalized }) {
  if (typeof file !== 'string' || file === '') throw new StateError('usage', '--file is required');
  if (CONTROL.test(file)) throw new StateError('usage', '--file must not contain control characters');
  if (path.isAbsolute(file) || file.split('/').includes('..') || file.startsWith('./') || (normalized && path.posix.normalize(file) !== file)) {
    throw new StateError('usage', '--file must be repo-root-relative');
  }
}
