#!/bin/sh
# Validate explicit test paths before starting either leak-check step. Resolve
# existing ancestors too, so a symlink cannot lead outside the suite's repo.
# All runner paths below are relative to the caller's working directory, even
# when this wrapper lives in another checkout (for example a fixture suite).
cf_suite_root=$(pwd -P) || exit 2
cf_validate_path() {
  node -e '
    const fs = require("node:fs"), path = require("node:path");
    function canonical(p) {
      try { return fs.realpathSync(p); } catch (err) {
        if (err.code !== "ENOENT" && err.code !== "ENOTDIR") throw err;
        const parent = path.dirname(p);
        if (parent === p) throw err;
        return path.join(canonical(parent), path.basename(p));
      }
    }
    const root = process.argv[1];
    try {
      for (const file of process.argv.slice(2)) {
        if (file.split("/").includes("..")) throw new Error("traversal");
        const relative = path.relative(root, canonical(path.resolve(root, file)));
        if (relative === ".." || relative.startsWith("../") || path.isAbsolute(relative)) throw new Error("outside");
      }
    } catch {
      console.error("run-suite: test paths must stay inside the repo (no ..)");
      process.exit(2);
    }
  ' "$cf_suite_root" "$1" >/dev/null || exit 2
}
cf_has_tests=false
cf_flag_value=false
cf_options=true
for cf_arg do
  if [ "$cf_flag_value" = true ]; then
    cf_flag_value=false
    continue
  fi
  if [ "$cf_options" = true ]; then
    case "$cf_arg" in
      --) cf_options=false; continue ;;
      --test-name-pattern|--test-skip-pattern|--test-reporter|--test-reporter-destination|\
      --test-timeout|--test-concurrency|--test-shard|--test-isolation|--experimental-test-isolation|\
      --test-coverage-branches|--test-coverage-functions|--test-coverage-lines|\
      --test-coverage-include|--test-coverage-exclude|--test-global-setup|\
      --test-rerun-failures|--test-random-seed|--experimental-test-tag-filter|\
      --import|--require|-r|--conditions|-C|--loader|--experimental-loader)
        cf_flag_value=true; continue ;;
      -*) continue ;;
    esac
  fi
  cf_validate_path "$cf_arg"
  # Preserve the B115 bare-wildcard argv contract: it is forwarded verbatim,
  # but does not replace the default suite glob. Other paths select the scope.
  if [ "$cf_arg" != '*' ]; then
    cf_has_tests=true
  fi
done

# Re-enter only after sandbox-exec has installed the profile. The two no-leak
# steps and the test runner must all inherit the same kernel restrictions.
if [ "${1:-}" = --suite-sandbox-active ]; then
  shift
  export CODE_FORGE_LEAK_GUARD=sandbox
  unset CODE_FORGE_LEAK_GUARD_SCOPE
else
  cf_reason=
  if [ "${CODE_FORGE_LEAK_GUARD:-}" = snapshot ]; then
    cf_reason=forced
  elif [ "$(uname -s)" != Darwin ]; then
    cf_reason='not macOS'
  elif [ "${CODEX_SANDBOX+x}" = x ]; then
    cf_reason=CODEX_SANDBOX
  elif ! command -v sandbox-exec >/dev/null 2>&1; then
    cf_reason='sandbox-exec missing'
  else
    cf_profile=${0%/*}/suite-sandbox.sb
    # Resolve existing ancestors as well: code-forge need not exist yet. This
    # also canonicalizes macOS /var aliases without creating guarded paths.
    # Tests may inject a JSON pair of HOME/TMP stand-ins; repo protection stays.
    cf_guard_path() {
      node -e '
        const fs = require("node:fs"), path = require("node:path"), os = require("node:os");
        const dirs = process.env.CODE_FORGE_LEAK_GUARD_DIRS
          ? JSON.parse(process.env.CODE_FORGE_LEAK_GUARD_DIRS)
          : [path.join(process.env.CODE_FORGE_REAL_HOME ?? os.homedir(), ".code-forge"),
             path.join(process.env.CODE_FORGE_REAL_TMPDIR ?? os.tmpdir(), "code-forge")];
        if (!Array.isArray(dirs) || dirs.length !== 2 || dirs.some(p => typeof p !== "string" || !path.isAbsolute(p)))
          throw new Error("CODE_FORGE_LEAK_GUARD_DIRS must be a JSON pair of absolute paths");
        dirs.push(path.resolve(process.argv[1], "../..", ".code-forge"));
        function canonical(p) {
          try { return fs.realpathSync(p); } catch (err) {
            if (err.code !== "ENOENT") throw err;
            const parent = path.dirname(p);
            if (parent === p) throw err;
            return path.join(canonical(parent), path.basename(p));
          }
        }
        process.stdout.write(canonical(path.resolve(dirs[Number(process.argv[2])])));
      ' "${0%/*}" "$1"
    }
    cf_home=$(cf_guard_path 0) || exit 1
    cf_tmp=$(cf_guard_path 1) || exit 1
    cf_repo=$(cf_guard_path 2) || exit 1
    if sandbox-exec -f "$cf_profile" -D "HOME_CF=$cf_home" -D "TMP_CF=$cf_tmp" -D "REPO_CF=$cf_repo" /usr/bin/true >/dev/null 2>&1; then
      printf '%s\n' 'leak guard: sandbox' >&2
      exec sandbox-exec -f "$cf_profile" -D "HOME_CF=$cf_home" -D "TMP_CF=$cf_tmp" -D "REPO_CF=$cf_repo" /bin/sh "$0" --suite-sandbox-active "$@"
    fi
    cf_reason='sandbox-exec probe failed'
  fi
  export CODE_FORGE_LEAK_GUARD=snapshot
  printf 'leak guard: snapshot (%s)\n' "$cf_reason" >&2
fi

export CODE_FORGE_TEST_SUITE=${CODE_FORGE_TEST_SUITE:-$(node -e 'process.stdout.write(require("node:crypto").randomUUID())')}
if [ "$cf_has_tests" = false ]; then
  set -- "$@" 'test/**/*.test.mjs'
fi
CODE_FORGE_NO_LEAK=snapshot node --import ./test/helpers/isolate.mjs --test test/no-leak.test.mjs && {
  node --import ./test/helpers/isolate.mjs --experimental-test-module-mocks --test "$@"
  cf_test_status=$?
  CODE_FORGE_NO_LEAK=final node --import ./test/helpers/isolate.mjs --test test/no-leak.test.mjs
  cf_leak_status=$?
  [ "$cf_test_status" -eq 0 ] && [ "$cf_leak_status" -eq 0 ]
}
