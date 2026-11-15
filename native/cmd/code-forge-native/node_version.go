package main

// B690d: the error-log `node` field, the gate-tree stamp's runtime slot and
// the `logs report` "- Node:" line once carried the version of the node a
// hand-over would run (B585 asked `node --version`). Go never starts Node now,
// so the value is always "none": the field stays, so older readers of
// errors.jsonl still find it, and no node process is started to fill it.
const errorLogNode = "none"
