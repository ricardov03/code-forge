# Native accelerator

`native/` contains the optional Go accelerator and compatibility packages for code-forge.
The build is pure Go, uses only the standard library, and keeps CGO off (`CGO_ENABLED=0`).
This also avoids the macOS 26 LC_UUID linker error during local tests.
Test from the repository root:

```sh
cd native && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
```

`state.ReadRun(home, runID)` returns a `ledger.Object`, retaining nested JSON
property order. Change properties with `Set`, then call `state.SaveRun(home,
record)` inside `state.WithRunLock(home, runID, fn)`. The mkdir lock interoperates
with Node (5-second timeout, 25ms polling). Saves replace the authoritative record
at mode 0600 and write a workspace mirror without `worker`.

`ledger.AppendObject(dir, slug, row, rotateAtBytes)` preserves input order and uses
JSON.stringify-compatible scalar encoding. `ledger.AppendRow` accepts ordinary
Go maps, whose keys have no insertion order, and sorts those input keys. Use
`ledger.DecodeObject` for byte parity with an existing Node object; integer-index
keys enumerate first in numeric order. Both append APIs normalize the row, rotate
at the pre-append byte threshold, and discard a torn final fragment. Pass
`ledger.DefaultRotateAtBytes` for Node's default 10 MiB threshold.

`state.WriteSigned(home, runID, row)` loads the private 32-byte Node run key,
signs through `native/sign`, and appends to the project slug in the run record.

Each ledger line is published by one O_APPEND write. A per-slug mkdir lock
serializes Go writers through rotation and tail repair. The existing Node ledger
writer does not acquire this lock: mixed writers share atomic line appends, but
concurrent rotation or torn-tail repair with a Node writer remains subject to
the Node writer's existing races.

The hidden `state-golden --home <dir> --run <id> --op append|save|lock` command is
only a compatibility-test driver. `append` reads `{slug, rows, rotateAtBytes?,
signed?}` from stdin; signed rows use the run's project. `save` reads an ordered
object of updates and merges it into the existing record under the run lock.
`lock` prints `locked` after acquiring the lock and holds it until stdin closes.
Build the host binary before running the Go write tables:

```sh
node scripts/build-native.mjs --host
```
