# @ricardov/code-forge

A CLI (`code-forge`) plus one Agent Skill that runs the plan → harden → code → review pipeline for
cross-model, parallel, evidence-gated feature delivery — on Claude Code, Codex, Grok, and Solo.

> **Status:** under construction. This package is being built block by block per
> `plans/code-forge-plan-v1.2.md`. `bin/code-forge.mjs` currently ships only the scaffolding verbs
> (`help`, `version`); the decision layer, review engine, proof policy, and installer land in later
> blocks (see the plan's §10 build-block table).

## What ships

- A CLI (`code-forge`), installed globally via `npx @ricardov/code-forge init` into every detected
  harness (Claude Code, Codex, Grok, …).
- One Agent Skill (`skill/SKILL.md` + `references/`) that is prose only — every fact (model ids,
  efforts, commands, thresholds, paths) lives in config or in the CLI.
- A JSON Schema for `.code-forge.yml` (`schema/code-forge.schema.json`).

See `plans/code-forge-plan-v1.2.md` for the full architecture, decision layer, review engine, proof
policy, security model, and the build-block breakdown.

## Development

```bash
npm install
npm test              # node --experimental-test-module-mocks --test 'test/**/*.test.mjs'
npm run typecheck     # tsc --checkJs --noEmit
npm run stryker       # mutation testing (Stryker)
```

Node >= 22 required (ruling R7). No build step — the package ships plain ESM `.mjs`, run directly by Node.
