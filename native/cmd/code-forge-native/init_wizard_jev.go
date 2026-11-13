package main

// B611: the interactive init's step 5 in Go when no Jev flag and no
// keys.jev is given (src/install/wizard/run.mjs, steps.mjs askJevSource and
// askOpRef, src/keys/onepassword.mjs toOpRef, src/keys/store.mjs put): the
// key found in the environment, the keychain or the file store is used as
// is; otherwise the person picks a 1Password item, an environment variable,
// a pasted key (stored, never shown) or skip.

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/ledger"
)

const (
	iwjOpRefHint       = "Paste the item ID (from `op item list`), the item's link, or its op:// secret reference."
	iwjOpRefQuestion   = "1Password item ID, item link, or op:// reference"
	iwjOpFullQuestion  = "Full op://vault/item/field reference"
	iwjOpResolveFailed = "1Password CLI failed (unexpected error); run `op item get <item-id>` yourself to see why"
	iwjLaterText       = "keys: jev not set — set it later with: code-forge keys set jev --op <item-id>"
	iwjApproveNote     = "1Password may ask you to approve access: check the 1Password window (Touch ID or password)."
	iwjOpNoField       = "the 1Password item has no single key field; pass a full op://vault/item/field reference"
	iwjOpBadInput      = "not an op:// reference, 1Password item ID or item link"
	iwjOpFailed        = "1Password CLI failed"
)

var (
	iwjEnvName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	iwjVaultID  = regexp.MustCompile(`^[a-z0-9]+$`)
	iwjCtrlText = regexp.MustCompile(`[\x00-\x1f\x7f]`)
)

// initWizJev is the Jev answer step 5 gives the setup: the reference kept in
// the config ("" drops keys.jev), the user-level source, and the values Node
// registered for redaction on the way.
type initWizJev struct {
	ref, source string
	secrets     []string
}

// iwjFound is a JevAnswer's found: what describeOpItem names.
type iwjFound struct{ title, vault, field string }

// iwjDescribe is describeOpItem; "" when nothing is known.
func iwjDescribe(f iwjFound) string {
	parts := []string{}
	if f.vault != "" {
		parts = append(parts, "vault "+f.vault)
	}
	if f.field != "" {
		parts = append(parts, "field "+f.field)
	}
	if f.title == "" && len(parts) == 0 {
		return ""
	}
	out := "1Password item"
	if f.title != "" {
		out += ` "` + f.title + `"`
	}
	if len(parts) > 0 {
		out += " (" + strings.Join(parts, ", ") + ")"
	}
	return out
}

// iwjOpResult is toOpRef's answer: ref "" exactly when err and kind are set.
type iwjOpResult struct {
	ref, kind, err string
	found          iwjFound
}

// iwjCleanText is cleanText: control characters removed, at most 64 UTF-16
// units (a split pair prints as U+FFFD, as Node writes a lone surrogate).
func iwjCleanText(text string) string {
	units := utf16.Encode([]rune(iwjCtrlText.ReplaceAllString(text, "")))
	if len(units) > 64 {
		units = units[:64]
	}
	return string(utf16.Decode(units))
}

// iwjJSString is String(x) on a value JSON.parse gave; present false is
// undefined.
func iwjJSString(o ledger.Object, key string) string {
	v, present := keysField(o, key)
	switch {
	case !present:
		return "undefined"
	case v == nil:
		return "null"
	}
	return keysJSString(v)
}

func iwjString(o ledger.Object, key string) (string, bool) {
	v, present := keysField(o, key)
	s, ok := v.(string)
	return s, present && ok
}

