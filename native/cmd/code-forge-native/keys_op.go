package main

// B678: the 1Password half of `keys set --op` and `keys test --ref` in Go:
// opRead and toOpRef (src/keys/onepassword.mjs), readOpAndCache and
// store.put (src/keys/store.mjs). `op` runs as Node's exec runs it (PATH
// lookup, its own process group, stdin closed, CODE_FORGE_OWNER_PID set,
// 60 s, retried once only after a first-call timeout); no message carries
// its output.

import (
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

// keysOpRead is opRead: the value (one trailing line break dropped), or ""
// with the user-facing failure message; the failure kind goes to the error
// log as reportErrorKind does.
func keysOpRead(ref string) (string, string) {
	if !opOnPath() {
		nativeReportedKind = "op_missing"
		return "", opMessages["op_missing"]
	}
	argv := []string{"op", "read", ref}
	res := toolsExec(argv, opAttemptTimeout(1))
	if !res.ok && res.timedOut {
		res = toolsExec(argv, opAttemptTimeout(2))
		logNativeWarning("keys", "op_retry", "1Password did not answer in time; the call was retried once")
	}
	if !res.ok {
		kind, message := iwjOpFailure(res, "op read "+ref, "; check the reference")
		nativeReportedKind = kind
		return "", message
	}
	value := nodeUTF8(res.stdout)
	if strings.HasSuffix(value, "\n") {
		value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	}
	if value == "" {
		nativeReportedKind = "op_bad_output"
		return "", opMessages["op_bad_output"]
	}
	return value, ""
}

// keysOpResolve is the item ID / link step of runKeys: toOpRef, then
// `<name>: <describeOpItem> -> <ref>` on stdout; ok false after
// `<name>: <error>` on stderr.
func keysOpResolve(name, itemID string, r *keysRedactor, stdout, stderr io.Writer) (string, bool) {
	res := iwjResolveItemFor("keys", itemID)
	if res.ref == "" {
		nativeReportedKind = res.kind
		r.write(stderr, name+": "+res.err+"\n")
		return "", false
	}
	found := iwjDescribe(res.found)
	if found != "" {
		found += " -> "
	}
	r.write(stdout, name+": "+found+res.ref+"\n")
	return res.ref, true
}

// keysWriter is the store's writer: the keychain (Node's keychain or
// security-cli backend, through `security`) or the 0600 file backend.
type keysWriter struct {
	kc             *kcStore
	dir, indexFile string
}

func (w keysWriter) name() string {
	if w.kc != nil {
		return w.kc.name
	}
	return "file"
}

// put is store.put: the {"v","exp"} entry to the writer, then the index
// row; "" or the reason Node's reasonOf gives for the failed step, and the
// message of the error Node's put throws there (B759: an unreadable index
// prints its fs text).
func (w keysWriter) put(name, value, source string, exp any, ref string) (string, string) {
	entry, err := ledger.Stringify(ledger.Object{{Key: "v", Value: value}, {Key: "exp", Value: exp}}, false)
	if err != nil {
		return "Error", "Error"
	}
	if w.kc != nil {
		if reason := w.kc.set(name, string(entry)); reason != "" {
			return reason, reason
		}
	} else if err := keysWritePrivate(filepath.Join(w.dir, name+".key"), entry); err != nil {
		return keysReason(err), keysReason(err)
	}
	row := ledger.Object{{Key: "source", Value: source}, {Key: "backend", Value: w.name()}, {Key: "exp", Value: exp}}
	if ref != "" {
		row = append(row, ledger.Field{Key: "ref", Value: ref})
	}
	if err := keysUpdateIndex(w.indexFile, func(o *ledger.Object) { o.Set(name, row) }); err != nil {
		return keysReason(err), keysThrown(err)
	}
	return "", ""
}

// keysReadOpAndCache is readOpAndCache: the value read from 1Password,
// cached for hours with the reference in its index row; a failed cache
// write is a reason, the value stays.
func keysReadOpAndCache(w keysWriter, storeName, opRef string, now float64, hours int, r *keysRedactor) (string, float64, []string) {
	value, message := keysOpRead(opRef)
	if value == "" {
		return "", 0, []string{message}
	}
	r.register(value)
	exp := now + float64(hours)*keysHourMS
	errs := []string{}
	if reason, _ := w.put(storeName, value, "op", exp, opRef); reason != "" {
		errs = append(errs, "cache write failed ("+reason+")")
	}
	return value, exp, errs
}

// keysCacheFrom is the stored line's ` (cache <h> h from <where>)`.
func keysCacheFrom(cache keysCache) string {
	if cache.from == "" {
		return ""
	}
	return " (cache " + strconv.Itoa(cache.hours) + " h from " + cache.from + ")"
}
