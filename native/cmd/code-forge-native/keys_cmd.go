package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/childreg"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B456: `keys` in Go. Go serves every usage error, and with the 0600 file
// backend (CODE_FORGE_KEY_BACKEND=file) list, remove and test; on macOS with
// the default chain it serves list. A case that needs the @napi-rs/keyring
// backend, a 1Password call or the hidden prompt keeps the Node hand-over.

func init() { routes["keys"] = runKeys }

const keysUsage = "usage: code-forge keys list | set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]] | test <name> [--ref <ref>] | remove <name>\n"

var keysSubUsage = map[string]string{
	"list":   "usage: code-forge keys list\n",
	"set":    "usage: code-forge keys set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]]\n",
	"test":   "usage: code-forge keys test <name> [--ref <ref>]\n",
	"remove": "usage: code-forge keys remove <name>\n",
}

var keysFlags = map[string][]string{"list": {}, "set": {"--op", "--cache-hours"}, "test": {"--ref"}, "remove": {}}

const (
	keysDefaultCacheHours = 8
	keysCacheHoursRule    = "a whole number of hours from 1 to 24"
	keysHourMS            = 3_600_000
	keysWarnBelowMS       = 3_600_000
	keysFileWarning       = "keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store"
	keysOpApproveNote     = "1Password may ask you to approve access: check the 1Password window (Touch ID or password)."
	keysNamePatternText   = "key name must match /^[a-z][a-z0-9_-]{0,63}$/"
)

var (
	keysNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	keysOpRef       = regexp.MustCompile(`^op://[^/\r\n]+/[^/\r\n]+/[^\r\n]+$`)
	keysOpItemID    = regexp.MustCompile(`^[a-z0-9]{26}$`)
	keysShellSafe   = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=-]+$`)
	keysHTTPS       = regexp.MustCompile(`(?i)^https://`)
	// Seams for tests: the clock, the platform and the `security` binary.
	keysNow         = func() float64 { return float64(time.Now().UnixMilli()) }
	keysGOOS        = runtime.GOOS
	keysSecurityBin = "/usr/bin/security"
)

type keysParsed struct {
	sub, name, flagArg, cacheHoursArg string
	hasName, hasFlag, hasCacheHours   bool
}

// keysParse is parseKeysArgs in src/cli/keys.mjs.
func keysParse(args []string) (keysParsed, bool) {
	p := keysParsed{}
	if len(args) == 0 {
		return p, false
	}
	allowed, ok := keysFlags[args[0]]
	if !ok {
		return p, false
	}
	p.sub = args[0]
	rest := args[1:]
	if p.sub == "list" {
		return p, len(rest) == 0
	}
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") || (len(rest)-1)%2 != 0 {
		return p, false
	}
	p.name, p.hasName = rest[0], true
	values := map[string]string{}
	for i := 1; i < len(rest); i += 2 {
		flag, value := rest[i], rest[i+1]
		known := false
		for _, f := range allowed {
			known = known || f == flag
		}
		if _, seen := values[flag]; !known || seen || value == "" || strings.HasPrefix(value, "-") {
			return p, false
		}
		values[flag] = value
	}
	_, hasOp := values["--op"]
	if hours, ok := values["--cache-hours"]; ok {
		if !hasOp {
			return p, false
		}
		p.cacheHoursArg, p.hasCacheHours = hours, true
	}
	flag := "--ref"
	if p.sub == "set" {
		flag = "--op"
	}
	if v, ok := values[flag]; ok {
		p.flagArg, p.hasFlag = v, true
	}
	return p, true
}

func keysIsCacheHours(v any) (int, bool) {
	n, ok := v.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < 1 || n > 24 {
		return 0, false
	}
	return int(n), true
}

func keysParseCacheHours(text string) (int, bool) {
	if len(text) < 1 || len(text) > 3 || strings.Trim(text, "0123456789") != "" {
		return 0, false
	}
	n, _ := strconv.Atoi(text)
	return keysIsCacheHours(float64(n))
}

func keysOpItemFromInput(s string) string {
	text := strings.TrimSpace(s)
	if keysOpItemID.MatchString(text) {
		return text
	}
	if !keysHTTPS.MatchString(text) {
		return ""
	}
	u, err := url.Parse(text)
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	hostOK := false
	for _, h := range []string{"1password.com", "1password.ca", "1password.eu"} {
		hostOK = hostOK || host == h || strings.HasSuffix(host, "."+h)
	}
	if !hostOK {
		return ""
	}
	// Node's URLSearchParams.get returns the first `i`; any doubt hands over.
	id := u.Query().Get("i")
	if keysOpItemID.MatchString(id) {
		return id
	}
	return ""
}

