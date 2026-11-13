package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/docsync"
	"github.com/ricardov03/code-forge/native/pkgroot"
)

const upgradeUsage = "usage: code-forge upgrade [--source <path>] [--plain]\n"

const upgradeLogo = " ___  __   __   ___       ___  __   __   __   ___\n|    |  | |  \\ |__   __  |__  |  | |__/ | _  |__\n|___ |__| |__/ |___      |    |__| |  \\ |__] |___\n"

func init() {
	routes["upgrade"] = smallVerb{name: "upgrade", usage: upgradeUsage, prepare: prepareUpgrade}.run
}

// Seams for tests: the package root (with Node's checks that it is the
// root Node would use), the clock behind linked_at and the docs sync.
var (
	upgradePackageRoot = upgradeRealPackageRoot
	upgradeNow         = time.Now
	upgradeDocsSync    = upgradeRealDocsSync
)

// upgradeRealDocsSync is the `docs sync` Node's upgrade spawns from this
// same binary, run in process: its one summary line.
func upgradeRealDocsSync(home, root, version string) string {
	return docsync.Sync(context.Background(), docsync.URL(), home, root, version).Line()
}

// upgradeRealPackageRoot is the root Node's upgrade.mjs runs from: this
// binary's <root>/native/bin/<platform>-<arch>/, with <root>/src/cli/upgrade.mjs
// there unmoved by a symlink (Node takes its root from that module's real
// path) and the docs-sync child Node would spawn being this same binary.
func upgradeRealPackageRoot() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", false
	}
	root := pkgroot.FromExecutable(exe)
	module := filepath.Join(root, "src", "cli", "upgrade.mjs")
	if real, err := filepath.EvalSymlinks(module); err != nil || real != module {
		return "", false
	}
	arch := map[string]string{"arm64": "arm64", "amd64": "x64"}[runtime.GOARCH]
	if (runtime.GOOS != "darwin" && runtime.GOOS != "linux") || arch == "" {
		return "", false
	}
	child, _ := pkgroot.Find(root, runtime.GOOS+"-"+arch) // B606
	if real, err := filepath.EvalSymlinks(child); err != nil || real != exe {
		return "", false
	}
	return root, true
}

// hasVT reports text node:util stripVTControlCharacters would rewrite: its
// pattern changes between Node releases, so such output stays with Node.
func hasVT(s string) bool { return strings.ContainsAny(s, "\x1b\u009b") }

// upgradeTarget is one install record with what Node's upgradeAll needs.
type upgradeTarget struct {
	installRecord
	method string
}

