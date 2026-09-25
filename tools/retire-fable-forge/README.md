# @ricardov/retire-fable-forge

Retires the old `/fable-forge` Agent Skill, which `/code-forge` replaces. This is a standalone package, separate from `@ricardov/code-forge`.

```sh
npx @ricardov/retire-fable-forge                   # list installs, print the plan, change nothing
npx @ricardov/retire-fable-forge --alias --yes     # step 1: keep a 5-line alias that points to /code-forge
npx @ricardov/retire-fable-forge --remove --yes    # step 2: delete the skill
```

Flags: `--alias` | `--remove`, `--yes`, `--dry-run`, `--home <dir>`, `--help`.

## What it looks at

- User level: `~/<marker>/skills/fable-forge` for `.claude`, `.agents`, `.codex`, `.grok`, `.gemini`, `.cursor` and `.copilot`.
- Project level (the current directory): `.claude/skills/fable-forge` and `.agents/skills/fable-forge`.

Each install is reported as a `directory`, a `symlink -> target`, or a `copy` (a folder whose `SKILL.md` matches another install's byte for byte). Any `~/.claude/projects/*/memory/MEMORY.md` line that mentions fable-forge is listed. The tool reports these lines and never edits them.

## Safety

- With no mode, or with `--dry-run`, nothing changes.
- The tool acts only on paths that are exactly `<skills root>/fable-forge`, and only when the folder's `SKILL.md` frontmatter says `name: fable-forge`. It never touches `code-forge`. If any install fails a check, the tool changes nothing.
- `--alias` and `--remove` need `--yes`. Without it the tool asks on a TTY and refuses when there is no TTY.
- A symlink is unlinked and its target is left as it is. With `--alias`, the link is replaced by a real folder that holds only the alias.
- Before any change, each install is saved to `~/.code-forge-retired/fable-forge-<iso>-<slug>.tgz`. The tool checks that the archive can be listed and prints a `restore:` command, for example:

  ```sh
  mkdir -p ~/.claude/skills && tar -xzf ~/.code-forge-retired/fable-forge-<iso>-user-claude.tgz -C ~/.claude/skills
  ```

Exit codes: `0` done or nothing to retire, `1` refused or failed, `2` usage error.

## Test

```sh
node --test tools/retire-fable-forge/test/retire.test.mjs
```

The tests use only a temporary HOME and a temporary cwd.
