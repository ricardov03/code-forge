# v0.27.0 documentation map

The 55 release entries appear below in changelog order, with their guide and code.
The lane report records which pages B1015 corrected. Each page is reviewed
separately; this map does not carry the other pages' diffs.

B983 supersedes B922's original first-option rule: always name `--fastest`.
The init-defaults entry has no block id in the released changelog. B891 changes
maintainer test helpers, with no new user timeout or option. The
[maintainer test runner section](native-engine.md#maintainer-test-runner) explains
its prompt and worker-launch waits.

| Entry id | Docs page | Code evidence |
|---|---|---|
| B952 | [native-engine.md](native-engine.md) | [main.go](../native/cmd/cf-gotest/main.go) |
| B953 | [native-engine.md](native-engine.md) | [main.go](../native/cmd/cf-gotest/main.go) |
| B807 | [native-engine.md](native-engine.md) | [main.go](../native/cmd/cf-gotest/main.go) |
| B989 | [releasing.md](releasing.md) | [init_b989_test.go](../native/cmd/code-forge-native/init_b989_test.go) |
| B940 | [native-engine.md](native-engine.md) | [ramtmp.go](../native/ramtmp/ramtmp.go) |
| B898 | [native-engine.md](native-engine.md) | [stateignore.go](../native/gitx/stateignore.go) |
| B992 | [how-it-works.md](how-it-works.md) | [review_enqueue_flags.go](../native/cmd/code-forge-native/review_enqueue_flags.go) |
| B991 | [review-only.md](review-only.md) | [review_requeue.go](../native/cmd/code-forge-native/review_requeue.go) |
| B890 | [how-it-works.md](how-it-works.md) | [table.go](../native/landmerge/table.go) |
| B813 | [releasing.md](releasing.md) | [fold.go](../native/landmerge/fold.go) |
| B936 | [how-it-works.md](how-it-works.md) | [b936_train_cases_test.go](../native/landmerge/b936_train_cases_test.go) |
| B938 | [how-it-works.md](how-it-works.md) | [carried.go](../native/rebase/carried.go) |
| B814 | [how-it-works.md](how-it-works.md) | [block_land_execute.go](../native/cmd/code-forge-native/block_land_execute.go) |
| B815 | [how-it-works.md](how-it-works.md) | [b815_repo_generated_test.go](../native/landmerge/b815_repo_generated_test.go) |
| B910 | [how-it-works.md](how-it-works.md) | [block_land_batch_ci.go](../native/cmd/code-forge-native/block_land_batch_ci.go) |
| B803 | [how-it-works.md](how-it-works.md) | [block_land_batch_ci.go](../native/cmd/code-forge-native/block_land_batch_ci.go) |
| B804 | [how-it-works.md](how-it-works.md) | [block_land_batch_ci.go](../native/cmd/code-forge-native/block_land_batch_ci.go) |
| B805 | [how-it-works.md](how-it-works.md) | [land_ci_branch.go](../native/cmd/code-forge-native/land_ci_branch.go) |
| B806 | [how-it-works.md](how-it-works.md) | [land_ci_retry.go](../native/cmd/code-forge-native/land_ci_retry.go) |
| B920 | [how-it-works.md](how-it-works.md) | [block_open_base_ci.go](../native/cmd/code-forge-native/block_open_base_ci.go) |
| B923 | [how-it-works.md](how-it-works.md) | [testbudget.go](../native/contextpack/testbudget.go) |
| B942 | [how-it-works.md](how-it-works.md) | [newest.go](../native/worktree/newest.go) |
| B921 | [autopilot.md](autopilot.md) | [autopilot_found.go](../native/cmd/code-forge-native/autopilot_found.go) |
| B984 | [autopilot.md](autopilot.md) | [autopilot_found.go](../native/cmd/code-forge-native/autopilot_found.go) |
| B922 | [autopilot.md](autopilot.md) | [autopilot_speed.go](../native/cmd/code-forge-native/autopilot_speed.go) |
| B983 | [autopilot.md](autopilot.md) | [autopilot_speed.go](../native/cmd/code-forge-native/autopilot_speed.go) |
| B969 | [review-only.md](review-only.md) | [review_file_wait.go](../native/cmd/code-forge-native/review_file_wait.go) |
| B997 | [review-only.md](review-only.md) | [review_flow.go](../native/cmd/code-forge-native/review_flow.go) |
| B970 | [review-only.md](review-only.md) | [review_flow.go](../native/cmd/code-forge-native/review_flow.go) |
| B971 | [review-only.md](review-only.md) | [review_wait_block.go](../native/cmd/code-forge-native/review_wait_block.go) |
| B956 | [how-it-works.md](how-it-works.md) | [land_ci_gate.go](../native/cmd/code-forge-native/land_ci_gate.go) |
| B979 | [how-it-works.md](how-it-works.md) | [block_land_execute.go](../native/cmd/code-forge-native/block_land_execute.go) |
| B980 | [how-it-works.md](how-it-works.md) | [block_land_batch_ci.go](../native/cmd/code-forge-native/block_land_batch_ci.go) |
| B981 | [how-it-works.md](how-it-works.md) | [land_ci_retry.go](../native/cmd/code-forge-native/land_ci_retry.go) |
| B982 | [how-it-works.md](how-it-works.md) | [spawn_coder.go](../native/cmd/code-forge-native/spawn_coder.go) |
| Init defaults (no block id) | [getting-started.md](getting-started.md) | [init_setup.go](../native/cmd/code-forge-native/init_setup.go) |
| B959 | [releasing.md](releasing.md) | [regendocs.go](../native/cmd/cf-release/regendocs.go) |
| B990 | [releasing.md](releasing.md) | [driver.go](../native/landmerge/driver.go) |
| B924 | [native-engine.md](native-engine.md) | [ramtmp.go](../native/ramtmp/ramtmp.go) |
| B939 | [getting-started.md](getting-started.md) | [init_setup.go](../native/cmd/code-forge-native/init_setup.go) |
| B941 | [getting-started.md](getting-started.md) | [ramtmp.go](../native/ramtmp/ramtmp.go) |
| B891 | [native-engine.md](native-engine.md) | [b703_pty_test.go](../native/cmd/code-forge-native/b703_pty_test.go) |
| B893 | [native-engine.md](native-engine.md) | [flaky_go.go](../native/gates/flaky_go.go) |
| B894 | [native-engine.md](native-engine.md) | [flaky_go.go](../native/gates/flaky_go.go) |
| B988 | [native-engine.md](native-engine.md) | [flaky_go.go](../native/gates/flaky_go.go) |
| B973 | [native-engine.md](native-engine.md) | [flaky_go.go](../native/gates/flaky_go.go) |
| B987 | [native-engine.md](native-engine.md) | [flaky_go.go](../native/gates/flaky_go.go) |
| B950 | [autopilot.md](autopilot.md) | [stepbudget.go](../native/stepbudget/stepbudget.go) |
| B951 | [releasing.md](releasing.md) | [size.go](../native/releasecheck/size.go) |
| B821 | [autopilot.md](autopilot.md) | [autopilot_found_auto_b821.go](../native/cmd/code-forge-native/autopilot_found_auto_b821.go) |
| B822 | [autopilot.md](autopilot.md) | [autopilot_found.go](../native/cmd/code-forge-native/autopilot_found.go) |
| B962 | [how-it-works.md](how-it-works.md) | [land_ci_main_b962.go](../native/cmd/code-forge-native/land_ci_main_b962.go) |
| B957 | [native-engine.md](native-engine.md) | [main.go](../native/cmd/cf-redgreen/main.go) |
| B958 | [native-engine.md](native-engine.md) | [gates_flaky_b798.go](../native/cmd/code-forge-native/gates_flaky_b798.go) |
| B964 | [native-engine.md](native-engine.md) | [land_fast_b964.go](../native/cmd/code-forge-native/land_fast_b964.go) |
