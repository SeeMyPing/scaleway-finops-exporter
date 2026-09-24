package collector

import (
	"testing"
	"time"
)

func TestPeriodsCollector(t *testing.T) {
	t.Parallel()

	// Early on the 1st of January in Paris is still December 31st in UTC.
	paris := time.FixedZone("CET", 60*60)
	now := func() time.Time { return time.Date(2026, 1, 1, 0, 30, 0, 0, paris) }
	checkGolden(t, NewPeriods(now, 2), "periods")
}

func TestPeriodsCollectorDefaultClock(t *testing.T) {
	t.Parallel()

	c := NewPeriods(nil, 0)
	if c.now == nil {
		t.Fatal("NewPeriods(nil, ...) must default to time.Now")
	}
}
