package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B483: the keys row's refresh cases. Expected bytes and files are the Node
// CLI's on the same fixtures (CODE_FORGE_NATIVE=0, temp HOME, file backend,
// a fake op on PATH). now is pinned at 1_000_000 ms, so the 8 h cache ends at
// 29_800_000 (1970-01-01T08:16:40.000Z).

const (
	b483Now     = 1_000_000
	b483Expires = "1970-01-01T08:16:40.000Z"
	b483OpCfg   = "provider: anthropic\nkeys: {jev: 'op://vault/item/field'}\n" + b474Levels
)

func b483Rows(key string) string {
	return "OK   config: valid\n" + b474NoLinks + key + "\n"
}

// b483FakeOp puts an op script on a PATH of its own; body runs after the
// call counter in $DIR/count is bumped (n holds the call number).
func b483FakeOp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nn=$(/bin/cat " + dir + "/count 2>/dev/null || echo 0); n=$((n+1)); echo $n > " + dir + "/count\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "op"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	return dir
}

func b483File(t *testing.T, home, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", rel))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(data)
}

func b483Mode(t *testing.T, home, rel string) os.FileMode {
	t.Helper()
	info, err := os.Stat(filepath.Join(home, ".code-forge", rel))
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// An expired cached entry is deleted with its index row; with no op://
// reference the key is not resolved.
func TestB483DoctorExpiredEntryIsDropped(t *testing.T) {
	home, _ := b474Project(t, "provider: anthropic\n"+b474Levels, b483Now)
	writeHomeFile(t, home, ".code-forge/store/jev.key", `{"v":"k1","exp":1000}`)
	writeHomeFile(t, home, ".code-forge/store/index.json", `{"jev":{"source":"op","backend":"file","exp":1000},"other":{"source":"user","exp":null}}`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey, b474Summary(1, 2, 0)}})
	if got := b483File(t, home, "store/jev.key"); !strings.Contains(got, "no such file") {
		t.Fatalf("expired key file kept: %q", got)
	}
	if got := b483File(t, home, "store/index.json"); got != "{\n  \"other\": {\n    \"source\": \"user\",\n    \"exp\": null\n  }\n}" {
		t.Fatalf("index = %q", got)
	}

	// no index yet: Node still writes an empty one
	home, _ = b474Project(t, "provider: anthropic\n"+b474Levels, b483Now)
	writeHomeFile(t, home, ".code-forge/store/jev.key", `{"v":"k1","exp":1000}`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey, b474Summary(1, 2, 0)}})
	if got := b483File(t, home, "store/index.json"); got != "{}" || b483Mode(t, home, "store/index.json") != 0o600 {
		t.Fatalf("index = %q", got)
	}
}

// An entry trusted for longer than the cache hours is cut to now + hours:
// the entry and an existing index row are rewritten, the value is kept.
func TestB483DoctorTooLongEntryIsCut(t *testing.T) {
	home, _ := b474Project(t, "provider: anthropic\nkeys: {cache_hours: 3}\n"+b474Levels, b483Now)
	writeHomeFile(t, home, ".code-forge/store/jev.key", `{"v":"long-value","exp":99999999999999}`+"\n")
	writeHomeFile(t, home, ".code-forge/store/index.json", `{"jev":{"source":"op","exp":99999999999999,"backend":"keychain","ref":"op://a/b/c"},"2":{}}`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0,
		b483Rows("OK   keys: jev key from file, expires 1970-01-01T03:16:40.000Z (cache 3 h)"), b474Summary(2, 1, 0)}})
	if got := b483File(t, home, "store/jev.key"); got != `{"v":"long-value","exp":11800000}` || b483Mode(t, home, "store/jev.key") != 0o600 {
		t.Fatalf("key file = %q", got)
	}
	want := "{\n  \"2\": {},\n  \"jev\": {\n    \"source\": \"op\",\n    \"exp\": 11800000,\n    \"backend\": \"file\",\n    \"ref\": \"op://a/b/c\"\n  }\n}"
	if got := b483File(t, home, "store/index.json"); got != want {
		t.Fatalf("index = %q", got)
	}

	// no index row: none is invented, and no index file is written
	home, _ = b474Project(t, "provider: anthropic\n"+b474Levels, b483Now)
	writeHomeFile(t, home, ".code-forge/store/jev.key", `{"v":"long-value","exp":99999999999999}`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, b483Rows("OK   keys: jev key from file, expires " + b483Expires), b474Summary(2, 1, 0)}})
	if got := b483File(t, home, "store/jev.key"); got != `{"v":"long-value","exp":29800000}` {
		t.Fatalf("key file = %q", got)
	}
	if got := b483File(t, home, "store/index.json"); got != "{}" {
		t.Fatalf("index = %q", got)
	}
}

