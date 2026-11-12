package main

import (
	"os"
	"strings"
	"testing"
)

// B588: a long scratchpad-style path in an error message. Node's findSecret
// reads a 32+ run of [A-Za-z0-9+/_=-] as one token, so a path such as
// /6bd8f68e-.../scratchpad/b588/x is a "high-entropy string" unless the
// scrubber turned it into ~ or <project>. Expected texts in testdata/b588 are
// Node's (CODE_FORGE_NATIVE=0 ./bin/code-forge logs report --no-ai --dry-run)
// for the lrTestEnv fixture with the third row's message replaced.

const lrB588LongTail = "/6bd8f68e-18be-4180-8409-3cc234c47873/scratchpad/b588/x.mjs"

func lrB588Golden(t *testing.T, c lrTestCase, name string) string {
	t.Helper()
	c.data = strings.TrimSuffix(c.data, "b554") + "b588"
	return c.golden(t, name)
}

// lrB588Refused checks Node's refusal: exit 1, the share notice, the report
// line on stderr, one "logs report" row appended to the error log, no gh call
// and no handover.
func lrB588Refused(t *testing.T, c lrTestCase, what string) {
	t.Helper()
	before := b500Read(t, c.errorLog())
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--dry-run")
	lrTestCheck(t, what, code, out, errText, 1, lrB588Golden(t, c, "refused.stdout"), lrB588Golden(t, c, "refused.stderr"))
	after := b500Read(t, c.errorLog())
	added := strings.TrimPrefix(after, before)
	if added == after || strings.Count(added, "\n") != 1 || !strings.Contains(added, `"verb":"logs","sub":"report"`) || !strings.Contains(added, `"kind":"error"`) {
		t.Errorf("%s: error log rows added: %q", what, added)
	}
	if strings.Contains(c.fakeLog(t), "gh ") {
		t.Errorf("%s: refused report ran gh: %q", what, c.fakeLog(t))
	}
	if h := b500HandedOver(t, c.home); h != "" {
		t.Errorf("%s: handed over: %s", what, h)
	}
}

func TestB588LogsReportLongPathOutsideHomeIsRefused(t *testing.T) {
	c := lrTestEnv(t, "failed in /private/tmp/claude-501/-Users-someone-Code-proj"+lrB588LongTail, false)
	lrB588Refused(t, c, "long path outside home and project")
}

func TestB588LogsReportLongPathUnderProjectIsRefused(t *testing.T) {
	// The project folder becomes <project>; the long tail after it stays.
	c := lrTestEnv(t, "", false)
	lrB588SetMessage(t, c, "failed in "+c.work+lrB588LongTail)
	lrB588Refused(t, c, "long tail under the project")
}

func TestB588LogsReportLongPathFromOutsideProjectIsRefused(t *testing.T) {
	c := lrTestEnv(t, "", false)
	lrB588SetMessage(t, c, "failed in "+c.work+lrB588LongTail)
	if err := os.Chdir(c.tmpRoot); err != nil {
		t.Fatal(err)
	}
	lrB588Refused(t, c, "run outside the project")
}

func TestB588LogsReportShortPathMatchesNode(t *testing.T) {
	c := lrTestEnv(t, "failed in /tmp/x/a.go", false)
	before := b500Read(t, c.errorLog())
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--dry-run")
	lrTestCheck(t, "short path", code, out, errText, 0, lrB588Golden(t, c, "shortpath.stdout"), "")
	if after := b500Read(t, c.errorLog()); after != before {
		t.Errorf("a dry run changed the error log")
	}
	if h := b500HandedOver(t, c.home); h != "" {
		t.Errorf("short path handed over: %s", h)
	}
}

// lrB588SetMessage rewrites the third fixture row's message, for messages
// that hold the test's own project folder.
func lrB588SetMessage(t *testing.T, c lrTestCase, message string) {
	t.Helper()
	text := b500Read(t, c.errorLog())
	old := "failed at " + c.home + "/x and " + c.work + "/src/a.go for bob@example.com in myslug op://vault/item/field abcdefghijklmnopqrstuvwxyz"
	if !strings.Contains(text, old) {
		t.Fatalf("fixture message not found")
	}
	b500Write(t, c.errorLog(), strings.Replace(text, old, message, 1))
}