// keysOpLike reports a value Node may treat as a 1Password item link even if
// Go's URL parser disagrees: any https:// text naming a 1Password host.
func keysOpLike(s string) bool {
	text := strings.ToLower(strings.TrimSpace(s))
	return keysHTTPS.MatchString(text) && strings.Contains(text, "1password.")
}

type keysCache struct {
	hours   int
	from    string
	warning string
	refuse  string
	handOff bool
}

// keysCacheHoursFor is cacheHoursFor in src/cli/keys.mjs.
func keysCacheHoursFor(p keysParsed, cwd string, strict bool) keysCache {
	fromFlag := 0
	if p.hasCacheHours {
		n, ok := keysParseCacheHours(p.cacheHoursArg)
		if !ok {
			return keysCache{refuse: "keys: --cache-hours must be " + keysCacheHoursRule + "\n"}
		}
		fromFlag = n
	}
	var cfg map[string]any
	out := keysCache{}
	loaded, err := config.LoadFile(filepath.Join(config.ProjectRootFor(cwd), config.Filename))
	if err == nil {
		cfg = loaded
	} else {
		var cerr *config.Error
		if !errors.As(err, &cerr) {
			return keysCache{handOff: true}
		}
		switch cerr.Kind {
		case "not-found":
		case "read-error", "parse-error", "migrate-error":
			used := fmt.Sprintf("the default %d h", keysDefaultCacheHours)
			if fromFlag != 0 {
				used = fmt.Sprintf("%d h from --cache-hours", fromFlag)
			}
			out.warning = "WARN keys: .code-forge.yml cannot be loaded (" + cerr.Kind + "); caching for " + used + "\n"
		default:
			// Node's loader has no such error class (Go adds merge-gate checks).
			return keysCache{handOff: true}
		}
	}
	hours, source, problem := keysDefaultCacheHours, "default", ""
	if section, ok := cfg["keys"].(map[string]any); ok {
		if value, has := section["cache_hours"]; has {
			if n, ok := keysIsCacheHours(value); ok {
				hours, source = n, "config"
			} else {
				problem = "keys.cache_hours must be " + keysCacheHoursRule
			}
		}
	}
	if problem != "" {
		if strict {
			return keysCache{refuse: "keys: " + problem + " (.code-forge.yml)\n"}
		}
		out.warning = fmt.Sprintf("WARN keys: %s (.code-forge.yml); caching for the default %d h\n", problem, hours)
	}
	if fromFlag != 0 {
		out.hours, out.from = fromFlag, "--cache-hours"
		return out
	}
	out.hours = hours
	if source == "config" {
		out.from = "keys.cache_hours"
	}
	return out
}

// keysRedactor is writeSafe: every registered value is masked on its way out.
type keysRedactor struct {
	secrets []string
}

func (r *keysRedactor) register(value string) {
	if value != "" {
		r.secrets = append(r.secrets, value)
	}
}

