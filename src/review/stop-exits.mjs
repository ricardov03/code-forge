/**
 * B73 (issue #12): the ways out of a file the fix loop stopped (`review_cap`,
 * `l3_patch_exhausted`, `split_required`), in this order — `block reopen` after a fix,
 * `block waive` for each open finding, `review-file --answers` for a fix made in another owned
 * file, or re-decompose the block. `review-file`'s `stopped` answer carries them as `exits`; the
 * gate appends {@link stopExitsLine} to its `review_cap` and `unreviewed` detail for a stopped
 * file. A `tampered` review state is not a stop (it is refused; the owner's way out is
 * `block close --no-require-reviews`) and never gets them.
 */

/** @type {ReadonlyArray<string>} */
export const STOP_EXITS = Object.freeze(['block reopen', 'block waive', 'review-file --answers', 're-decompose']);

/** The one short line the gate appends to a stopped file's detail. */
export const stopExitsLine = `exits: ${STOP_EXITS.join(', ')}`;
