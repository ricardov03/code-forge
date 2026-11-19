package state

import (
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B829: a run record that grows past a bound is seen early. Every save of a
// record over caps.run_record_warn_mb (default 2 MB, 0 turns it off) writes
// one WARN line naming the largest keys. The save itself never fails on
// size, and a large record still reads.

// DefaultRecordWarnMB is caps.run_record_warn_mb when the run's config does
// not set it.
const DefaultRecordWarnMB = 2.0

// RecordWarnOut is where the size WARN goes (stderr; tests swap it).
var RecordWarnOut io.Writer = os.Stderr

const recordMB = 1 << 20

// RecordWarnMB is caps.run_record_warn_mb of the config the run uses now
// (config.snapshots[config.hash]); the default when it is not a number >= 0.
func RecordWarnMB(record ledger.Object) float64 {
	cfg, _ := record.Get("config").(ledger.Object)
	hash, _ := cfg.Get("hash").(string)
	snapshots, _ := cfg.Get("snapshots").(ledger.Object)
	snapshot, _ := snapshots.Get(hash).(ledger.Object)
	caps, _ := snapshot.Get("caps").(ledger.Object)
	if mb, ok := sizeNumber(caps.Get("run_record_warn_mb")); ok && mb >= 0 {
		return mb
	}
	return DefaultRecordWarnMB
}

func sizeNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// RecordSizeWarning is the WARN line for a record of size bytes, "" when it
// is within the bound (or the bound is 0).
func RecordSizeWarning(runID string, record ledger.Object, size int) string {
	mb := RecordWarnMB(record)
	if mb == 0 || float64(size) <= mb*recordMB {
		return ""
	}
	return fmt.Sprintf("WARN run record %s is %s, over caps.run_record_warn_mb (%s); largest keys: %s",
		runID, sizeText(size), sizeText(int(mb*recordMB)), strings.Join(largestKeys(record), ", "))
}

type keySize struct {
	path string
	size int
}

// largestKeys names the three largest top-level keys by their JSON size,
// and inside the largest one, its own largest key (blocks.B3 and the like).
func largestKeys(record ledger.Object) []string {
	sizes := childSizes("", record)
	out := []string{}
	for i, k := range sizes {
		if i == 3 {
			break
		}
		out = append(out, k.path+" "+sizeText(k.size))
		if i == 0 {
			if inner, ok := record.Get(k.path).(ledger.Object); ok {
				if nested := childSizes(k.path+".", inner); len(nested) > 0 {
					out = append(out, nested[0].path+" "+sizeText(nested[0].size))
				}
			}
		}
	}
	return out
}

func childSizes(prefix string, obj ledger.Object) []keySize {
	sizes := make([]keySize, 0, len(obj))
	for _, f := range obj {
		data, err := ledger.Stringify(f.Value, true)
		if err != nil {
			continue
		}
		sizes = append(sizes, keySize{prefix + f.Key, len(data)})
	}
	sort.SliceStable(sizes, func(a, b int) bool { return sizes[a].size > sizes[b].size })
	return sizes
}

func sizeText(n int) string {
	switch {
	case n >= recordMB:
		return fmt.Sprintf("%.1f MB", float64(n)/recordMB)
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