// An op:// reference with nothing cached is read with `op read` and cached
// with its index row; an expired entry is dropped first.
func TestB483DoctorReadsOpAndCaches(t *testing.T) {
	home, _ := b474Project(t, b483OpCfg, b483Now)
	fake := b483FakeOp(t, `[ "$1 $2" = "read op://vault/item/field" ] || exit 9; printf 'sk-fake-value\n'`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, b483Rows("OK   keys: jev key from op, expires " + b483Expires), b474Summary(2, 1, 0)}})
	if got := b483File(t, home, "store/jev.key"); got != `{"v":"sk-fake-value","exp":29800000}` || b483Mode(t, home, "store/jev.key") != 0o600 {
		t.Fatalf("key file = %q", got)
	}
	wantIndex := "{\n  \"jev\": {\n    \"source\": \"op\",\n    \"backend\": \"file\",\n    \"exp\": 29800000,\n    \"ref\": \"op://vault/item/field\"\n  }\n}"
	if got := b483File(t, home, "store/index.json"); got != wantIndex {
		t.Fatalf("index = %q", got)
	}
	if count, _ := os.ReadFile(filepath.Join(fake, "count")); string(count) != "1\n" {
		t.Fatalf("op calls = %q", count)
	}

	home, _ = b474Project(t, b483OpCfg, b483Now)
	b483FakeOp(t, `printf 'fresh\r\n'`)
	writeHomeFile(t, home, ".code-forge/store/jev.key", `{"v":"old-value","exp":1000}`)
	writeHomeFile(t, home, ".code-forge/store/index.json", `{"other":{"source":"user"},"jev":{"source":"op","exp":1000}}`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, b483Rows("OK   keys: jev key from op, expires " + b483Expires), b474Summary(2, 1, 0)}})
	if got := b483File(t, home, "store/jev.key"); got != `{"v":"fresh","exp":29800000}` {
		t.Fatalf("key file = %q", got)
	}
	if got := b483File(t, home, "store/index.json"); !strings.HasPrefix(got, "{\n  \"other\": {\n    \"source\": \"user\"\n  },\n  \"jev\": {\n    \"source\": \"op\",") {
		t.Fatalf("index = %q", got)
	}

	// the value read is redacted from the output, as Node registers it
	b474Project(t, b483OpCfg, b483Now)
	b483FakeOp(t, `printf 'x\n'`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick", "--json"}, 0,
		`{"ok":true,"counts":{"OK":2,"WARN":1,"FAIL":0,"INFO":0},"rows":[{"id":"config","status":"OK","label":"config","detail":"valid"},{"id":"links","status":"WARN","label":"links","detail":"no harness link recorded (run code-forge init)"},{"id":"keys","status":"OK","label":"keys","detail":"jev key from op, e[REDACTED]pires 1970-01-01T08:16:40.000Z"}]}` + "\n",
		b474Summary(2, 1, 0)}})
}

