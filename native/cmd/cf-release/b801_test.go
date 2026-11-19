package main

// B801: the command reference now carries each command's flags, examples,
// exit codes, JSON shape and related commands. The site still reads it with
// sdCommands: every verb keeps its summary as description and its usage, and
// every sub-command its own usage, as commands.json lists them.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestB801SiteReadsTheCommandReference(t *testing.T) {
	root, _ := filepath.Abs("../../..")
	md, err := os.ReadFile(filepath.Join(root, "docs", "reference", "commands.md"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "docs", "reference", "commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		Commands []struct {
			Name, Purpose, Usage string
		} `json:"commands"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	type site struct {
		description, usage string
		subs               map[string]string
	}
	parsed := map[string]site{}
	for _, item := range sdCommands(string(md)) {
		cmd := item.(*jsObject)
		s := site{description: cmd.vals["description"].(string), usage: cmd.vals["usage"].(string), subs: map[string]string{}}
		for _, sub := range cmd.vals["subcommands"].([]any) {
			o := sub.(*jsObject)
			s.subs[o.vals["name"].(string)] = o.vals["usage"].(string)
		}
		parsed[cmd.vals["name"].(string)] = s
	}
	verbs, subs := 0, 0
	for _, c := range ref.Commands {
		usage := strings.TrimRight(c.Usage, "\n")
		verb, sub, isSub := strings.Cut(c.Name, " ")
		got, ok := parsed[verb]
		if !ok {
			t.Errorf("%s: the site finds no section", verb)
			continue
		}
		if !isSub {
			verbs++
			if got.description+"." != c.Purpose || got.usage != usage {
				t.Errorf("%s: site description %q usage %q; want %q and the usage", c.Name, got.description, got.usage, c.Purpose)
			}
			continue
		}
		subs++
		if got.subs[sub] != usage {
			t.Errorf("%s: site usage %q, want %q", c.Name, got.subs[sub], usage)
		}
	}
	total := 0
	for _, s := range parsed {
		total += len(s.subs)
	}
	if verbs != len(parsed) || subs != total || verbs == 0 {
		t.Errorf("site sees %d verbs and %d sub-commands; the reference lists %d and %d", len(parsed), total, verbs, subs)
	}
}
