package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

func readOrdered(t *testing.T, name string) ledger.Object {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	o, err := ledger.DecodeObject(data)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func verdictJSON(t *testing.T, v Verdict) string {
	t.Helper()
	o := ledger.Object{{Key: "ok", Value: v.OK}}
	if v.OK {
		o.Set("review", v.Review)
		o.Set("tokens_out", v.TokensOut)
		if v.PassedCoerced {
			o.Set("passed_coerced", true)
		}
	} else {
		o.Set("reason", v.Reason)
		o.Set("detail", v.Detail)
	}
	b, err := ledger.Stringify(o, false)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestValidateReviewMatchesNode: the stub guard gives Node's verdict, reason,
// detail and coerced answer on every fixture session.
func TestValidateReviewMatchesNode(t *testing.T) {
	fx := readOrdered(t, "validate_fixture.json")
	golden := readOrdered(t, "validate_golden.json").Get("validate").(ledger.Object)
	cases := fx.Get("validate").([]any)
	if len(cases) != len(golden) {
		t.Fatalf("cases %d, golden %d", len(cases), len(golden))
	}
	for _, raw := range cases {
		c := raw.(ledger.Object)
		name := c.Get("name").(string)
		t.Run(name, func(t *testing.T) {
			session, _ := c.Get("session").(ledger.Object)
			var hunks []string
			for _, h := range c.Get("hunks").([]any) {
				hunks = append(hunks, h.(string))
			}
			got := verdictJSON(t, ValidateReview(session, hunks, int(c.Get("min").(float64))))
			if want := golden.Get(name).(string); got != want {
				t.Errorf("got  %s\nNode %s", got, want)
			}
		})
	}
}

// TestTruncateReviewFieldsMatchesNode: cuts, truncated_fields and the restored
// answer equal Node's on every fixture result.
func TestTruncateReviewFieldsMatchesNode(t *testing.T) {
	fx := readOrdered(t, "validate_fixture.json")
	golden := readOrdered(t, "validate_golden.json").Get("truncate").(ledger.Object)
	for _, raw := range fx.Get("truncate").([]any) {
		c := raw.(ledger.Object)
		name := c.Get("name").(string)
		t.Run(name, func(t *testing.T) {
			res, _ := TruncateReviewFields(c.Get("res").(ledger.Object))
			b, err := ledger.Stringify(res, false)
			if err != nil {
				t.Fatal(err)
			}
			if want := golden.Get(name).(string); string(b) != want {
				t.Errorf("got  %s\nNode %s", b, want)
			}
		})
	}
}

// TestMinTokensOut: only a positive integer overrides the default floor.
func TestMinTokensOut(t *testing.T) {
	for _, c := range []struct {
		cfg  string
		want int
	}{{`{}`, 120}, {`{"review":{"min_tokens_out":80}}`, 80}, {`{"review":{"min_tokens_out":0}}`, 120}, {`{"review":{"min_tokens_out":1.5}}`, 120}, {`{"review":{"min_tokens_out":"90"}}`, 120}} {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(c.cfg), &cfg); err != nil {
			t.Fatal(err)
		}
		if got := MinTokensOut(cfg); got != c.want {
			t.Errorf("%s: got %d, want %d", c.cfg, got, c.want)
		}
	}
}