// A failed op read leaves the key unresolved with its fixed reason.
func TestB483DoctorOpFailures(t *testing.T) {
	path := os.Getenv("PATH")
	for _, c := range []struct{ body, reason string }{
		{`echo "[ERROR] \"item\" isn't an item in vault" >&2; exit 1`, "1Password item or field not found, or no access"},
		{`echo "account is not signed in" >&2; exit 1`, "1Password is locked or not signed in; unlock the app (or run `op signin`) and try again"},
		{`exit 3`, "1Password CLI failed"},
		{`printf '\n'`, "unexpected answer from op"},
		{`exit 127`, "1Password CLI not found; install it with: brew install 1password-cli"},
	} {
		home, _ := b474Project(t, b483OpCfg, b483Now)
		b483FakeOp(t, c.body)
		checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0,
			b483Rows("WARN keys: jev key not resolved (" + c.reason + "); System 1 falls back to rules"), b474Summary(1, 2, 0)}})
		if got := b483File(t, home, "store/index.json"); !strings.Contains(got, "no such file") {
			t.Fatalf("%s: index written: %q", c.reason, got)
		}
	}

	// no op on PATH; an expired entry is still dropped and a read error adds its reason
	home, _ := b474Project(t, b483OpCfg, b483Now)
	t.Setenv("PATH", t.TempDir())
	writeHomeFile(t, home, ".code-forge/store/jev.key", `{"v":"k1","exp":1000}`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0,
		b483Rows("WARN keys: jev key not resolved (1Password CLI not found; install it with: brew install 1password-cli); System 1 falls back to rules"), b474Summary(1, 2, 0)}})
	if got := b483File(t, home, "store/index.json"); got != "{}" {
		t.Fatalf("index = %q", got)
	}
	t.Setenv("PATH", path)
	home, _ = b474Project(t, b483OpCfg, b483Now)
	b483FakeOp(t, `exit 3`)
	if err := os.MkdirAll(filepath.Join(home, ".code-forge", "store", "jev.key"), 0o700); err != nil {
		t.Fatal(err)
	}
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0,
		b483Rows("WARN keys: jev key not resolved (key store error; 1Password CLI failed); System 1 falls back to rules"), b474Summary(1, 2, 0)}})
}

// A first call that times out is retried once and logged as an op_retry
// warning; two timeouts are op_timeout. B599: the outcome must not depend on
// machine load. The fake op hangs unless an answer file exists; the test's
// attempt hook writes that file only when attempt 2 starts and gives attempt 2
// a long budget, so a slow process start can neither turn call 1 into the
// answering call nor make call 2 time out. A hanging call always times out,
// however busy the machine is.
func TestB483DoctorOpTimeoutRetriesOnce(t *testing.T) {
	saved := opAttemptTimeout
	t.Cleanup(func() { opAttemptTimeout = saved })
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	const hangUnlessAnswered = `if [ -e "${0%/op}/answer" ]; then printf 'second\n'; else exec /bin/sleep 60; fi`

	home, _ := b474Project(t, b483OpCfg, b483Now)
	fake := b483FakeOp(t, hangUnlessAnswered)
	var attempts []int
	opAttemptTimeout = func(attempt int) time.Duration {
		attempts = append(attempts, attempt)
		if attempt == 2 {
			if err := os.WriteFile(filepath.Join(fake, "answer"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			return time.Minute
		}
		return time.Second
	}
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, b483Rows("OK   keys: jev key from op, expires " + b483Expires), b474Summary(2, 1, 0)}})
	if fmt.Sprint(attempts) != "[1 2]" {
		t.Fatalf("op attempts = %v", attempts)
	}
	log := b483File(t, home, "logs/errors.jsonl")
	if !strings.Contains(log, `"verb":"doctor","sub":null,"flags":[],"exit":0,"kind":"warning","warning":"op_retry","message":"1Password did not answer in time; the call was retried once","stack":null,"cleaned":[],"fp":"3d3635502fa2"`) || strings.Count(log, "\n") != 1 {
		t.Fatalf("errors.jsonl = %q", log)
	}

	b474Project(t, b483OpCfg, b483Now)
	b483FakeOp(t, hangUnlessAnswered)
	attempts = nil
	opAttemptTimeout = func(attempt int) time.Duration {
		attempts = append(attempts, attempt)
		return time.Second
	}
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0,
		b483Rows("WARN keys: jev key not resolved (1Password did not answer in time; unlock the app and try again); System 1 falls back to rules"), b474Summary(1, 2, 0)}})
	if fmt.Sprint(attempts) != "[1 2]" {
		t.Fatalf("op attempts = %v", attempts)
	}
}

// An index Go cannot read back as JSON.parse does keeps the hand-over.
func TestB483DoctorKeyIndexAmbiguityHandsOver(t *testing.T) {
	home, _ := b474Project(t, b483OpCfg, b483Now)
	writeHomeFile(t, home, ".code-forge/store/index.json", `{"x":"\ud800"}`)
	expectHandover(t, "doctor-key-refresh", "doctor", "--quick")
	home, _ = b474Project(t, "provider: anthropic\n"+b474Levels, b483Now)
	writeHomeFile(t, home, ".code-forge/store/jev.key", `{"v":"k1","exp":999000000}`)
	writeHomeFile(t, home, ".code-forge/store/index.json", `{"jev":"row"}`)
	expectHandover(t, "doctor-key-refresh", "doctor", "--quick")
}
