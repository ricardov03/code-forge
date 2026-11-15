package queue

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// TestB720V8SortMatchesNode replays arrays Node sorted with a logging
// comparator (testdata/b720_sort.js) and checks the Go port calls the
// comparator with the same pairs in the same order and ends in the same order.
func TestB720V8SortMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/b720_sort.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Values []int `json:"values"`
		Calls  int   `json:"calls"`
		Hash   int64 `json:"hash"`
		Order  []int `json:"order"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	type item struct{ x, id int }
	for ci, c := range cases {
		items := make([]item, len(c.Values))
		for i, x := range c.Values {
			items[i] = item{x, i}
		}
		var h int64
		calls := 0
		err := v8Sort(items, func(a, b item) (int, error) {
			calls++
			h = (h*31 + int64(a.id)*1009 + int64(b.id)) % 1000000007
			return a.x - b.x, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if calls != c.Calls || h != c.Hash {
			t.Fatalf("case %d (n=%d): calls %d hash %d, Node calls %d hash %d", ci, len(c.Values), calls, h, c.Calls, c.Hash)
		}
		for i, it := range items {
			if it.id != c.Order[i] {
				t.Fatalf("case %d: order differs at %d", ci, i)
			}
		}
	}
}

// TestB720V8SortStopsOnError checks a comparator error stops the sort at the
// call that raised it, as a JS throw does.
func TestB720V8SortStopsOnError(t *testing.T) {
	stop := errors.New("stop")
	arr := []int{5, 4, 3, 2, 1, 0}
	calls := 0
	err := v8Sort(arr, func(a, b int) (int, error) {
		calls++
		if calls == 3 {
			return 0, stop
		}
		return a - b, nil
	})
	if !errors.Is(err, stop) || calls != 3 {
		t.Fatalf("err %v calls %d", err, calls)
	}
}