func (r *keysRedactor) clean(text string) string {
	type span struct{ start, end int }
	spans := []span{}
	for _, secret := range r.secrets {
		for at := 0; at <= len(text); {
			i := strings.Index(text[at:], secret)
			if i < 0 {
				break
			}
			spans = append(spans, span{at + i, at + i + len(secret)})
			at += i + 1
		}
	}
	if len(spans) == 0 {
		return text
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var b strings.Builder
	cursor, cur := 0, spans[0]
	for _, s := range spans[1:] {
		if s.start <= cur.end {
			cur.end = max(cur.end, s.end)
			continue
		}
		b.WriteString(text[cursor:cur.start] + "[REDACTED]")
		cursor, cur = cur.end, s
	}
	b.WriteString(text[cursor:cur.start] + "[REDACTED]" + text[cur.end:])
	return b.String()
}

func (r *keysRedactor) write(w io.Writer, text string) { _, _ = io.WriteString(w, r.clean(text)) }

// keysJSISO is new Date(ms).toISOString() for a valid time.
func keysJSISO(ms float64) string {
	whole := int64(math.Trunc(ms))
	t := time.UnixMilli(whole).UTC()
	year := t.Year()
	rest := t.Format("-01-02T15:04:05.000Z")
	if year >= 0 && year <= 9999 {
		return fmt.Sprintf("%04d%s", year, rest)
	}
	if year < 0 {
		return fmt.Sprintf("-%06d%s", -year, rest)
	}
	return fmt.Sprintf("+%06d%s", year, rest)
}

func keysIsTime(v any) (float64, bool) {
	n, ok := v.(float64)
	return n, ok && !math.IsNaN(n) && !math.IsInf(n, 0) && math.Abs(n) <= 8.64e15
}

// keysFormatExpiry is formatExpiry: nil is never, a non-time is unknown.
func keysFormatExpiry(exp any, now float64) string {
	if exp == nil {
		return "never"
	}
	n, ok := keysIsTime(exp)
	if !ok {
		return "unknown"
	}
	if n <= now {
		return "expired"
	}
	return keysJSISO(n)
}

func keysShellWord(ref string) string {
	if keysShellSafe.MatchString(ref) {
		return ref
	}
	return "'" + strings.ReplaceAll(ref, "'", `'\''`) + "'"
}

// keysJSString is String(value) for the JSON values an index row can hold.
func keysJSString(v any) string {
	switch x := v.(type) {
	case nil:
		return "undefined"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		data, _ := ledger.Stringify(x, false)
		return string(data)
	case ledger.Object:
		return "[object Object]"
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			if item != nil {
				parts[i] = keysJSString(item)
			}
		}
		return strings.Join(parts, ",")
	}
	return fmt.Sprint(v)
}

func keysUTF16Less(a, b string) bool {
	x, y := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}

// keysIndex reads index.json as readIndex does: missing or corrupt is empty;
// any other read error is returned.
func keysIndex(file string) (ledger.Object, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ledger.Object{}, nil
		}
		return nil, err
	}
	if !json.Valid(data) {
		// a SyntaxError in JSON.parse reads as empty
		return ledger.Object{}, nil
	}
	obj, err := ledger.DecodeObject(data)
	if err != nil {
		if err.Error() == "expected a JSON object" {
			// null, an array or a scalar reads as empty
			return ledger.Object{}, nil
		}
		return nil, errKeysUnsure
	}
	return obj, nil
}

var errKeysUnsure = errors.New("keys: unsure of Node parity")

// keysField is a property with its presence: String(undefined) differs from
// String(null).
func keysField(o ledger.Object, key string) (any, bool) {
	for _, field := range o {
		if field.Key == key {
			return field.Value, true
		}
	}
	return nil, false
}

func keysFieldString(o ledger.Object, key string) string {
	value, ok := keysField(o, key)
	if !ok {
		return "undefined"
	}
	if value == nil {
		return "null"
	}
	return keysJSString(value)
}

// keysUnlink is fs.rm on a file: never removes a directory.
func keysUnlink(file string) error { return syscall.Unlink(file) }

// keysWritePrivate is writePrivateFile in src/keys/backends/file.mjs.
func keysWritePrivate(file string, content []byte) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.%d.%s.tmp", filepath.Base(file), os.Getpid(), hex.EncodeToString(random)))
	handle, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = handle.Chmod(0600)
	if err == nil {
		_, err = handle.Write(content)
	}
	if err == nil {
		err = handle.Sync()
	}
	if err != nil {
		_ = handle.Close()
		_ = os.Remove(tmp)
		return err
	}
	_ = handle.Close()
	return os.Rename(tmp, file)
}

func keysUpdateIndex(file string, change func(*ledger.Object)) error {
	index, err := keysIndex(file)
	if err != nil {
		return err
	}
	change(&index)
	data, err := ledger.Stringify(index, true)
	if err != nil {
		return err
	}
	return keysWritePrivate(file, data)
}

func keysReason(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if name := blockErrno(errno); name != "UNKNOWN" {
			return name
		}
	}
	return "Error"
}

// keysSecurityAvailable is the security-cli backend's available(): macOS and
// `security help` exits 0 or 1 within 10 s.
func keysSecurityAvailable() bool {
	if keysGOOS != "darwin" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, keysSecurityBin, "help")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	err := childreg.Run(cmd)
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1 && ctx.Err() == nil
}

func keysHome() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return home
}