// prepareUpgrade is src/cli/upgrade.mjs with upgradeAll and linkSkill from
// src/install/link.mjs. Everything that could make Node's run throw part
// way is checked first; such a run goes to Node before anything changes.
func prepareUpgrade(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	refuse := func(code int, text string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
		return func(_, stderr io.Writer) int {
			fmt.Fprint(stderr, text)
			return code
		}, 0, true
	}
	plain, source, hasSource := false, "", false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--plain" && !plain:
			plain = true
		case args[i] == "--source" && !hasSource:
			if i+1 >= len(args) || args[i+1] == "" || len(args[i+1]) >= 2 && args[i+1][:2] == "--" {
				return refuse(2, upgradeUsage)
			}
			source, hasSource = args[i+1], true
			i++
		default:
			return refuse(2, upgradeUsage)
		}
	}
	cwd := physicalCwd()
	// Node decodes argv and the cwd as UTF-8, so a broken byte names a
	// different path there.
	if !utf8.ValidString(source) || !utf8.ValidString(cwd) {
		return nil, hUpgradePathEncoding, false
	}
	checkSource := func(resolved string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
		problem := ""
		if info, err := os.Stat(resolved); err != nil {
			problem = "does not exist"
		} else if !info.IsDir() {
			problem = "is not a directory"
		}
		switch {
		case problem == "":
			return nil, 0, true
		case hasVT(resolved):
			return nil, hUpgradeControlChars, false
		}
		return refuse(1, "upgrade: source \""+resolved+"\" "+problem+"\n")
	}
	resolved := ""
	if hasSource {
		resolved = filepath.Clean(source)
		if !filepath.IsAbs(source) {
			resolved = filepath.Join(cwd, source)
		}
		if body, reason, ok := checkSource(resolved); body != nil || !ok {
			return body, reason, ok
		}
	}
	root, ok := upgradePackageRoot()
	if !ok {
		return nil, hUpgradePackageRoot, false
	}
	version, err := docsync.InstalledVersion(root)
	if err != nil {
		return nil, hUpgradePackageRoot, false
	}
	if !hasSource {
		resolved = filepath.Join(root, "skill")
		if body, reason, ok := checkSource(resolved); body != nil || !ok {
			return body, reason, ok
		}
	}
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return nil, hUpgradeNoHome, false
	}
	file := installsFilePath(home)
	if !cacheFileWritable(file) {
		return nil, hUpgradeUnfinishable, false
	}
	targets, ok := upgradeReadTargets(file)
	if !ok {
		return nil, hUpgradeLedgerEncoding, false
	}
	notes := filepath.Join(root, "CHANGELOG.md")
	var lowered []string
	for _, t := range targets {
		if reason, ok := upgradeLinkable(resolved, t, home); !ok {
			return nil, reason, false
		}
		lowered = append(lowered, strings.ToLower(canonicalTargetLocation(filepath.Clean(t.target))))
	}
	// One target inside another changes what the later link finds; the
	// release notes or the package itself under a target likewise.
	guarded := []string{strings.ToLower(notes), strings.ToLower(root)}
	for i, a := range lowered {
		for j, b := range lowered {
			if i != j && a != b && pathInside(a, b) {
				return nil, hUpgradeUnsafeTarget, false
			}
		}
		for _, g := range guarded {
			if a == g || pathInside(a, g) || pathInside(g, a) {
				return nil, hUpgradeUnsafeTarget, false
			}
		}
	}
	change := "first versioned upgrade"
	if oldest, ok, valid := upgradeOldestVersion(targets); !valid {
		return nil, hUpgradeVersionOrder, false
	} else if ok && oldest == version {
		change = "already on " + version
	} else if ok {
		change = oldest + " -> " + version
	}
	var head strings.Builder
	if !plain {
		head.WriteString(upgradeLogo + "code-forge " + version + "\n" + change + "\n")
	}
	for _, t := range targets {
		head.WriteString("upgraded " + t.harness + " (" + t.scope + ") -> " + resolved + "\n")
	}
	if len(targets) == 0 {
		head.WriteString("nothing to upgrade\n")
	}
	bullets, bulletVT := upgradeReleaseBullets(notes, version)
	if !plain && len(bullets) > 0 {
		head.WriteString("What's new in " + version + ":\n" + strings.Join(bullets, "\n") + "\nFull notes: " + notes + "\n")
	}
	if hasVT(head.String()) || !plain && bulletVT {
		return nil, hUpgradeControlChars, false
	}
	// Node runs the docs sync last; it writes only on a match with this
	// version (a line Node prints as is), so it runs here first and a line
	// Node would rewrite leaves the whole run to Node untouched.
	line := strings.TrimFunc(nodeUTF8(upgradeDocsSync(home, root, version)), isJSSpace)
	if hasVT(line) {
		return nil, hUpgradeControlChars, false
	}
	if line != "" {
		line += "\n"
	}
	return func(stdout, _ io.Writer) int {
		rows := make([]any, 0, len(targets))
		for _, t := range targets {
			if err := upgradeLink(resolved, t.target, t.method); err != nil {
				panic(err)
			}
			row := t.raw.Clone()
			row.Set("source", resolved)
			row.Set("linked_at", upgradeNow().UTC().Format("2006-01-02T15:04:05.000Z"))
			row.Set("version", version)
			rows = append(rows, row)
		}
		writeInstallsFile(file, rows)
		fmt.Fprint(stdout, head.String()+line)
		return 0
	}, 0, true
}