// iwjOpFailure is failureMessage over classifyOpFailure.
func iwjOpFailure(res childResult, command, notFoundSuffix string) (kind, message string) {
	stderr := nodeUTF8(res.stderr)
	switch {
	case res.timedOut:
		kind = "op_timeout"
	case res.code != nil && *res.code == 127:
		kind = "op_missing"
	case opLocked.MatchString(stderr):
		kind = "op_locked"
	case opNotFound.MatchString(stderr):
		kind = "op_not_found"
	default:
		how := "no exit code"
		switch {
		case res.code != nil:
			how = "exit " + strconv.Itoa(*res.code)
		case res.signal != "":
			how = "signal " + res.signal
		}
		return "op_failed", iwjOpFailed + " (" + how + "); run `" + command + "` yourself to see why"
	}
	if kind == "op_not_found" {
		return kind, opMessages[kind] + notFoundSuffix
	}
	return kind, opMessages[kind]
}

// iwjPickField is pickField: id credential with a value, else purpose
// PASSWORD, else the one filled CONCEALED field.
func iwjPickField(fields []any) (ledger.Object, string) {
	objects := []ledger.Object{}
	for _, f := range fields {
		if o, ok := f.(ledger.Object); ok {
			objects = append(objects, o)
		} else {
			objects = append(objects, nil)
		}
	}
	hasValue := func(f ledger.Object) bool { s, ok := iwjString(f, "value"); return ok && s != "" }
	for _, f := range objects {
		if id, _ := iwjString(f, "id"); f != nil && id == "credential" && hasValue(f) {
			return f, ""
		}
	}
	for _, f := range objects {
		if p, _ := iwjString(f, "purpose"); f != nil && p == "PASSWORD" {
			return f, ""
		}
	}
	var concealed, filled []ledger.Object
	for _, f := range objects {
		if t, _ := iwjString(f, "type"); f != nil && t == "CONCEALED" {
			concealed = append(concealed, f)
			if hasValue(f) {
				filled = append(filled, f)
			}
		}
	}
	if len(filled) == 1 {
		return filled[0], ""
	}
	labels := []string{}
	for i, f := range concealed {
		if i == 10 {
			break
		}
		text := iwjJSString(f, "id")
		if label, ok := iwjString(f, "label"); ok {
			text = label
		}
		quoted, _ := ledger.Stringify(iwjCleanText(text), false)
		labels = append(labels, string(quoted))
	}
	more := ""
	if len(concealed) > 10 {
		more = " and " + strconv.Itoa(len(concealed)-10) + " more"
	}
	which := "no concealed field"
	if len(labels) > 0 {
		which = "concealed fields: " + strings.Join(labels, ", ") + more
	}
	return nil, iwjOpNoField + " (" + which + ")"
}

// iwjResolveItem is resolveOpItemRef: `op item get <id> --format json`
// (retried once after a first-call timeout) turned into
// op://<vaultId>/<itemId>/<fieldId>; only ids and labels are taken.
func iwjResolveItem(itemID string) iwjOpResult {
	bad := iwjOpResult{kind: "op_bad_output", err: opMessages["op_bad_output"]}
	if !opOnPath() {
		return iwjOpResult{kind: "op_missing", err: opMessages["op_missing"]}
	}
	argv := []string{"op", "item", "get", itemID, "--format", "json"}
	res := toolsExec(argv, opAttemptTimeout(1))
	if !res.ok && res.timedOut {
		res = toolsExec(argv, opAttemptTimeout(2))
		logNativeWarning("init", "op_retry", "1Password did not answer in time; the call was retried once")
	}
	if !res.ok {
		kind, message := iwjOpFailure(res, "op item get "+itemID, " (item "+itemID+")")
		return iwjOpResult{kind: kind, err: message}
	}
	value, err := ledger.DecodeValue([]byte(nodeUTF8(res.stdout)))
	item, ok := value.(ledger.Object)
	if err != nil || !ok {
		return bad
	}
	vault, _ := item.Get("vault").(ledger.Object)
	vaultID, vok := iwjString(vault, "id")
	id, iok := iwjString(item, "id")
	fields, fok := item.Get("fields").([]any)
	if vault == nil || !vok || !iwjVaultID.MatchString(vaultID) || !iok || id != itemID || !fok {
		return bad
	}
	field, message := iwjPickField(fields)
	if field == nil {
		return iwjOpResult{kind: "op_no_field", err: message}
	}
	fieldID, ok := iwjString(field, "id")
	if !ok || fieldID == "" || strings.ContainsAny(fieldID, "/\r\n") {
		return bad
	}
	label := fieldID
	if l, ok := iwjString(field, "label"); ok && l != "" {
		label = l
	}
	name := vaultID
	if n, ok := iwjString(vault, "name"); ok {
		name = n
	}
	title := itemID
	if t, ok := iwjString(item, "title"); ok {
		title = t
	}
	return iwjOpResult{ref: "op://" + vaultID + "/" + itemID + "/" + fieldID,
		found: iwjFound{title: iwjCleanText(title), vault: iwjCleanText(name), field: iwjCleanText(label)}}
}

