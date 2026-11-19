package queue

import (
	"os"
	"path/filepath"
	"testing"
)

// ResetTimings clears only the finish time of a finished ticket, keeps every
// other field in place, and leaves a queued ticket's bytes alone.
func TestB918ResetTimingsClearsTheFinishTime(t *testing.T) {
	dir := t.TempDir()
	id := "bb8e6124c7f942cbdc7a058f"
	finished := `{"ticket":"` + id + `","run":"r1","block":"B14","file":"app/Mseller/CredentialSet.php","content_hash":"deleted","enqueued_at":"2026-11-15T09:00:00.000Z","queued_at":"2026-11-15T09:00:00.000Z","started_at":"2026-11-18T10:00:00.000Z","finished_at":"2026-11-18T10:00:05.000Z","duration_ms":5000}` + "\n"
	file := filepath.Join(dir, id+".json")
	if err := os.WriteFile(file, []byte(finished), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ResetTimings(dir, id); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(file)
	want := `{"ticket":"` + id + `","run":"r1","block":"B14","file":"app/Mseller/CredentialSet.php","content_hash":"deleted","enqueued_at":"2026-11-15T09:00:00.000Z","queued_at":"2026-11-15T09:00:00.000Z","started_at":"2026-11-18T10:00:00.000Z","finished_at":null,"duration_ms":null}` + "\n"
	if string(got) != want {
		t.Fatalf("reset ticket\n%s\nwant\n%s", got, want)
	}
	queued := `{"ticket":"` + id + `", "finished_at":null}` + "\n"
	if err := os.WriteFile(file, []byte(queued), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ResetTimings(dir, id); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); string(got) != queued {
		t.Fatalf("a queued ticket was rewritten: %s", got)
	}
	if err := ResetTimings(dir, "aaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("missing ticket: %v", err)
	}
}
