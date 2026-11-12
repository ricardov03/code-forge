package main

import (
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/reviewq"
)

// B587: Node's loadGate exclusion step (block.mjs). Each changed file that a
// review.exclude rule (or the default lock-file list) matches leaves the
// review set, keeps its proof check, gets a gate.review_excluded close row and,
// for an npm lock, the lockfileProblem safety check.
type closeExcludedFile struct{ File, Hash, Rule string }

// closeExclusions splits the block's file set. rulesOK is false for a
// review.exclude that is not a list of strings (Node throws a TypeError);
// lockOK is false for a lock file closeLockfileProblem cannot judge. Both are
// decided before any write.
func closeExclusions(cfg map[string]any, top string, files []gatecheck.File) (kept []gatecheck.File, excluded []closeExcludedFile, lock []gatecheck.Blocker, rulesOK, lockOK bool) {
	kept = []gatecheck.File{}
	for _, f := range files {
		rule, ok := reviewq.Exclusion(cfg, f.File)
		if !ok {
			return nil, nil, nil, false, true
		}
		if rule == "" {
			kept = append(kept, f)
			continue
		}
		excluded = append(excluded, closeExcludedFile{f.File, f.Hash, rule})
	}
	for _, f := range excluded {
		problem, ok := closeLockfileProblem(top, f.File, cfg)
		if !ok {
			return nil, nil, nil, true, false
		}
		if problem != "" {
			lock = append(lock, gatecheck.Blocker{Code: "lockfile", File: f.File, Detail: problem})
		}
	}
	return kept, excluded, lock, true, true
}

// closeExcludedRows are Node's {event, block, ...fileSetEntry, rule} rows.
func closeExcludedRows(id string, excluded []closeExcludedFile) []ledger.Object {
	rows := []ledger.Object{}
	for _, f := range excluded {
		rows = append(rows, ledger.Object{{Key: "event", Value: "gate.review_excluded"}, {Key: "block", Value: id}, {Key: "file", Value: f.File}, {Key: "content_hash", Value: f.Hash}, {Key: "rule", Value: f.Rule}})
	}
	return rows
}

// closeLockReason is the reviews check's reason with lock blockers first:
// [...loadBlockers.map(`${code} ${file}: ${detail}`), result.reason].join('; ').
func closeLockReason(lock []gatecheck.Blocker, gateReason string) string {
	if len(lock) == 0 {
		return gateReason
	}
	parts := []string{}
	for _, b := range lock {
		parts = append(parts, b.Code+" "+b.File+": "+b.Detail)
	}
	if gateReason != "" {
		parts = append(parts, gateReason)
	}
	return strings.Join(parts, "; ")
}

var ceSurrogateEscape = regexp.MustCompile(`\\u[dD][89a-fA-F][0-9a-fA-F]{2}`)

// closeLockfileProblem is gate-check.mjs lockfileProblem: "" for a safe,
// deleted or non-npm lock. ok is false for a shape Go does not judge
// byte-equal (an escaped surrogate, an array "resolved", a URL outside the
// forms closeLockURL proves, registries that are not a list of strings).
func closeLockfileProblem(top, file string, cfg map[string]any) (string, bool) {
	if base := path.Base(file); base != "package-lock.json" && base != "npm-shrinkwrap.json" {
		return "", true
	}
	data, err := gitx.ReadRegularFileNoFollow(top, file)
	if err != nil {
		return "cannot read valid lock JSON", true
	}
	if data == nil {
		return "", true
	}
	text := jsDecode(data)
	if ceSurrogateEscape.MatchString(text) {
		return "", false
	}
	value, err := ledger.DecodeValue([]byte(text))
	if err != nil {
		return "cannot read valid lock JSON", true
	}
	lock, isObject := value.(ledger.Object)
	if !isObject {
		return "expected a lock object", true
	}
	hosts := []string{"registry.npmjs.org"}
	if review := blockMap(cfg["review"]); review["lockfile_registries"] != nil {
		list, ok := review["lockfile_registries"].([]any)
		if !ok {
			return "", false
		}
		hosts = []string{}
		for _, v := range list {
			s, ok := v.(string)
			if !ok {
				return "", false
			}
			hosts = append(hosts, s)
		}
	}
	var check func(entries any, prefix string, recursive bool) (string, bool)
	check = func(entries any, prefix string, recursive bool) (string, bool) {
		obj, ok := entries.(ledger.Object)
		if !ok {
			return prefix + " must be an object", true
		}
		for _, field := range ceEntries(obj) {
			entry := prefix + "." + field.Key
			pkg, ok := field.Value.(ledger.Object)
			if !ok {
				return entry + " must be an object", true
			}
			resolved, has := ceField(pkg, "resolved")
			if pkg.Get("link") != true && has {
				valid, protocol, host, ok := closeLockURL(resolved)
				if !ok {
					return "", false
				}
				if !valid {
					return entry + ".resolved must be an HTTPS URL on an allowed host", true
				}
				allowed := false
				for _, h := range hosts {
					allowed = allowed || h == host
				}
				if protocol != "https:" || !allowed {
					return entry + ".resolved must be HTTPS on review.lockfile_registries", true
				}
				if integrity, ok := pkg.Get("integrity").(string); !ok || jsTrim(integrity) == "" {
					return entry + " has no integrity", true
				}
			}
			if deps, has := ceField(pkg, "dependencies"); recursive && has {
				if problem, ok := check(deps, entry+".dependencies", true); !ok || problem != "" {
					return problem, ok
				}
			}
		}
		return "", true
	}
	packages, has := ceField(lock, "packages")
	if !has || packages == nil {
		packages = ledger.Object{}
	}
	if problem, ok := check(packages, "packages", false); !ok || problem != "" {
		return problem, ok
	}
	deps, has := ceField(lock, "dependencies")
	if !has {
		return "", true
	}
	return check(deps, "dependencies", true)
}

