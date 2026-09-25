### Block record — `<id>` `<title>`

| field | value |
|---|---|
| run / block | `<run id>` / `<id>` |
| level, attempt | `L<n>`, attempt `<n>` (escalations: `<trigger>` → `L<n+1>` …) |
| base sha | `<sha>` (rebased: `<old>` → `<new>` on `<date>`) |
| owned files | `<paths…>` |
| brief pointer | `BRIEF <path> lines=<n> sha=<sha8>` · ACK received `<yes/no>` |
| facts diff | `<n>` contradictions: `<one line each — plan said X, code says Y, ruling>` |
| forecast | cases `<n>` · lines `<n>` · orchestrator's floor: test lines ≥ `<cases × rate>`, docblocks ≥ 10 % |
| actual | cases `<n>` (runner count) · lines `<n>` (`git diff --stat`) · miss attributed to `<architect-added scope | coder under-forecast | none>` |
| review rounds | per file: `<path>` `<n>` rounds (`<stalled | converged | review_cap>`) |
| late findings | `<n>` ruled at close: `<fix_now | nit>` |
| dispositions / waivers | `<finding id → successor block>` (architect) · `review.waived` via `forge block waive <id> <finding> --run <r> --file <path> --reason "<why>"` (human) |
| spawn-flag defects | `<n>` — a coder parked at a permission or model prompt: closed and respawned with the pinned flags (`adapters/solo.md` §3) |
| gate | `<complete | stopped: <reason>>` at `<iso-8601>` |
| commit | `<sha>` · draft PR `<url or none>` |
| cost | coder `<$>` · review `<$>` · S1+S2 `<$>` · tokens `<reported | estimated>` |