// iwjToOpRef is the wizard's resolveOp: the approval note on stderr when op
// will really be called, then toOpRef (an op:// reference passes as is; a
// failure's kind is reported for the error log).
func iwjToOpRef(input string, stderr io.Writer) iwjOpResult {
	id := keysOpItemFromInput(input)
	if id != "" {
		_, _ = io.WriteString(stderr, iwjApproveNote+"\n")
	}
	text := jsTrim(input)
	if initOpRef.MatchString(text) {
		return iwjOpResult{ref: text}
	}
	res := iwjOpResult{kind: "op_bad_input", err: iwjOpBadInput}
	if id != "" {
		res = iwjResolveItem(id)
	}
	if res.ref == "" {
		nativeReportedKind = res.kind
	}
	return res
}

// iwjAnswer is a JevAnswer: ref "" is null.
type iwjAnswer struct {
	ref, source string
	found       iwjFound
	secret      string // a pasted key, registered for redaction
}

// askOpRef is askOpRef: other true is "Choose another key source".
func (u *initWizUI) askOpRef(stderr io.Writer) (ans iwjAnswer, other, ok bool) {
	input, failure := "", ""
	question := iwjOpRefQuestion
	for {
		if failure == "" {
			typed, ok := u.text(initWizWithHint(question, iwjOpRefHint), input)
			if !ok {
				return iwjAnswer{}, false, false
			}
			input = jsTrim(typed)
			res := iwjOpResult{kind: "op_bad_input", err: iwjOpBadInput}
			if question == iwjOpRefQuestion || initOpRef.MatchString(input) {
				res = iwjToOpRef(input, stderr)
			}
			if res.ref != "" {
				return iwjAnswer{ref: res.ref, source: "op", found: res.found}, false, true
			}
			failure = res.err
			if failure == "" {
				failure = iwjOpResolveFailed
			}
		}
		next, ok := u.sel(initWizWithHint("1Password lookup failed. What next?", failure), []initWizOption{
			{value: "retry", label: "Try again"},
			{value: "full", label: "Enter a full op:// reference"},
			{value: "other", label: "Choose another key source"},
			{value: "skip", label: "Skip for now"},
		}, "retry")
		if !ok {
			return iwjAnswer{}, false, false
		}
		failure = ""
		switch next {
		case "other":
			return iwjAnswer{}, true, true
		case "skip":
			return iwjAnswer{source: "none"}, false, true
		case "full":
			question, input = iwjOpFullQuestion, ""
		default:
			question = iwjOpRefQuestion
		}
	}
}

