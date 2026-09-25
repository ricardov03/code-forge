// Fixture (test/imports-order.test.mjs): a side-effect import (no `from`, no binding) of FXA's
// batch-mate FXB — the checker must catch this shape too, not only `import {x} from '...'`.
import '../block-y/thing.mjs';
