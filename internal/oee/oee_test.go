package oee

import (
	"math"
	"testing"
	"time"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

const (
	run  = contract.StateRunning
	stop = contract.StateStopped
)

var start = time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)

// at builds a reading taken sec seconds after the start of the period.
func at(sec int, bootID uint32, state contract.State, good, scrap uint64) Reading {
	return Reading{BootID: bootID, TS: start.Add(time.Duration(sec) * time.Second), State: state, GoodTotal: good, ScrapTotal: scrap}
}

func TestCompute(t *testing.T) {
	tests := []struct {
		name        string
		readings    []Reading
		running     time.Duration
		good, scrap uint64
		a, p, q     float64 // availability, performance and quality
	}{
		{
			name:     "running the whole period at the ideal cycle",
			readings: []Reading{at(0, 7, run, 0, 0), at(40, 7, run, 10, 0), at(100, 7, run, 25, 0)},
			running:  100 * time.Second, good: 25,
			a: 1, p: 1, q: 1,
		},
		{
			name:     "a reading's state holds until the next reading",
			readings: []Reading{at(0, 7, run, 0, 0), at(60, 7, stop, 15, 0), at(100, 7, stop, 15, 0)},
			running:  60 * time.Second, good: 15,
			a: 0.6, p: 1, q: 1,
		},
		{
			name:     "starts again after a stop",
			readings: []Reading{at(0, 7, stop, 0, 0), at(20, 7, run, 0, 0), at(100, 7, run, 20, 0)},
			running:  80 * time.Second, good: 20,
			a: 0.8, p: 1, q: 1,
		},
		{
			name:     "slower than the ideal cycle",
			readings: []Reading{at(0, 7, run, 0, 0), at(100, 7, run, 20, 0)},
			running:  100 * time.Second, good: 20,
			a: 1, p: 0.8, q: 1,
		},
		{
			name:     "faster than the ideal cycle is limited to 1",
			readings: []Reading{at(0, 7, run, 0, 0), at(100, 7, run, 30, 0)},
			running:  100 * time.Second, good: 30,
			a: 1, p: 1, q: 1,
		},
		{
			name:     "scrap lowers quality",
			readings: []Reading{at(0, 7, run, 0, 0), at(100, 7, run, 20, 5)},
			running:  100 * time.Second, good: 20, scrap: 5,
			a: 1, p: 1, q: 0.8,
		},
		{
			// Boot 7 makes 9 good and 1 scrap. Boot 8 starts with 1 good already
			// counted and makes 8 good and 1 scrap more. The 20 s between boots
			// are not running time.
			name: "reboot restarts the counters under a new boot_id",
			readings: []Reading{
				at(0, 7, run, 100, 2), at(40, 7, run, 109, 3),
				at(60, 8, run, 1, 0), at(100, 8, run, 9, 1),
			},
			running: 80 * time.Second, good: 18, scrap: 2,
			a: 0.8, p: 1, q: 0.9,
		},
		{
			// Boot 8 already counts 5 pieces in its first reading, more than
			// boot 7 had. All 5 are new, not 5 - 2.
			name: "counters of a new boot_id are not compared with the old one",
			readings: []Reading{
				at(0, 7, run, 0, 0), at(40, 7, run, 2, 0),
				at(60, 8, run, 5, 0), at(100, 8, run, 13, 0),
			},
			running: 80 * time.Second, good: 15,
			a: 0.8, p: 0.75, q: 1,
		},
		{
			name:     "counter going down in the same boot_id restarts from zero",
			readings: []Reading{at(0, 7, run, 50, 0), at(50, 7, run, 3, 0), at(100, 7, run, 15, 0)},
			running:  100 * time.Second, good: 15,
			a: 1, p: 0.6, q: 1,
		},
		{
			// Counters are cumulative, so pieces made between lost readings
			// show up in the next one.
			name:     "lost readings in the same boot_id",
			readings: []Reading{at(0, 7, run, 0, 0), at(10, 7, run, 2, 0), at(100, 7, run, 25, 0)},
			running:  100 * time.Second, good: 25,
			a: 1, p: 1, q: 1,
		},
		{
			name:     "repeated reading changes nothing",
			readings: []Reading{at(0, 7, run, 0, 0), at(40, 7, run, 10, 0), at(40, 7, run, 10, 0), at(100, 7, run, 25, 0)},
			running:  100 * time.Second, good: 25,
			a: 1, p: 1, q: 1,
		},
		{
			name:     "no readings in part of the period is not running",
			readings: []Reading{at(50, 7, run, 0, 0), at(100, 7, run, 10, 0)},
			running:  50 * time.Second, good: 10,
			a: 0.5, p: 0.8, q: 1,
		},
		{
			name:     "running without making pieces",
			readings: []Reading{at(0, 7, run, 4, 0), at(100, 7, run, 4, 0)},
			running:  100 * time.Second, good: 0, scrap: 0,
			a: 1, p: 0, q: 0,
		},
		{
			name:     "stopped the whole period",
			readings: []Reading{at(0, 7, stop, 4, 1), at(100, 7, stop, 4, 1)},
		},
		{
			name:     "a single reading",
			readings: []Reading{at(0, 7, run, 4, 1)},
		},
		{
			name: "no readings",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Compute(tt.readings, 100*time.Second, 4*time.Second)
			want := Result{
				Planned: 100 * time.Second, Running: tt.running, Good: tt.good, Scrap: tt.scrap,
				Availability: tt.a, Performance: tt.p, Quality: tt.q, OEE: tt.a * tt.p * tt.q,
			}
			if !equal(got, want) {
				t.Errorf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

// equal compares results, allowing for float rounding in the factors.
func equal(a, b Result) bool {
	near := func(x, y float64) bool { return math.Abs(x-y) < 1e-9 }
	return a.Planned == b.Planned && a.Running == b.Running && a.Good == b.Good && a.Scrap == b.Scrap &&
		near(a.Availability, b.Availability) && near(a.Performance, b.Performance) &&
		near(a.Quality, b.Quality) && near(a.OEE, b.OEE)
}
