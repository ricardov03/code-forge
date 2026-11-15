package queue

import (
	"testing"

	"github.com/ricardov03/code-forge/native/ticket"
)

// TestB704AnswersIDWithLoneSurrogateMatchesNode: the want value is Node's
// answersTicketId({run:"r1",block:"B1",file:"a\ud83d.md",content_hash:"h",
// tree:"w\udc00",by_file:"b\ud800",by_hash:"bh",answers:["F2","F1"]});
// update(string) hashes each lone surrogate as U+FFFD.
func TestB704AnswersIDWithLoneSurrogateMatchesNode(t *testing.T) {
	tree := "w\xed\xb0\x80"
	p := ticket.Parts{Run: "r1", Block: "B1", File: "a\xed\xa0\xbd.md", ContentHash: "h", Tree: &tree}
	if got := AnswersID(p, "b\xed\xa0\x80", "bh", []string{"F2", "F1"}); got != "e7c6a2dd4def4ef5261e5a3c" {
		t.Fatalf("AnswersID %s, want Node's e7c6a2dd4def4ef5261e5a3c", got)
	}
}
