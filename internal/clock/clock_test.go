package clock

import (
	"sync"
	"testing"
	"time"
)

func TestManualClockAdvanceSetAndConcurrentRead(t *testing.T) {
	base := time.Date(2026, 8, 24, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	manual := NewManual(base)
	if got := manual.Now(); !got.Equal(base.UTC()) || got.Location() != time.UTC {
		t.Fatalf("initial now = %v", got)
	}
	if got := manual.Advance(90 * time.Minute); !got.Equal(base.Add(90 * time.Minute)) {
		t.Fatalf("advanced now = %v", got)
	}
	manual.Set(base.Add(24 * time.Hour))
	var group sync.WaitGroup
	for index := 0; index < 20; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for read := 0; read < 100; read++ {
				if got := manual.Now(); !got.Equal(base.Add(24 * time.Hour)) {
					t.Errorf("concurrent read = %v", got)
					return
				}
			}
		}()
	}
	group.Wait()
}

func TestRealClockReturnsUTCNearCurrentTime(t *testing.T) {
	before := time.Now().UTC()
	got := (Real{}).Now()
	after := time.Now().UTC()
	if got.Location() != time.UTC {
		t.Fatalf("location = %v", got.Location())
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("real time %v not between %v and %v", got, before, after)
	}
}
