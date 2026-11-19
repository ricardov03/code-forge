package yamledit

import (
	"errors"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B781: the customer's config shape (issue 2026-11-18): comment lines,
// flow-mapping levels, keys, nested review: maps and review.routes as a
// block list of mappings. Set refuses it ("a collection inside a sequence
// item"); approve handed over (autopilot-approve-yaml-shape).
const spCustomer = `# code-forge config for the iOS app
version: 1
provider: anthropic

# Levels: who does what
levels:
  L0: { model: claude-haiku-4-5 }
  L1: { model: claude-sonnet-4-6, effort: medium }
  L2: { model: claude-opus-4-7, effort: high }
  L3: { model: claude-opus-4-7, effort: max }

# stored key: a 1Password reference, not a secret
keys:
  jev: op://vault/item/field

review:
  close_wait_s: 30 # seconds
  # second opinion
  second_provider: xai
  second_levels:
    L2:
      provider: xai
      model: grok-4.7
  # files over 400 lines go to xai L2
  routes:
    - min_lines: 401
      level: L2
      provider: xai
      model: grok-4.7

proof:
  # high tier: payments
  tiers:
    high:
      paths:
        - Sources/Payments/**
    # end of tiers

# trailing comment
`

func TestB781ModelRefusesCustomerShape(t *testing.T) {
	if _, err := Set(spCustomer, []string{"proof", "tiers"}, ledger.Object{}); !IsUnsupported(err) {
		t.Fatalf("Set err = %v; the model is expected to refuse this shape", err)
	}
}

func TestB781SpliceSetCases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		segs  []string
		value any
		old   string // the lines replaced (joined)
		new   string // the lines written in their place
	}{
		{"proof.tiers {}", []string{"proof", "tiers"}, ledger.Object{},
			"  tiers:\n    high:\n      paths:\n        - Sources/Payments/**\n", "  tiers: {}\n"},
		{"scalar keeps its comment", []string{"review", "close_wait_s"}, float64(60),
			"  close_wait_s: 30 # seconds\n", "  close_wait_s: 60 # seconds\n"},
		{"inside a flow mapping", []string{"levels", "L2", "effort"}, "max",
			"  L2: { model: claude-opus-4-7, effort: high }\n", "  L2:\n    model: claude-opus-4-7\n    effort: max\n"},
		{"the routes list itself", []string{"review", "routes"}, []any{ledger.Object{{Key: "min_lines", Value: float64(801)}, {Key: "model", Value: "grok-4.7"}}},
			"  routes:\n    - min_lines: 401\n      level: L2\n      provider: xai\n      model: grok-4.7\n", "  routes:\n    - min_lines: 801\n      model: grok-4.7\n"},
		{"a new key in a nested mapping", []string{"review", "max_rounds_per_file"}, float64(3),
			"      model: grok-4.7\n\nproof:\n", "      model: grok-4.7\n  max_rounds_per_file: 3\n\nproof:\n"},
		{"a new top-level parent", []string{"caps", "coders"}, float64(2),
			"    # end of tiers\n", "    # end of tiers\ncaps:\n  coders: 2\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SetOrSplice(spCustomer, tc.segs, tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(spCustomer, tc.old) {
				t.Fatalf("bad case: %q not in the sample", tc.old)
			}
			if want := strings.Replace(spCustomer, tc.old, tc.new, 1); got != want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// Approve then restore: Set {} then Set the old value back reads back as
// the start; a key added by approve is removed again, with the prune.
func TestB781SpliceRestoreRoundTrip(t *testing.T) {
	approved, err := SetOrSplice(spCustomer, []string{"proof", "tiers"}, ledger.Object{})
	if err != nil {
		t.Fatal(err)
	}
	old := ledger.Object{{Key: "high", Value: ledger.Object{{Key: "paths", Value: []any{"Sources/Payments/**"}}}}}
	back, err := SetOrSplice(approved, []string{"proof", "tiers"}, old)
	if err != nil {
		t.Fatal(err)
	}
	if back != spCustomer {
		t.Fatalf("restore:\n%s\nwant:\n%s", back, spCustomer)
	}
	added, err := SetOrSplice(spCustomer, []string{"caps", "coders"}, float64(2))
	if err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveOrSplice(added, []string{"caps", "coders"}, 1)
	if err != nil || removed != spCustomer {
		t.Fatalf("remove with prune: %v\n%s", err, removed)
	}
	kept, err := RemoveOrSplice(added, []string{"caps", "coders"}, 0)
	if err != nil || kept != strings.Replace(spCustomer, "    # end of tiers\n", "    # end of tiers\ncaps: {}\n", 1) {
		t.Fatalf("remove without prune: %v\n%s", err, kept)
	}
	sibling, err := RemoveOrSplice(spCustomer, []string{"review", "second_provider"}, 1)
	if err != nil || sibling != strings.Replace(spCustomer, "  second_provider: xai\n", "", 1) {
		t.Fatalf("remove one key of several: %v\n%s", err, sibling)
	}
	same, err := RemoveOrSplice(spCustomer, []string{"review", "absent"}, 1)
	if err != nil || same != spCustomer {
		t.Fatalf("remove of a missing key: %v", err)
	}
}

func TestB781SpliceRefusals(t *testing.T) {
	if _, err := SetOrSplice(spCustomer, []string{"provider", "x"}, "y"); !errors.Is(err, ErrNotMapping) {
		t.Fatalf("through a scalar: %v; want ErrNotMapping", err)
	}
	if _, err := SetOrSplice(spCustomer, []string{"review", "routes", "x"}, "y"); !IsUnsupported(err) {
		t.Fatalf("through a sequence: %v; want Unsupported", err)
	}
	anchored := spCustomer + "x: &a 1\ny: *a\n"
	if _, err := SetOrSplice(anchored, []string{"proof", "tiers"}, ledger.Object{}); !IsUnsupported(err) {
		t.Fatalf("anchors: %v; want Unsupported", err)
	}
	crlf := strings.ReplaceAll(spCustomer, "\n", "\r\n")
	if _, err := SetOrSplice(crlf, []string{"proof", "tiers"}, ledger.Object{}); !IsUnsupported(err) {
		t.Fatalf("CRLF: %v; want Unsupported", err)
	}
}

// The model still serves what it served: same bytes as Set.
func TestB781SpliceLeavesServedShapes(t *testing.T) {
	text := "# c\nreview:\n  close_wait_s: 30\n"
	want, err := Set(text, []string{"review", "close_wait_s"}, float64(5))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := SetOrSplice(text, []string{"review", "close_wait_s"}, float64(5)); err != nil || got != want {
		t.Fatalf("got %q %v; want %q", got, err, want)
	}
}