// askJevSource is askJevSource with no pending flag value. usage is Node's
// UsageError text; storeErr is a key store write that threw.
func (u *initWizUI) askJevSource(home string, stderr io.Writer) (ans iwjAnswer, usage, storeErr string, ok bool) {
	for {
		choice, ok := u.sel(initWizWithHint("Where is the Jev key?", "Jev is the small fast classifier behind System 1; skip uses rules only."), []initWizOption{
			{value: "op", label: "1Password item or reference", hint: iwjOpRefHint},
			{value: "env", label: "environment variable name"},
			{value: "paste", label: "paste now (hidden)"},
			{value: "skip", label: "skip (rules-only System 1)"},
		}, "skip")
		if !ok {
			return iwjAnswer{}, "", "", false
		}
		switch choice {
		case "op":
			ans, other, ok := u.askOpRef(stderr)
			if !ok {
				return iwjAnswer{}, "", "", false
			}
			if other {
				continue
			}
			return ans, "", "", true
		case "env":
			typed, ok := u.text(initWizWithHint("Environment variable name", "That variable must hold the key whenever code-forge runs."), "")
			if !ok {
				return iwjAnswer{}, "", "", false
			}
			name := jsTrim(typed)
			if !iwjEnvName.MatchString(name) {
				return iwjAnswer{}, "not an environment variable name", "", true
			}
			return iwjAnswer{ref: "env:" + name, source: "env"}, "", "", true
		case "paste":
			typed, ok := u.password(initWizWithHint("Jev key (input hidden)", "The key goes to the OS key store; the config keeps only a reference."))
			if !ok {
				return iwjAnswer{}, "", "", false
			}
			if typed == "" {
				return iwjAnswer{source: "none"}, "", "", true
			}
			if failure := iwjStorePut(home, typed); failure != "" {
				return iwjAnswer{secret: typed}, "", failure, true
			}
			return iwjAnswer{ref: "user", source: "keychain", secret: typed}, "", "", true
		}
		return iwjAnswer{source: "none"}, "", "", true
	}
}

// iwjStorePut is store.put('jev', value, {source: 'user', exp: null}) on
// the default store: the writer (the keychain through `security`, or the
// file store with CODE_FORGE_KEY_BACKEND=file or no keychain) gets
// {"v":…,"exp":null}, then the index row {source, backend, exp}. It returns
// the thrown error's message, "" on success.
func iwjStorePut(home, value string) string {
	entry, _ := ledger.Stringify(ledger.Object{{Key: "v", Value: value}, {Key: "exp", Value: nil}}, false)
	backend := "file"
	kc, kcOK := kcStore{}, false
	if os.Getenv("CODE_FORGE_KEY_BACKEND") != "file" {
		kc, kcOK = kcOpen()
	}
	if kcOK {
		backend = kc.name
		if failure := iwjKcSet(kc, "jev", string(entry)); failure != "" {
			return failure
		}
	} else if err := writePrivateFile(filepath.Join(home, ".code-forge", "store", "jev.key"), string(entry)); err != nil {
		return err.Error()
	}
	err := updateKeyIndex(home, func(index *ledger.Object) {
		index.Set("jev", ledger.Object{{Key: "source", Value: "user"}, {Key: "backend", Value: backend}, {Key: "exp", Value: nil}})
	})
	if err != nil {
		return err.Error()
	}
	return ""
}

// iwjKcSet is security-cli.mjs set with its error messages: the entry in on
// stdin twice, read back and compared (a mismatch deletes the item).
func iwjKcSet(kc kcStore, name, entry string) string {
	exit := func(res childResult) string {
		if res.code == nil {
			return "null"
		}
		return strconv.Itoa(*res.code)
	}
	if strings.ContainsAny(entry, "\r\n") {
		return "security-cli: a value containing a line break cannot be written through stdin"
	}
	input := entry + "\n" + entry + "\n"
	if res := kcExec(&input, "add-generic-password", "-U", "-s", kcService, "-a", name, "-w"); !res.ok {
		return "security-cli: add-generic-password failed (exit " + exit(res) + ")"
	}
	res := kcExec(nil, "find-generic-password", "-s", kcService, "-a", name, "-w")
	if res.timedOut || res.code == nil || !res.ok && *res.code != kcNotFound {
		return "security-cli: find-generic-password failed (exit " + exit(res) + ")"
	}
	if got := strings.TrimSuffix(nodeUTF8(res.stdout), "\n"); !res.ok || got != entry {
		_, _ = kc.delete(name)
		return "security-cli: add-generic-password stored value mismatch"
	}
	return ""
}
