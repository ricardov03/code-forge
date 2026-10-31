package sign_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/ricardov03/code-forge/native/sign"
)

func TestGolden(t *testing.T) {
	data, err := os.ReadFile("../../test/compat/golden/sign.json")
	if err != nil {
		t.Fatal(err)
	}
	var kit struct {
		KeyHex   string   `json:"key_hex"`
		Unsigned []string `json:"unsigned_fields"`
		Cases    []struct {
			Row    map[string]any
			MAC    string
			Signed map[string]any
		}
		Verify []struct {
			Name     string
			Row      map[string]any
			Expected sign.Result
		}
	}
	if err := json.Unmarshal(data, &kit); err != nil {
		t.Fatal(err)
	}
	key, err := hex.DecodeString(kit.KeyHex)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sign.UnsignedFields(), kit.Unsigned) {
		t.Fatalf("unsigned fields: got %v, want %v", sign.UnsignedFields(), kit.Unsigned)
	}
	if len(kit.Cases) != 3 || len(kit.Verify) != 8 {
		t.Fatalf("cases: got %d signing/%d verification, want 3/8", len(kit.Cases), len(kit.Verify))
	}
	for _, c := range kit.Cases {
		t.Run(c.Row["event"].(string), func(t *testing.T) {
			before, _ := json.Marshal(c.Row)
			mac, err := sign.MACFor(c.Row, key)
			if err != nil {
				t.Fatal(err)
			}
			if mac != c.MAC {
				t.Errorf("MAC: got %s, want %s", mac, c.MAC)
			}
			row, err := sign.SignRow(c.Row, key)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(row, c.Signed) {
				t.Errorf("signed row: got %#v, want %#v", row, c.Signed)
			}
			after, _ := json.Marshal(c.Row)
			if string(before) != string(after) {
				t.Fatal("signing mutated its input row")
			}
		})
	}
	for _, c := range kit.Verify {
		t.Run(c.Name, func(t *testing.T) {
			got, err := sign.VerifyRow(c.Row, key)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.Expected {
				t.Fatalf("verification: got %+v, want %+v", got, c.Expected)
			}
		})
	}
}
