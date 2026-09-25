// Fixture (test/imports-order.test.mjs): FXA's cli verb file. src/cli/ is shared by every block,
// so it is checked by this FILE's owner (FXA): importing FXA's own module is allowed; importing
// FXB's module and FXB's cli verb file (both batch-mates, not in depends_on) must be caught.
import { x } from '../block-x/mod.mjs';
import { helper } from '../block-y/thing.mjs';
import { verbY } from './verb-y.mjs';

export const verbX = () => x + helper() + verbY();
