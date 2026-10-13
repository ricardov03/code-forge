// state helpers FIRST: the import-time guard moves $HOME and cwd to a temp dir before any src module loads.
import { captureStream, countOccurrences, withFixture } from '../state/helpers.mjs';
import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import { describe, test } from 'node:test';
import { runRun } from '../../src/cli/run.mjs';
import { readAllRows } from '../../src/ledger/write.mjs';
import { readRun } from '../../src/state/run.mjs';
import { snapshotFor } from '../../src/state/config-snapshot.mjs';
import { validateConfig } from '../../src/config/validate.mjs';
import { routeTicket, ruleProvider } from '../../src/review/route.mjs';
import { CONFIG_YAML } from '../fixtures/repos/two-blocks/build.mjs';

// B90 (6)(7): config rule 18 `review-routes-invalid`, rule 16 on a Codex route, the schema shape,
// and `run reload` accepting a `review.routes` change.

const BIG = 'grok-4.7';

/** A valid config with `review` merged in. @param {Record<string, any>} [review] @returns {Record<string, any>} */
function cfgWith(review = {}) {
  return structuredClone({
    version: 1,
    provider: 'anthropic',
    levels: {
      L0: { model: 'claude-haiku-4-5-20251001' },
      L1: { model: 'claude-sonnet-5' },
      L2: { model: 'claude-opus-5-5' },
      L3: { model: 'claude-fable-5-1' },
    },
    review,
  });
}

/** @param {ReturnType<typeof validateConfig>} result @param {string} rule */
const issuesOf = (result, rule) => [...result.errors, ...result.warnings].filter((i) => i.rule === rule);

