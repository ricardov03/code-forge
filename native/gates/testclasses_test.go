package gates

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

// The reporter sample (issue #35 P2) lives in git under test/fixtures.
var androidSample = filepath.Join("..", "..", "test", "fixtures", "android-classes")

type androidCase struct {
	rules []ClassRule
	plan  Plan
	block ledger.Object
}

func loadAndroidSample(t *testing.T) androidCase {
	t.Helper()
	loaded, err := config.LoadFile(filepath.Join(androidSample, "code-forge.yml"))
	if err != nil {
		t.Fatal(err)
	}
	configured, _ := loaded["gates"].(map[string]any)
	rules, err := ParseClassMap(configured["test_class_map"])
	if err != nil {
		t.Fatalf("sample test_class_map: %v", err)
	}
	plan, ok := Resolve(t.TempDir(), configured)
	if !ok {
		t.Fatal("sample gates.test did not resolve")
	}
	raw, err := os.ReadFile(filepath.Join(androidSample, "block.json"))
	if err != nil {
		t.Fatal(err)
	}
	block, err := ledger.DecodeObject(raw)
	if err != nil {
		t.Fatal(err)
	}
	return androidCase{rules: rules, plan: plan, block: block}
}

func ownedBlock(files ...string) ledger.Object {
	owned := []any{}
	for _, f := range files {
		owned = append(owned, f)
	}
	return ledger.Object{{Key: "owned_files", Value: owned}}
}

// fakeGradleArgv runs the plan with a fake gradle first on PATH and returns
// the argv (without argv[0]) that the fake received, one item per line.
func fakeGradleArgv(t *testing.T, plan Plan, filter string) []string {
	t.Helper()
	bin, record := t.TempDir(), filepath.Join(t.TempDir(), "argv.txt")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + record + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "gradle"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	expanded, err := WithClasses(plan, filter)
	if err != nil {
		t.Fatalf("WithClasses: %v", err)
	}
	results, all, err := Run(context.Background(), expanded, t.TempDir(), "", 30000, Scope{Kind: "full", Reason: "no_tests"})
	if err != nil || !all {
		t.Fatalf("gates run: all=%v err=%v results=%v", all, err, results)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("fake gradle was not run: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// Acceptance: a block's Kotlin androidTest files map to the exact class filter
// the fake gradle receives.
func TestClassesAndroidSampleReachesFakeGradle(t *testing.T) {
	sample := loadAndroidSample(t)
	filter := BlockClassFilter(sample.rules, sample.block)
	if filter != "a.b.FooTest,a.b.BarTest" {
		t.Fatalf("filter: want %q, got %q", "a.b.FooTest,a.b.BarTest", filter)
	}
	got := fakeGradleArgv(t, sample.plan, filter)
	want := []string{"connectedDebugAndroidTest", "-Pandroid.testInstrumentationRunnerArguments.class=a.b.FooTest,a.b.BarTest"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fake gradle argv: want %d items %q, got %d items %q", len(want), want, len(got), got)
	}
}

// Acceptance: a block with no matching tests runs the full suite — the item
// holding {test_classes} is dropped, nothing else.
func TestClassesNoMatchingTestRunsFullSuite(t *testing.T) {
	sample := loadAndroidSample(t)
	block := ownedBlock("app/src/main/java/a/b/Foo.kt", "docs/android/notes.md")
	filter := BlockClassFilter(sample.rules, block)
	if filter != "" {
		t.Fatalf("filter: want empty, got %q", filter)
	}
	got := fakeGradleArgv(t, sample.plan, filter)
	want := []string{"connectedDebugAndroidTest"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fake gradle argv: want %d items %q, got %d items %q", len(want), want, len(got), got)
	}
}

// Boundary: a path outside every rule (a JVM unit test, a source file, a doc)
// is ignored; only the one androidTest class is kept.
func TestClassesPathOutsideEveryRuleIsIgnored(t *testing.T) {
	sample := loadAndroidSample(t)
	block := ownedBlock("app/src/test/java/a/b/UnitTest.kt", "app/src/androidTest/java/a/b/FooTest.kt", "app/src/main/java/a/b/Foo.kt", "docs/android/notes.md")
	if got := BlockClassFilter(sample.rules, block); got != "a.b.FooTest" {
		t.Fatalf("filter: want %q (1 class), got %q", "a.b.FooTest", got)
	}
}

// Negative: {test_classes} cannot be the command, and a rule needs a glob.
func TestClassesRefusesBadShapes(t *testing.T) {
	plan := Plan{Commands: map[string]*Command{"test": {Argv: []string{"{test_classes}", "x"}}}}
	if _, err := WithClasses(plan, "a.b.FooTest"); err == nil || err.Error() != "gates.test: '{test_classes}' cannot be in the command (argv[0])" {
		t.Fatalf("argv[0] token: got %v", err)
	}
	_, err := ParseClassMap([]any{map[string]any{"strip_suffix": ".kt"}})
	if err == nil || err.Error() != "gates.test_class_map[0].glob must be a non-empty string" {
		t.Fatalf("rule without glob: got %v", err)
	}
}
