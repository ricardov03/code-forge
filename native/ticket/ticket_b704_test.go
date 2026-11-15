package ticket_test

import (
	"testing"

	"github.com/ricardov03/code-forge/native/ticket"
)

// TestB704IDWithLoneSurrogateMatchesNode: createHash().update(string) hashes
// a lone surrogate as U+FFFD, so a ticket whose file or tree holds one (WTF-8
// here, as JSON.parse gives it) gets Node's id. The want value is Node's
// ticketId({run:"r1",block:"B1",file:"a\ud83d.md",content_hash:"h",tree:"w\udc00"}),
// the same id Node gives with U+FFFD in place of each surrogate.
func TestB704IDWithLoneSurrogateMatchesNode(t *testing.T) {
	tree := "w\xed\xb0\x80"
	p := ticket.Parts{Run: "r1", Block: "B1", File: "a\xed\xa0\xbd.md", ContentHash: "h", Tree: &tree}
	if got := ticket.ID(p); got != "1d0ca322a6da79216ba76dc6" {
		t.Fatalf("ID %s, want Node's 1d0ca322a6da79216ba76dc6", got)
	}
}