describe('B90 (6): review.routes validation', () => {
  test('control: two valid rules (L2 and L3), one with a second level ⇒ valid, no issue at all', () => {
    const r = validateConfig(
      cfgWith({
        routes: [
          { min_lines: 400, model: BIG, provider: 'xai', session_timeout_s: 900, second: { provider: 'anthropic', model: 'claude-sonnet-5' } },
          { min_lines: 400, level: 'L3', model: 'claude-opus-5-5', effort: 'high' },
          { min_lines: 800, model: 'claude-fable-5-1' },
        ],
      }),
    );
    assert.deepEqual([r.valid, r.errors, r.warnings], [true, [], []]);
  });

  test('a duplicate level + min_lines (the default level and an explicit L2) ⇒ exactly one review-routes-invalid error naming both key paths', () => {
    const r = validateConfig(cfgWith({ routes: [{ min_lines: 400, model: BIG, provider: 'xai' }, { min_lines: 800, model: BIG, provider: 'xai' }, { min_lines: 400, level: 'L2', model: 'claude-sonnet-5' }] }));
    const issues = issuesOf(r, 'review-routes-invalid');
    assert.equal(r.valid, false);
    assert.deepEqual(issues.map((i) => [i.severity, i.message]), [['error', 'review.routes[2] has the same level and min_lines as review.routes[0]']]);
    assert.equal(r.errors.length, 1);
  });

  test('the same min_lines on two levels is not a duplicate', () => {
    const r = validateConfig(cfgWith({ routes: [{ min_lines: 400, model: BIG, provider: 'xai' }, { min_lines: 400, level: 'L3', model: 'claude-opus-5-5' }] }));
    assert.deepEqual(issuesOf(r, 'review-routes-invalid'), []);
    assert.equal(r.valid, true);
  });

  test('multimodel on: an L2 route whose provider (its own, else the top-level one) equals the second reviewer\'s ⇒ one error per rule; the message names key paths, never a value', () => {
    const r = validateConfig(
      cfgWith({
        multimodel: true,
        second_provider: 'xai',
        routes: [
          { min_lines: 400, model: BIG, provider: 'xai' },
          { min_lines: 800, model: 'claude-fable-5-1' }, // anthropic: fine
          { min_lines: 400, level: 'L3', model: BIG, provider: 'xai' }, // the judge: not this rule's
        ],
      }),
    );
    const issues = issuesOf(r, 'review-routes-invalid');
    assert.equal(issues.length, 1);
    assert.equal(issues[0].severity, 'error');
    assert.match(issues[0].message, /^review\.routes\[0\]\.provider \(else provider\) equals the second reviewer's effective L2 provider/);
    for (const value of ['xai', BIG]) assert.equal(countOccurrences(issues[0].message, value), 0, value);
  });

  test('multimodel on: an L2 route with no provider is checked against the top-level provider (second_levels.L2 wins over second_provider)', () => {
    const r = validateConfig(cfgWith({ multimodel: true, second_provider: 'xai', second_levels: { L2: { provider: 'anthropic', model: 'claude-sonnet-5' } }, routes: [{ min_lines: 400, model: 'claude-fable-5-1' }] }));
    assert.deepEqual(issuesOf(r, 'review-routes-invalid').map((i) => [i.severity, i.message]), [
      ['error', "review.routes[0].provider (else provider) equals the second reviewer's effective L2 provider (review.second_levels.L2.provider, else review.second_provider) — multimodel needs two different providers"],
    ]);
  });

  test('multimodel off: the same provider as second_provider is allowed', () => {
    const r = validateConfig(cfgWith({ second_provider: 'xai', routes: [{ min_lines: 400, model: BIG, provider: 'xai' }] }));
    assert.deepEqual(issuesOf(r, 'review-routes-invalid'), []);
  });

  test('a level other than L2 or L3 ⇒ the schema refuses it and rule 18 names the key path', () => {
    const r = validateConfig(cfgWith({ routes: [{ min_lines: 400, level: 'L1', model: BIG, provider: 'xai' }] }));
    assert.equal(r.valid, false);
    assert.deepEqual(issuesOf(r, 'review-routes-invalid').map((i) => i.message), ['review.routes[0].level must be L2 or L3']);
    assert.equal(issuesOf(r, 'schema').length, 1);
  });

  test('the schema: min_lines and model are required, min_lines is a non-negative integer and session_timeout_s a positive one, no unknown key — exactly one schema error each, by keyword and path', () => {
    const M = 'claude-fable-5-1'; // valid for the top-level anthropic provider: no unrelated issue
    /** @type {Array<[Record<string, any>, Record<string, any>, string, string]>} [bad rule, its fixed twin, keyword, path] */
    const cases = [
      [{ model: M }, { min_lines: 400, model: M }, 'required', '/review/routes/0'],
      [{ min_lines: 400 }, { min_lines: 400, model: M }, 'required', '/review/routes/0'],
      [{ min_lines: -1, model: M }, { min_lines: 0, model: M }, 'minimum', '/review/routes/0/min_lines'],
      [{ min_lines: 1.5, model: M }, { min_lines: 2, model: M }, 'type', '/review/routes/0/min_lines'],
      [{ min_lines: 400, model: M, session_timeout_s: 0 }, { min_lines: 400, model: M, session_timeout_s: 1 }, 'minimum', '/review/routes/0/session_timeout_s'],
      [{ min_lines: 400, model: M, max_lines: 9 }, { min_lines: 400, model: M }, 'additionalProperties', '/review/routes/0'],
      [{ min_lines: 400, model: M, second: { provider: 'anthropic' } }, { min_lines: 400, model: M, second: { provider: 'anthropic', model: 'claude-sonnet-5' } }, 'required', '/review/routes/0/second'],
    ];
    for (const [bad, fixed, keyword, at] of cases) {
      const r = validateConfig(cfgWith({ routes: [bad] }));
      assert.deepEqual(r.errors.map((i) => [i.rule, i.keyword, i.path]), [['schema', keyword, at]], JSON.stringify(bad));
      assert.deepEqual(r.warnings, [], JSON.stringify(bad));
      const ok = validateConfig(cfgWith({ routes: [fixed] }));
      assert.deepEqual([ok.valid, ok.errors, ok.warnings], [true, [], []], JSON.stringify(fixed));
    }
  });

  test('multimodel on: the rule\'s own second provider counts — equal to the rule\'s provider ⇒ exactly one error naming review.routes[0].second.provider', () => {
    const r = validateConfig(
      cfgWith({ multimodel: true, second_provider: 'xai', routes: [{ min_lines: 400, model: 'claude-fable-5-1', second: { provider: 'anthropic', model: 'claude-sonnet-5' } }] }),
    );
    assert.deepEqual(
      issuesOf(r, 'review-routes-invalid').map((i) => [i.severity, i.message]),
      [['error', 'review.routes[0].provider (else provider) equals review.routes[0].second.provider — multimodel needs two different providers']],
    );
    // (the judge-family rule also fires on this all-anthropic base: not this rule's)
    assert.deepEqual(r.errors.map((i) => i.rule), ['judge-family-collision', 'review-routes-invalid']);
    // the same rule with a second on another provider: no route issue
    const ok = validateConfig(cfgWith({ multimodel: true, second_provider: 'xai', routes: [{ min_lines: 400, model: 'claude-fable-5-1', second: { provider: 'xai', model: BIG } }] }));
    assert.deepEqual(issuesOf(ok, 'review-routes-invalid'), []);
  });

  test('multimodel on: a rule\'s second on another provider does NOT clear a clash with the consensus slot B (a routed ticket keeps review.second_levels.L2) ⇒ one error', () => {
    const r = validateConfig(
      cfgWith({ multimodel: true, second_provider: 'xai', routes: [{ min_lines: 400, model: BIG, provider: 'xai', second: { provider: 'anthropic', model: 'claude-sonnet-5' } }] }),
    );
    assert.deepEqual(issuesOf(r, 'review-routes-invalid').map((i) => i.message.split(' — ')[0]), [
      "review.routes[0].provider (else provider) equals the second reviewer's effective L2 provider (review.second_levels.L2.provider, else review.second_provider)",
    ]);
  });

  test('rule 16 on a route\'s second: only with multimodel on, and on the effective second provider (its own, else second_levels.L2, else second_provider)', () => {
    const second = (/** @type {Record<string, any>} */ review) =>
      issuesOf(validateConfig(cfgWith(review)), 'closed-book-on-openai').map((i) => [i.severity, i.message.split(' ')[0]]).filter(([, k]) => k.startsWith('review.routes'));
    const codexSecond = { min_lines: 400, model: 'claude-fable-5-1', second: { provider: 'openai', model: 'gpt-6-sol' } };
    // multimodel off: that second never runs as a consensus reviewer ⇒ no warning
    assert.deepEqual(second({ second_provider: 'xai', routes: [codexSecond] }), []);
    assert.deepEqual(second({ multimodel: true, second_provider: 'xai', routes: [codexSecond] }), [['warning', 'review.routes[0].second']]);
    // a second with no provider takes review.second_levels.L2.provider (openai here), not the top-level one
    const bare = { min_lines: 400, model: 'claude-fable-5-1', second: { model: 'gpt-6-sol' } };
    assert.deepEqual(second({ multimodel: true, second_provider: 'xai', second_levels: { L2: { provider: 'openai', model: 'gpt-6-sol' } }, routes: [bare] }), [['warning', 'review.routes[0].second']]);
    assert.deepEqual(second({ multimodel: true, second_provider: 'xai', routes: [{ ...bare, second: { model: BIG } }] }), []);
  });

  test('rule 16: a Codex route model and its Codex fallback ⇒ one closed-book-on-openai warning each, by key path; a Codex second with multimodel off ⇒ none', () => {
    const r = validateConfig(
      cfgWith({
        routes: [
          { min_lines: 400, model: 'gpt-6-sol', provider: 'openai', fallback: [{ provider: 'anthropic', model: 'claude-sonnet-5' }, { provider: 'openai', model: 'gpt-6-sol' }] },
          { min_lines: 800, model: 'claude-fable-5-1', second: { provider: 'openai', model: 'gpt-6-sol' } },
        ],
      }),
    );
    assert.deepEqual(
      issuesOf(r, 'closed-book-on-openai').map((i) => [i.severity, i.message.split(' ')[0]]),
      [
        ['warning', 'review.routes[0]'],
        ['warning', 'review.routes[0].fallback[1]'],
      ],
    );
    assert.deepEqual(r.errors, []);
  });

  test('rule 16: a route with no provider under a top-level openai provider warns too; an anthropic route does not', () => {
    const cfg = cfgWith({ routes: [{ min_lines: 400, model: 'gpt-6-sol' }, { min_lines: 800, model: 'claude-fable-5-1', provider: 'anthropic' }] });
    const routes = (r) => issuesOf(r, 'closed-book-on-openai').map((i) => i.message.split(' ')[0]).filter((k) => k.startsWith('review.routes'));
    assert.deepEqual(routes(validateConfig(cfg)), []);
    cfg.provider = 'openai';
    assert.deepEqual(routes(validateConfig(cfg)), ['review.routes[0]']);
  });
});

describe('B90: a rule with no provider is validated with the provider the worker spawns it on', () => {
  // levels.L2.provider (xai) differs from the top-level provider (anthropic); the rule REPLACES
  // levels.L2, so at run time it runs on the top-level provider — validation must use the same one
  const base = () => {
    const cfg = cfgWith({ multimodel: true, second_provider: 'anthropic', second_levels: { L2: { model: 'claude-sonnet-5' } }, routes: [{ min_lines: 400, model: 'claude-fable-5-1' }] });
    cfg.levels.L2 = { provider: 'xai', model: BIG };
    return cfg;
  };

  test('the worker routes the rule to the top-level provider, and ruleProvider says the same', () => {
    const cfg = base();
    assert.equal(ruleProvider(cfg, cfg.review.routes[0]), 'anthropic');
    const routed = routeTicket(cfg, { lines: 450, timeoutS: 300 });
    assert.deepEqual(routed.rows.map((r) => [r.level, r.provider, r.model]), [['L2', 'anthropic', 'claude-fable-5-1']]);
  });

  test('rule 18 compares that provider (anthropic) with slot B: equal ⇒ exactly the slot-B error; slot B on xai ⇒ no rule-18 issue', () => {
    const same = validateConfig(base());
    assert.deepEqual(issuesOf(same, 'review-routes-invalid').map((i) => i.message), [
      "review.routes[0].provider (else provider) equals the second reviewer's effective L2 provider (review.second_levels.L2.provider, else review.second_provider) — multimodel needs two different providers",
    ]);
    const cfg = base();
    cfg.review.second_levels.L2 = { provider: 'xai', model: BIG };
    cfg.levels.L2 = { provider: 'anthropic', model: 'claude-opus-5-5' };
    cfg.provider = 'openai';
    cfg.review.routes[0] = { min_lines: 400, model: 'gpt-6-sol' }; // spawns on openai ≠ xai
    assert.deepEqual(issuesOf(validateConfig(cfg), 'review-routes-invalid'), []);
  });

  test('rule 16 warns for the provider the rule spawns on: top-level openai with levels.L2 on anthropic ⇒ the route warns; top-level anthropic with levels.L2 on openai ⇒ it does not', () => {
    const routes = (r) => issuesOf(r, 'closed-book-on-openai').map((i) => i.message.split(' ')[0]).filter((k) => k.startsWith('review.routes'));
    const a = cfgWith({ routes: [{ min_lines: 400, model: 'gpt-6-sol' }] });
    a.provider = 'openai';
    a.levels = { L0: { provider: 'anthropic', model: 'claude-haiku-4-5-20251001' }, L1: { provider: 'anthropic', model: 'claude-sonnet-5' }, L2: { provider: 'anthropic', model: 'claude-opus-5-5' }, L3: { provider: 'anthropic', model: 'claude-fable-5-1' } };
    assert.deepEqual(routes(validateConfig(a)), ['review.routes[0]']);
    const b = cfgWith({ routes: [{ min_lines: 400, model: 'claude-fable-5-1' }] });
    b.levels.L2 = { provider: 'openai', model: 'gpt-6-sol' };
    assert.deepEqual(routes(validateConfig(b)), []);
  });
});

describe('B90 (7): run reload accepts a review.routes change', () => {
  test('a run started without routes reloads to one with a route: exit 0, the changed key is review, the new snapshot holds the routes', async () => {
    await withFixture(async ({ ws }) => {
      const out = captureStream();
      const err = captureStream();
      assert.equal(await runRun(['start', '--cwd', ws, '--run', 'r-routes'], { stdout: out, stderr: err }), 0, err.text);
      const routes = [{ min_lines: 400, model: BIG, provider: 'xai', session_timeout_s: 900 }];
      const yaml = `${CONFIG_YAML}review:\n  routes:\n    - {min_lines: 400, model: ${BIG}, provider: xai, session_timeout_s: 900}\n`;
      await writeFile(path.join(ws, '.code-forge.yml'), yaml);
      const stdout = captureStream();
      const stderr = captureStream();
      const code = await runRun(['reload', '--run', 'r-routes'], { stdout, stderr });
      assert.equal(code, 0, stderr.text);
      assert.equal(stderr.text, '');
      assert.equal(stdout.text, 'run r-routes: config reloaded · 1 key changed\n  review\nqueued review tickets (0) keep the config they were enqueued with; new tickets use the new config\n');
      const record = await readRun('r-routes');
      assert.deepEqual(snapshotFor(record, 'any-new-ticket')?.config?.review?.routes, routes);
      const rows = await readAllRows('two-blocks');
      assert.deepEqual(rows.map((r) => r.event), ['run.start', 'run.reload']);
      assert.deepEqual(rows[1].changed_keys, ['review']);
    });
  });

  test('a reload to an invalid routes list (a duplicate) is refused: exit 1, the rule id only, record unchanged', async () => {
    await withFixture(async ({ ws }) => {
      assert.equal(await runRun(['start', '--cwd', ws, '--run', 'r-routes-bad'], { stdout: captureStream(), stderr: captureStream() }), 0);
      const before = await readRun('r-routes-bad');
      const yaml = `${CONFIG_YAML}review:\n  routes:\n    - {min_lines: 400, model: ${BIG}, provider: xai}\n    - {min_lines: 400, model: claude-sonnet-5}\n`;
      await writeFile(path.join(ws, '.code-forge.yml'), yaml);
      const stderr = captureStream();
      const code = await runRun(['reload', '--run', 'r-routes-bad'], { stdout: captureStream(), stderr });
      assert.equal(code, 1);
      assert.equal(stderr.text, 'run reload: .code-forge.yml is invalid (1 error) — nothing changed; run code-forge validate\n  [review-routes-invalid]\n');
      assert.deepEqual(await readRun('r-routes-bad'), before);
    });
  });
});