func pathInside(ancestor, p string) bool {
	if ancestor == p {
		return false
	}
	if !strings.HasSuffix(ancestor, "/") {
		ancestor += "/"
	}
	return strings.HasPrefix(p, ancestor)
}

// upgradeReadTargets is readInstalls on the file as Node decodes it; a file
// that is not UTF-8 reads differently there and is reported as not ok.
func upgradeReadTargets(file string) ([]upgradeTarget, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, true
	}
	if !utf8.Valid(data) {
		return nil, false
	}
	var targets []upgradeTarget
	for _, record := range readInstallRecords(file) {
		method, _ := record.raw.Get("method").(string)
		targets = append(targets, upgradeTarget{record, method})
	}
	return targets, true
}

// upgradeLinkable is what linkSkill and clearTarget check for one record,
// compared without case so Go never links what Node's case-correcting
// check refuses, plus the file system conditions under which every step
// (mkdir of the parent, rm -rf, symlink or cp) can finish.
func upgradeLinkable(source string, t upgradeTarget, home string) (handoverReason, bool) {
	target := filepath.Clean(t.target)
	located := strings.ToLower(canonicalTargetLocation(target))
	for _, form := range protectedPathForms(source) {
		form = strings.ToLower(form)
		if form == located || pathInside(form, located) || pathInside(located, form) {
			return hUpgradeUnsafeTarget, false
		}
	}
	if !installTargetSafe(target, source, home) {
		return hUpgradeUnsafeTarget, false
	}
	if !removable(target) || !upgradeParentMakeable(filepath.Dir(target)) {
		return hUpgradeUnfinishable, false
	}
	if t.method == "copy" {
		return upgradeCopyable(source)
	}
	return 0, true
}

// upgradeParentMakeable: mkdir -p dir can finish and dir can take a new entry.
func upgradeParentMakeable(dir string) bool {
	for {
		info, err := os.Stat(dir)
		if err == nil {
			return info.IsDir() && unixWritable(dir)
		}
		if !os.IsNotExist(err) {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// upgradeCopyable: the source tree holds only directories and regular files
// Go can read; fs.cp handles links and special files its own way.
func upgradeCopyable(source string) (handoverReason, bool) {
	reason, ok := handoverReason(0), true
	_ = filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			reason, ok = hUpgradeUnfinishable, false
		case d.IsDir():
			if syscall.Access(path, 5) != nil {
				reason, ok = hUpgradeUnfinishable, false
			}
		case d.Type().IsRegular():
			if syscall.Access(path, 4) != nil {
				reason, ok = hUpgradeUnfinishable, false
			}
		default:
			reason, ok = hUpgradeCopyEntry, false
		}
		if !ok {
			return filepath.SkipAll
		}
		return nil
	})
	return reason, ok
}

// upgradeLink is linkSkill's write half: parent made, target cleared, then
// a directory symlink or a copy of the source tree.
func upgradeLink(source, target, method string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	if method == "symlink" {
		return os.Symlink(source, target)
	}
	return upgradeCopyTree(source, target)
}

