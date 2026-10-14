#!/bin/sh
export CODE_FORGE_TEST_SUITE=${CODE_FORGE_TEST_SUITE:-$(node -e 'process.stdout.write(require("node:crypto").randomUUID())')}
CODE_FORGE_NO_LEAK=snapshot node --import ./test/helpers/isolate.mjs --test test/no-leak.test.mjs && {
  node --import ./test/helpers/isolate.mjs --experimental-test-module-mocks --test "$@" 'test/**/*.test.mjs'
  cf_test_status=$?
  CODE_FORGE_NO_LEAK=final node --import ./test/helpers/isolate.mjs --test test/no-leak.test.mjs
  cf_leak_status=$?
  [ "$cf_test_status" -eq 0 ] && [ "$cf_leak_status" -eq 0 ]
}
