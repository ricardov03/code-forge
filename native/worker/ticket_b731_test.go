package worker

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ticket"
)

// B731: a ticket whose started_at (or finished_at, duration_ms) holds a lone
// surrogate escape is decided in Go. JSON.parse keeps the surrogate; the
// worker only tests started_at != null and writes {...ticket, ...timings},
// which replaces those fields, so Node's files carry no surrogate.
// testdata/b731_node_started_at.golden is what Node's createWorker wrote for
// the same tickets (b488 fixture and normalizer). A lone surrogate in any
// other field still hands the ticket to Node, with nothing written.

// b731Ticket writes a ticket body as JSON text (raw: the fields after
// enqueued_at, escapes kept) and returns its id.
func (fx *b488Fix) b731Ticket(t *testing.T, run, block, file, hash, raw string) string {
	t.Helper()
	id := ticket.ReviewID(ticket.Parts{Run: run, Block: block, File: file, ContentHash: hash})
	body := `{"ticket":"` + id + `","run":"` + run + `","block":"` + block + `","file":"` + file + `","content_hash":"` + hash + `","enqueued_at":"2026-11-11T09:00:00.000Z"` + raw + "}\n"
	writeJSON(t, filepath.Join(QueueDir(fx.root), id+".json"), body)
	return id
}

func TestB731StartedAtSurrogateMatchesNode(t *testing.T) {
	fx := newB488Fix(t)
	real := sha("hello\n")
	ids := []string{
		fx.b731Ticket(t, "r1", "B1", "a.txt", strings.Repeat("0", 64), `,"started_at":"\ud800"`),
		fx.b731Ticket(t, "r2", "B1", "a.txt", real, `,"started_at":"\udfff","finished_at":"x\ud800","duration_ms":null`),
		fx.b731Ticket(t, "r1", "B1", "gone.txt", strings.Repeat("1", 64), `,"started_at":{"\ud800":["\udc00"]}`),
	}
	retry := fx.b731Ticket(t, "r1", "B2", "a.txt", real, `,"started_at":"\ud83d"`)
	clock := fixedNow
	w := fx.runner(&clock)
	run := func(id string) {
		t.Helper()
		n, poison, err := w.RunTicket(id)
		if err != nil || n != 1 || poison {
			t.Fatalf("RunTicket(%s) = %d, %v, %v; want 1, false, nil", id, n, poison, err)
		}
	}
	for _, id := range ids {
		run(id)
	}
	// a started_at that is not null makes this attempt a retry: with no
	// config snapshot it is unavailable, never reviewed
	fx.record(t, `{"run_id":"r1","project":"unit","workspace":"`+fx.root+`","status":"active","config":{"hash":"`+strings.Repeat("f", 64)+`","snapshots":{}}}`)
	run(retry)
	ids = append(ids, retry)

	ledgerText := readFile(t, filepath.Join(fx.home, ".code-forge", "ledger", "unit.jsonl"))
	raw := fx.files(t, ids) + ledgerText
	for _, bad := range []string{`\ud8`, `\udc`, `\udf`, "�"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("Go kept %q in a written file:\n%s", bad, raw)
		}
	}
	got := b488Normalize(raw)
	want := readFile(t, filepath.Join("testdata", "b731_node_started_at.golden"))
	if got != want {
		gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
		for i := range wl {
			if i >= len(gl) || gl[i] != wl[i] {
				g := "<missing>"
				if i < len(gl) {
					g = gl[i]
				}
				t.Fatalf("line %d differs from Node\n got: %s\nwant: %s", i+1, g, wl[i])
			}
		}
		t.Fatalf("Go wrote %d lines, Node %d", len(gl), len(wl))
	}
}

// TestB731SurrogateElsewhereMatchesNode: since B704 the worker reads a
// ticket with JSON.parse's lone surrogates (WTF-8), so a surrogate in
// queued_at, another field or a key runs in Go too. Each
// testdata/b731_node_elsewhere_<case>.golden is what Node's createWorker
// wrote for the same ticket (b488 fixture and normalizer).
func TestB731SurrogateElsewhereMatchesNode(t *testing.T) {
	for name, raw := range map[string]string{
		"queued_at": `,"queued_at":"\ud800"`,
		"extra":     `,"note":"\ud800","started_at":"\ud800"`,
		"key":       `,"\ud800":1`,
	} {
		t.Run(name, func(t *testing.T) {
			fx := newB488Fix(t)
			id := fx.b731Ticket(t, "r1", "B1", "a.txt", strings.Repeat("0", 64), raw)
			clock := fixedNow
			n, poison, err := fx.runner(&clock).RunTicket(id)
			if err != nil || n != 1 || poison {
				t.Fatalf("RunTicket = %d, %v, %v; want 1, false, nil", n, poison, err)
			}
			got := b488Normalize(fx.files(t, []string{id}) + readFile(t, filepath.Join(fx.home, ".code-forge", "ledger", "unit.jsonl")))
			want := readFile(t, filepath.Join("testdata", "b731_node_elsewhere_"+name+".golden"))
			if got != want {
				t.Fatalf("Go wrote:\n%s\nNode wrote:\n%s", got, want)
			}
		})
	}
}
