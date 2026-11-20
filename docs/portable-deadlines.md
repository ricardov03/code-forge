# Portable command deadlines

Use code-forge's deadline flags on macOS and Linux. They run in Go and do not need the
GNU `timeout` executable. The limits apply to different operations:

| Operation | Command | What the limit controls |
|---|---|---|
| Gates | `code-forge gates run --timeout-ms 30000` | Each gate command has a 30-second limit. `gates.timeout_s` sets the configuration default. |
| Review a change | `code-forge review --max 900` | One 900-second deadline for the review operation. |
| Wait for a ticket | `code-forge review-file --wait <ticket> --max 90s` | Wait up to 90 seconds for the ticket; the wait deadline does not cancel the worker's reviewer session. |
| Wait for a block | `code-forge review-file --wait-block <block> --run <run> --max 90s` | One 90-second deadline for waiting for the block's files. |
| Watch CI | `code-forge ci watch --sha <sha> --timeout 30m` | Poll CI for up to 30 minutes. |

A reviewer session's own limit is `review.session_timeout_s`. Use
[`review.routes`](review-only.md) to give larger files a longer session
limit. Waiting longer for a ticket does not change that limit.

For Go tests, use Go's own deadline and select tests by name:

```sh
GOFLAGS=-trimpath go test ./path/to/package -run '^(TestName)$' -count=1 -p 2 -timeout 10m
```

See [review only](review-only.md), [CI watch](how-it-works.md), and the
[command reference](reference/commands.md) for flags and exit codes. The Go gate runner
and provider session runner enforce their deadlines themselves, including stopping their
child process groups; a shell deadline wrapper is unnecessary for these operations.

## B1020 audit

At base `e89f7451d654aa6be98c1cbe1c9db1984a83a87a`, no tracked script or shipped documentation
command invokes GNU `timeout`. This includes `bin/`, `scripts/`, `tools/`, `.github/`,
`examples/`, `README.md`, `docs/` and `skill/`. The npm package's file list includes `bin/`,
`schema/`, `skill/`, `docs/reference/`, `examples/`, `scripts/install.sh`, the README,
changelog and license. Local, ignored lane plans are outside that package.

The audit searched tracked files for the word `timeout`, then inspected command-like
matches. Occurrences in the shipped command reference, schema descriptions and examples
are built-in flags, configuration or prose. `go test -timeout`, `--timeout-ms`,
`--timeout`, and result labels such as `unavailable: timeout` do not invoke the GNU program.
Keep these forms. There was no GNU command call to replace and no runtime behavior change
requiring a new regression test.