func ceField(o ledger.Object, key string) (any, bool) {
	for _, f := range o {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

// ceEntries is Object.entries order: array index keys first, ascending.
func ceEntries(o ledger.Object) ledger.Object {
	out := o.Clone()
	index := func(key string) (uint64, bool) {
		n, err := strconv.ParseUint(key, 10, 32)
		return n, err == nil && n < 4294967295 && strconv.FormatUint(n, 10) == key
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, okA := index(out[i].Key)
		b, okB := index(out[j].Key)
		if okA != okB {
			return okA
		}
		return okA && a < b
	})
	return out
}

var (
	ceSchemeRE  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*:`)
	ceDomainRE  = regexp.MustCompile(`^[A-Za-z0-9.\-]+$`)
	ceDigitsRE  = regexp.MustCompile(`^[0-9]*$`)
	ceNumericRE = regexp.MustCompile(`^(?:[0-9]+|0x[0-9a-f]*)$`)
)

// closeLockURL is `new URL(String(resolved))` reduced to what lockfileProblem
// reads: whether it parses, its protocol, and its host. ok is false for any
// input outside the WHATWG forms proved here (Go then hands the close over).
func closeLockURL(resolved any) (valid bool, protocol, host string, ok bool) {
	var s string
	switch v := resolved.(type) {
	case string:
		s = v
	case nil, bool, float64, ledger.Object:
		// "null", "true", a number, "[object Object]": no scheme, no base.
		return false, "", "", true
	default:
		return false, "", "", false
	}
	s = strings.TrimFunc(s, func(r rune) bool { return r <= 0x20 })
	s = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(s)
	scheme := ceSchemeRE.FindString(s)
	if scheme == "" {
		return false, "", "", true
	}
	rest := s[len(scheme):]
	scheme = strings.ToLower(scheme)
	switch scheme {
	case "https:", "http:", "ws:", "wss:", "ftp:":
		if strings.Contains(rest, "\\") || !strings.HasPrefix(rest, "//") || strings.HasPrefix(rest, "///") {
			return false, "", "", false
		}
		authority := rest[2:]
		if i := strings.IndexAny(authority, "/?#"); i >= 0 {
			authority = authority[:i]
		}
		if strings.ContainsAny(authority, "@[]") {
			return false, "", "", false
		}
		name, port, hasPort := strings.Cut(authority, ":")
		if name == "" {
			return false, "", "", true
		}
		if !ceDomainRE.MatchString(name) {
			return false, "", "", false
		}
		name = strings.ToLower(name)
		labels := strings.Split(strings.TrimSuffix(name, "."), ".")
		last := labels[len(labels)-1]
		if ceNumericRE.MatchString(last) {
			return false, "", "", false
		}
		for _, label := range labels {
			if strings.HasPrefix(label, "xn--") {
				return false, "", "", false
			}
		}
		host = name
		if hasPort && port != "" {
			if !ceDigitsRE.MatchString(port) {
				return false, "", "", true
			}
			n, err := strconv.ParseUint(port, 10, 64)
			if err != nil || len(port) > 10 || n > 65535 {
				return false, "", "", true
			}
			defaults := map[string]uint64{"https:": 443, "http:": 80, "ws:": 80, "wss:": 443, "ftp:": 21}
			if n != defaults[scheme] {
				host += ":" + strconv.FormatUint(n, 10)
			}
		}
		return true, scheme, host, true
	case "file:":
		if strings.Contains(rest, "\\") || strings.HasPrefix(rest, "//") && !strings.HasPrefix(rest, "///") {
			return false, "", "", false
		}
		return true, scheme, "", true
	}
	if !strings.HasPrefix(rest, "//") {
		// An opaque or absolute path never fails to parse.
		return true, scheme, "", true
	}
	authority := rest[2:]
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	at := strings.LastIndex(authority, "@")
	hostPort := authority[at+1:]
	if strings.HasPrefix(hostPort, "[") {
		return false, "", "", false
	}
	name, port, hasPort := strings.Cut(hostPort, ":")
	if at >= 0 && hostPort == "" || hasPort && name == "" {
		return false, "", "", true
	}
	if strings.ContainsAny(name, "\x00 <>[]\\^|") {
		return false, "", "", true
	}
	if hasPort && port != "" {
		if !ceDigitsRE.MatchString(port) {
			return false, "", "", true
		}
		if n, err := strconv.ParseUint(port, 10, 64); err != nil || len(port) > 10 || n > 65535 {
			return false, "", "", true
		}
	}
	return true, scheme, "", true
}