func runKeys(args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(os.Stdout, keysUsage)
		return 0
	}
	if len(args) > 1 && (args[1] == "--help" || args[1] == "-h") {
		if usage, ok := keysSubUsage[args[0]]; ok {
			fmt.Fprint(os.Stdout, usage)
			return 0
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		code, _ := handover(append([]string{"keys"}, args...))
		return code
	}
	plan := keysDecide(args, cwd)
	if plan == nil {
		code, _ := handover(append([]string{"keys"}, args...))
		return code
	}
	contextApplyTmpRoot()
	name := "keys"
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			if _, ok := keysFlags[arg]; ok {
				name += " " + arg
			}
			break
		}
	}
	before := gatesBreadcrumbName(name)
	stderr := &contextErrWriter{}
	code := plan(os.Stdout, stderr)
	if code != 0 {
		var message any
		if stderr.last != "" {
			message = stderr.last
		}
		sub := ""
		if name != "keys" {
			sub = strings.TrimPrefix(name, "keys ")
		}
		nativeVerbFailure("keys", sub, args, code, message, before)
	}
	return code
}

type keysRun func(stdout, stderr io.Writer) int

// keysDecide returns the Go run for a case Go serves, or nil to hand over.
// Nothing is written or changed before the decision.
func keysDecide(args []string, cwd string) keysRun {
	r := &keysRedactor{}
	p, ok := keysParse(args)
	if !ok {
		return func(_, stderr io.Writer) int { r.write(stderr, keysUsage); return 2 }
	}
	if p.hasName && !keysNamePattern.MatchString(p.name) {
		return func(_, stderr io.Writer) int { r.write(stderr, "keys: "+keysNamePatternText+"\n"); return 2 }
	}
	fileOnly := os.Getenv("CODE_FORGE_KEY_BACKEND") == "file"
	home := keysHome()
	dir := filepath.Join(home, ".code-forge", "store")
	indexFile := filepath.Join(dir, "index.json")
	now := keysNow()
	storeWarning := ""
	if fileOnly {
		storeWarning = "WARN " + keysFileWarning + "\n"
	}

	switch p.sub {
	case "list":
		if !fileOnly && !keysSecurityAvailable() {
			return nil
		}
		index, err := keysIndex(indexFile)
		if err != nil {
			return nil
		}
		type row struct {
			name, source, backend string
			exp, ref              any
		}
		rows := []row{}
		for _, field := range index {
			entry, ok := field.Value.(ledger.Object)
			if !ok {
				// a scalar or null row: Node lists "undefined" or throws; hand over
				return nil
			}
			rows = append(rows, row{field.Key, keysFieldString(entry, "source"), keysFieldString(entry, "backend"), entry.Get("exp"), entry.Get("ref")})
		}
		sort.SliceStable(rows, func(i, j int) bool { return keysUTF16Less(rows[i].name, rows[j].name) })
		return func(stdout, stderr io.Writer) int {
			if storeWarning != "" {
				r.write(stderr, storeWarning)
			}
			r.write(stdout, "NAME\tSOURCE\tBACKEND\tEXPIRES\n")
			for _, row := range rows {
				r.write(stdout, row.name+"\t"+row.source+"\t"+row.backend+"\t"+keysFormatExpiry(row.exp, now)+"\n")
			}
			for _, row := range rows {
				exp, ok := keysIsTime(row.exp)
				if !ok || exp <= now || exp-now >= keysWarnBelowMS {
					continue
				}
				minutes := max(1, int64(math.Floor((exp-now)/60_000)))
				ref := "<your 1Password reference>"
				if s, ok := row.ref.(string); ok {
					ref = keysShellWord(s)
				}
				r.write(stderr, fmt.Sprintf("WARN %s expires in %d min; re-link: code-forge keys set %s --op %s\n", row.name, minutes, row.name, ref))
			}
			return 0
		}
	case "remove":
		// B610: the default chain removes from the keychain through
		// `security` too (Node's second keychain backend then finds nothing)
		var kc *kcStore
		if !fileOnly {
			store, ok := kcOpen()
			if !ok {
				return nil
			}
			kc = &store
		}
		if _, err := keysIndex(indexFile); err != nil {
			return nil
		}
		return func(stdout, stderr io.Writer) int {
			r.write(stderr, storeWarning)
			removed := 0
			if kc != nil {
				if gone, _ := kc.delete(p.name); gone {
					removed++
				}
			}
			if keysUnlink(filepath.Join(dir, p.name+".key")) == nil {
				removed++
			}
			if err := keysUpdateIndex(indexFile, func(o *ledger.Object) { o.Delete(p.name) }); err != nil {
				r.write(stderr, "code-forge: verb \"keys\" failed: Error: "+keysReason(err)+"\n")
				return 1
			}
			plural := "s"
			if removed == 1 {
				plural = ""
			}
			r.write(stdout, fmt.Sprintf("removed %s (%d backend%s)\n", p.name, removed, plural))
			return 0
		}
	case "set":
		if !p.hasFlag {
			// the hidden prompt stays in Node
			return nil
		}
		cache := keysCacheHoursFor(p, cwd, true)
		if cache.handOff {
			return nil
		}
		if cache.refuse != "" {
			return func(_, stderr io.Writer) int { r.write(stderr, cache.refuse); return 2 }
		}
		if keysOpRef.MatchString(p.flagArg) || keysOpItemFromInput(p.flagArg) != "" || keysOpLike(p.flagArg) {
			// a 1Password call follows: Node only
			return nil
		}
		return func(_, stderr io.Writer) int {
			if cache.warning != "" {
				r.write(stderr, cache.warning)
			}
			r.write(stderr, p.name+": --op must be a 1Password item ID, item link or op://vault/item/field reference\n")
			return 2
		}
	case "test":
		var kc *kcStore
		if !fileOnly {
			store, ok := kcOpen()
			if !ok {
				return nil
			}
			kc = &store
		}
		cache := keysCacheHoursFor(p, cwd, false)
		if cache.handOff {
			return nil
		}
		fullRef := p.hasFlag && keysOpRef.MatchString(p.flagArg)
		if p.hasFlag && !fullRef && (keysOpItemFromInput(p.flagArg) != "" || keysOpLike(p.flagArg)) {
			return nil
		}
		prefix := ""
		if cache.warning != "" {
			prefix = cache.warning
		}
		if fullRef {
			prefix += keysOpApproveNote + "\n"
		}
		ref := p.flagArg
		envName := "CODE_FORGE_KEY_" + strings.ReplaceAll(strings.ToUpper(p.name), "-", "_")
		if strings.HasPrefix(ref, "env:") {
			envName = ref[4:]
		}
		storeName := p.name
		if strings.HasPrefix(ref, "keychain:") {
			storeName = ref[9:]
			if !keysNamePattern.MatchString(storeName) {
				return func(_, stderr io.Writer) int {
					r.write(stderr, prefix+"keys: "+keysNamePatternText+"\n")
					return 2
				}
			}
		}
		if strings.HasPrefix(ref, "op://") && !fullRef {
			return func(_, stderr io.Writer) int {
				r.write(stderr, prefix+"keys: key \""+p.name+"\": malformed 1Password reference\n")
				return 2
			}
		}
		if value := os.Getenv(envName); value != "" {
			return func(stdout, stderr io.Writer) int {
				r.write(stderr, prefix+storeWarning)
				r.register(value)
				r.write(stdout, p.name+": ok source=env expires=never\n")
				return 0
			}
		}
		if strings.HasPrefix(ref, "op://") {
			// a store miss would read 1Password: Node only
			return nil
		}
		if kc != nil {
			// B610: the keychain is read first; a miss goes on to the file
			raw, found, ok := kc.get(storeName)
			if !ok {
				return nil
			}
			if found {
				if _, err := keysIndex(indexFile); err != nil {
					return nil
				}
				return func(stdout, stderr io.Writer) int {
					r.write(stderr, prefix+storeWarning)
					errs := []string{}
					found, exp := keysKeychainRead(*kc, storeName, indexFile, raw, now, cache.hours, r, &errs)
					for _, e := range errs {
						r.write(stderr, p.name+": "+e+"\n")
					}
					if !found {
						r.write(stderr, p.name+": not found\n")
						return 1
					}
					r.write(stdout, p.name+": ok source="+kc.name+" expires="+keysFormatExpiry(exp, now)+"\n")
					return 0
				}
			}
		}
		keyFile := filepath.Join(dir, storeName+".key")
		raw, readErr := os.ReadFile(keyFile)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			if keysReason(readErr) == "Error" {
				return nil
			}
		}
		if readErr == nil {
			if _, err := keysIndex(indexFile); err != nil {
				return nil
			}
		}
		return func(stdout, stderr io.Writer) int {
			r.write(stderr, prefix+storeWarning)
			errs := []string{}
			found, exp := keysFileRead(storeName, keyFile, indexFile, raw, readErr, now, cache.hours, r, &errs)
			for _, e := range errs {
				r.write(stderr, p.name+": "+e+"\n")
			}
			if !found {
				r.write(stderr, p.name+": not found\n")
				return 1
			}
			r.write(stdout, p.name+": ok source=file expires="+keysFormatExpiry(exp, now)+"\n")
			return 0
		}
	}
	return nil
}

