package ticket_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/ricardov03/code-forge/native/ticket"
)

func TestGolden(t *testing.T) {
	data, err := os.ReadFile("../../test/compat/golden/ticket.json")
	if err != nil {
		t.Fatal(err)
	}
	type identity struct {
		Name     string
		Parts    ticket.Parts
		Expected string
	}
	var kit struct {
		IDs    []identity
		Review []identity
		States []struct{ File, Expected string }
	}
	if err := json.Unmarshal(data, &kit); err != nil {
		t.Fatal(err)
	}
	if len(kit.IDs) != 4 || len(kit.Review) != 6 || len(kit.States) != 3 {
		t.Fatalf("ticket cases: got %d/%d/%d, want 4/6/3", len(kit.IDs), len(kit.Review), len(kit.States))
	}
	for _, c := range kit.IDs {
		if got := ticket.ID(c.Parts); got != c.Expected {
			t.Errorf("%s: ID got %s, want %s", c.Name, got, c.Expected)
		}
	}
	for i, c := range kit.Review {
		if got := ticket.ReviewID(c.Parts); got != c.Expected {
			t.Errorf("review %d: got %s, want %s", i, got, c.Expected)
		}
	}
	for _, c := range kit.States {
		if got := ticket.StateName(c.File); got != c.Expected {
			t.Errorf("%s: state name got %s, want %s", c.File, got, c.Expected)
		}
	}
}
