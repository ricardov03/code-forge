package releasecheck

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// NpmLsResult sorts what `npm ls --json` reported (B778).
type NpmLsResult struct {
	// Parsed is false when the output was not a JSON object.
	Parsed bool
	// Missing and Invalid are real problems (`npm install` fixes them).
	Missing, Invalid []string
	// Extraneous are installed packages no package.json asks for (`npm prune` removes them).
	Extraneous []string
	// Ignored are the project's own platform packages at an older version than package.json
	// pins (the release bumps the pins before those versions exist on npm) or not installed.
	Ignored []string
	// Other are problems npm listed that no dependency entry explains.
	Other []string
}

// OK is true when nothing but ignored own platform packages was found.
func (r NpmLsResult) OK() bool {
	return r.Parsed && len(r.Missing)+len(r.Invalid)+len(r.Extraneous)+len(r.Other) == 0
}

// Summary is one line: what failed, or that npm ls is clean (and what it ignored).
func (r NpmLsResult) Summary() string {
	if !r.Parsed {
		return "npm ls --json printed no JSON"
	}
	var parts []string
	add := func(label string, items []string) {
		if len(items) > 0 {
			parts = append(parts, strconv.Itoa(len(items))+" "+label+": "+strings.Join(items, ", "))
		}
	}
	add("missing", r.Missing)
	add("invalid", r.Invalid)
	add("extraneous", r.Extraneous)
	add("other", r.Other)
	if len(parts) == 0 {
		s := "npm ls: 0 invalid, 0 missing, 0 extraneous"
		if len(r.Ignored) > 0 {
			s += " (ignored the own platform packages at the previous version: " + strings.Join(r.Ignored, ", ") + ")"
		}
		return s
	}
	return "npm ls: " + strings.Join(parts, "; ")
}

// Fix names the command that clears the problems: npm prune when only extraneous packages fail.
func (r NpmLsResult) Fix() string {
	switch {
	case !r.Parsed:
		return "run npm install, then npm ls"
	case len(r.Missing)+len(r.Invalid)+len(r.Other) == 0 && len(r.Extraneous) > 0:
		return "npm prune (removes the extraneous packages)"
	case len(r.Extraneous) > 0:
		return "npm install, then npm prune (removes the extraneous packages)"
	case len(r.Missing)+len(r.Invalid)+len(r.Other) > 0:
		return "npm install"
	}
	return ""
}

type lsNode struct {
	Version      string             `json:"version"`
	Missing      json.RawMessage    `json:"missing"`
	Invalid      json.RawMessage    `json:"invalid"`
	Extraneous   json.RawMessage    `json:"extraneous"`
	Dependencies map[string]*lsNode `json:"dependencies"`
	Problems     []json.RawMessage  `json:"problems"`
}

// truthy is JavaScript truthiness for a raw JSON value.
func truthy(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "", "null", "false", "0", `""`:
		return false
	}
	return true
}

// NpmLs classifies `npm ls --json` output for the package name with these optionalDependencies.
// An own platform package (<name>-<suffix> in optionalDependencies) that is missing, or installed
// at a lower version than the pin, is ignored: the release bumps the pins first.
func NpmLs(stdout, name string, optional map[string]string) NpmLsResult {
	var root lsNode
	var res NpmLsResult
	if err := json.Unmarshal([]byte(stdout), &root); err != nil {
		return res
	}
	res.Parsed = true
	own := map[string]bool{}
	for _, dep := range OwnPlatformPackages(name, optional) {
		own[dep] = true
	}
	explained := map[string]bool{}
	var walk func(n *lsNode, at string, top bool)
	walk = func(n *lsNode, at string, top bool) {
		names := make([]string, 0, len(n.Dependencies))
		for dep := range n.Dependencies {
			names = append(names, dep)
		}
		sort.Strings(names)
		for _, dep := range names {
			info := n.Dependencies[dep]
			if info == nil {
				continue
			}
			label := at + dep
			ownOld := top && own[dep] && (truthy(info.Missing) || info.Version == "" || older(info.Version, optional[dep]))
			switch {
			case ownOld && (truthy(info.Missing) || truthy(info.Invalid)):
				v := info.Version
				if v == "" {
					v = "not installed"
				}
				res.Ignored = append(res.Ignored, dep+"@"+v)
				explained[dep] = true
			case truthy(info.Missing):
				res.Missing = append(res.Missing, label)
				explained[dep] = true
			case truthy(info.Invalid):
				res.Invalid = append(res.Invalid, label+"@"+info.Version)
				explained[dep] = true
			}
			if truthy(info.Extraneous) {
				res.Extraneous = append(res.Extraneous, label+"@"+info.Version)
				explained[dep] = true
			}
			walk(info, label+" > ", false)
		}
	}
	walk(&root, "", true)
	for _, p := range root.Problems {
		var s string
		if json.Unmarshal(p, &s) != nil {
			s = string(p)
		}
		if !problemExplained(s, explained) {
			res.Other = append(res.Other, s)
		}
	}
	return res
}

// problemExplained: npm's problem lines name the package ("extraneous: foo@1.0.0 /path",
// "invalid: @scope/x@1.0.0 /path", "missing: bar@^2.0.0, required by x@1.0.0").
func problemExplained(p string, explained map[string]bool) bool {
	_, rest, ok := strings.Cut(p, ": ")
	if !ok {
		return false
	}
	spec := strings.Fields(rest)
	if len(spec) == 0 {
		return false
	}
	s := strings.TrimSuffix(spec[0], ",")
	at := strings.LastIndex(s, "@")
	if at > 0 {
		s = s[:at]
	}
	return explained[s]
}