// keysFileRead is store.read over the file backend alone.
func keysFileRead(name, keyFile, indexFile string, raw []byte, readErr error, now float64, hours int, r *keysRedactor, errs *[]string) (bool, any) {
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return false, nil
		}
		*errs = append(*errs, "file: read failed ("+keysReason(readErr)+")")
		return false, nil
	}
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" {
		return false, nil
	}
	value, exp := keysDecodeEntry(text)
	r.register(value)
	latest := now + float64(hours)*keysHourMS
	if n, ok := exp.(float64); ok && n <= now {
		if err := keysUnlink(keyFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			*errs = append(*errs, "file: delete of expired entry failed ("+keysReason(err)+")")
		}
		if err := keysUpdateIndex(indexFile, func(o *ledger.Object) { o.Delete(name) }); err != nil {
			*errs = append(*errs, "index: removal of the expired entry failed ("+keysReason(err)+")")
		}
		return false, nil
	}
	if n, ok := exp.(float64); ok && n > latest {
		entry, _ := ledger.Stringify(ledger.Object{{Key: "v", Value: value}, {Key: "exp", Value: latest}}, false)
		if err := keysWritePrivate(keyFile, entry); err != nil {
			*errs = append(*errs, "file: rewrite of the shortened expiry failed ("+keysReason(err)+")")
			return true, latest
		}
		err := keysUpdateIndex(indexFile, func(o *ledger.Object) {
			row, ok := o.Get(name).(ledger.Object)
			if !ok {
				return
			}
			row = row.Clone()
			row.Set("backend", "file")
			row.Set("exp", latest)
			o.Set(name, row)
		})
		if err != nil {
			*errs = append(*errs, "index: rewrite of the shortened expiry failed ("+keysReason(err)+")")
		}
		return true, latest
	}
	return true, exp
}