// upgradeCopyTree is fs.cp recursive: contents copied, each entry given
// its source mode, times left new.
func upgradeCopyTree(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.Mkdir(target, 0o777); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := upgradeCopyTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return os.Chmod(target, info.Mode().Perm()|info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chmod(target, info.Mode().Perm()|info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
}

// upgradeOldestVersion is the first of the recorded versions sorted by
// compareVersions. valid is false for a version whose order rests on ICU
// collation or a line break, which Go does not copy.
func upgradeOldestVersion(targets []upgradeTarget) (oldest string, found, valid bool) {
	core := regexp.MustCompile(`^(0|[1-9][0-9]*)(\.(0|[1-9][0-9]*))*$`)
	for _, t := range targets {
		v, ok := t.raw.Get("version").(string)
		if !ok {
			continue
		}
		if strings.ContainsAny(v, "\n\r  ") || !core.MatchString(upgradeVersionCore(v)) {
			return "", false, false
		}
		if !found || compareVersions(v, oldest) < 0 {
			oldest, found = v, true
		}
	}
	return oldest, found, true
}

func upgradeVersionCore(v string) string {
	core, _, _ := strings.Cut(strings.SplitN(v, "+", 2)[0], "-")
	return core
}

// compareVersions is the Node function for cores of dot-separated numbers
// without leading zeros (localeCompare numeric orders those part by part).
func compareVersions(a, b string) int {
	coreA, preA, hasA := strings.Cut(strings.SplitN(a, "+", 2)[0], "-")
	coreB, preB, hasB := strings.Cut(strings.SplitN(b, "+", 2)[0], "-")
	partsA, partsB := strings.Split(coreA, "."), strings.Split(coreB, ".")
	for i := 0; i < len(partsA) && i < len(partsB); i++ {
		x, y := partsA[i], partsB[i]
		if len(x) != len(y) {
			return len(x) - len(y)
		}
		if x != y {
			return strings.Compare(x, y)
		}
	}
	if len(partsA) != len(partsB) {
		return len(partsA) - len(partsB)
	}
	if !hasA {
		if !hasB {
			return 0
		}
		return 1
	}
	if !hasB {
		return -1
	}
	idsA, idsB := strings.Split(preA, "."), strings.Split(preB, ".")
	numeric := regexp.MustCompile(`^[0-9]+$`)
	for i := 0; i < len(idsA) || i < len(idsB); i++ {
		if i >= len(idsA) {
			return -1
		}
		if i >= len(idsB) {
			return 1
		}
		x, y := idsA[i], idsB[i]
		if x == y {
			continue
		}
		numA, numB := numeric.MatchString(x), numeric.MatchString(y)
		if numA != numB {
			if numA {
				return -1
			}
			return 1
		}
		if numA {
			fx, _ := strconv.ParseFloat(x, 64)
			fy, _ := strconv.ParseFloat(y, 64)
			switch {
			case fx < fy:
				return -1
			case fx > fy:
				return 1
			}
			return 0
		}
		if utf16Less(x, y) {
			return -1
		}
		return 1
	}
	return 0
}

func utf16Less(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// upgradeReleaseBullets is releaseBullets: up to five "- " lines of the
// exact release section, each cut to 100 code points; vt reports a bullet
// Node would first strip of terminal sequences.
func upgradeReleaseBullets(file, version string) (bullets []string, vt bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, false
	}
	inRelease := false
	for _, line := range strings.Split(nodeUTF8(string(data)), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "##") && len(line) > 2 {
			if r, _ := utf8.DecodeRuneInString(line[2:]); isJSSpace(r) {
				if inRelease {
					break
				}
				inRelease = releaseHeading(line) == version
				continue
			}
		}
		if inRelease && strings.HasPrefix(line, "- ") {
			vt = vt || hasVT(line[2:])
			chars := []rune(line[2:])
			if len(chars) > 100 {
				bullets = append(bullets, string(chars[:97])+"...")
			} else {
				bullets = append(bullets, string(chars))
			}
			if len(bullets) == 5 {
				break
			}
		}
	}
	return bullets, vt
}

// releaseHeading is /^## \[([^\]]+)\](?:\s|$)/'s capture, or "" without a match.
func releaseHeading(line string) string {
	rest, ok := strings.CutPrefix(line, "## [")
	if !ok {
		return ""
	}
	end := strings.IndexByte(rest, ']')
	if end <= 0 {
		return ""
	}
	if after := rest[end+1:]; after != "" {
		if r, _ := utf8.DecodeRuneInString(after); !isJSSpace(r) {
			return ""
		}
	}
	return rest[:end]
}
