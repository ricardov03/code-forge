package runstatus

import "github.com/ricardov03/code-forge/native/ledger"

// RunSpend is Node's runSpend (src/ledger/spend.mjs): the rows whose run is
// id, spend rows only (session and session.background, never shadow), summed
// in row order and rounded once to 4 decimals; unknown counts the rows whose
// price is unknown. ok is false for a row that is not an object (Node throws).
func RunSpend(rows []ledger.Row, id string) (usd float64, unknown int, ok bool) {
	sum := 0.0
	for _, value := range rows {
		row, isObject := value.(map[string]any)
		if !isObject {
			return 0, 0, false
		}
		if row["run"] != id {
			continue
		}
		event := str(row, "event")
		if event != "session" && event != "session.background" || row["source"] == "shadow" {
			continue
		}
		cost, known := rowUSD(row)
		if known {
			sum += cost
		} else {
			unknown++
		}
	}
	return round(sum), unknown, true
}

// RowUSD is Node's rowUsd for an object row: its dollars, or false when the
// price is unknown.
func RowUSD(row map[string]any) (float64, bool) { return rowUSD(row) }

// RoundUSD is Node's roundUsd: Math.round(n * 10000) / 10000.
func RoundUSD(n float64) float64 { return round(n) }