// keysKeychainRead is store.read's keychain hit: an expired item is deleted
// with its index row (not found), an expiry past the cache hours is cut.
func keysKeychainRead(kc kcStore, name, indexFile, raw string, now float64, hours int, r *keysRedactor, errs *[]string) (bool, any) {
	value, exp := keysDecodeEntry(raw)
	r.register(value)
	latest := now + float64(hours)*keysHourMS
	if n, ok := exp.(float64); ok && n <= now {
		if _, ok := kc.delete(name); !ok {
			*errs = append(*errs, kc.name+": delete of expired entry failed (Error)")
		}
		if err := keysUpdateIndex(indexFile, func(o *ledger.Object) { o.Delete(name) }); err != nil {
			*errs = append(*errs, "index: removal of the expired entry failed ("+keysReason(err)+")")
		}
		return false, nil
	}
	if n, ok := exp.(float64); ok && n > latest {
		entry, _ := ledger.Stringify(ledger.Object{{Key: "v", Value: value}, {Key: "exp", Value: latest}}, false)
		if reason := kc.set(name, string(entry)); reason != "" {
			*errs = append(*errs, kc.name+": rewrite of the shortened expiry failed ("+reason+")")
			return true, latest
		}
		err := keysUpdateIndex(indexFile, func(o *ledger.Object) {
			row, ok := o.Get(name).(ledger.Object)
			if !ok {
				return
			}
			row = row.Clone()
			row.Set("backend", kc.name)
			row.Set("exp", latest)
			o.Set(name, row)
		})
		if err != nil {
			*errs = append(*errs, "index: rewrite of the shortened expiry failed ("+keysReason(err)+")")
		}
		return true, latest
	}
	return true, exp
}

// keysDecodeEntry is decodeEntry: our {"v","exp"} JSON, else the raw text.
func keysDecodeEntry(raw string) (string, any) {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var parsed map[string]any
	if dec.Decode(&parsed) != nil || dec.More() {
		return raw, nil
	}
	v, ok := parsed["v"].(string)
	if !ok {
		return raw, nil
	}
	if n, ok := parsed["exp"].(json.Number); ok {
		if f, err := strconv.ParseFloat(string(n), 64); err == nil && !math.IsInf(f, 0) {
			return v, f
		}
	}
	return v, nil
}
