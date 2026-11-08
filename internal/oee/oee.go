// Package oee computes the Overall Equipment Effectiveness of a machine from its
// telemetry readings, following the rules in docs/adr/0009-regras-do-oee.md.
//
// OEE = availability × performance × quality, each factor limited to [0, 1].
package oee

import (
	"time"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

// Reading is the part of a telemetry reading the calculation uses.
type Reading struct {
	BootID     uint32
	TS         time.Time
	State      contract.State
	GoodTotal  uint64
	ScrapTotal uint64
}

// Result is the OEE of one machine over a period, with the values behind it.
type Result struct {
	Planned      time.Duration // length of the period
	Running      time.Duration // time the machine was running
	Good         uint64        // good pieces made in the period
	Scrap        uint64        // scrapped pieces made in the period
	Availability float64       // Running / Planned
	Performance  float64       // pieces × ideal cycle / Running
	Quality      float64       // Good / pieces
	OEE          float64
}

// Compute returns the OEE of one machine. readings are the machine's readings
// inside the period, sorted by ts; the first one is only the baseline for the
// next. planned is the length of the period and idealCycle the time one piece
// should take.
//
// A reading holds until the next one: the interval between them counts as
// running when the earlier reading is running and both share the boot_id.
func Compute(readings []Reading, planned, idealCycle time.Duration) Result {
	r := Result{Planned: planned}
	for i := 1; i < len(readings); i++ {
		prev, cur := readings[i-1], readings[i]
		sameBoot := cur.BootID == prev.BootID
		// Across a reboot the machine was off for some unknown part of the interval.
		if sameBoot && prev.State == contract.StateRunning {
			r.Running += cur.TS.Sub(prev.TS)
		}
		r.Good += increase(prev.GoodTotal, cur.GoodTotal, sameBoot)
		r.Scrap += increase(prev.ScrapTotal, cur.ScrapTotal, sameBoot)
	}
	pieces := r.Good + r.Scrap
	r.Availability = ratio(r.Running.Seconds(), planned.Seconds())
	r.Performance = ratio(float64(pieces)*idealCycle.Seconds(), r.Running.Seconds())
	r.Quality = ratio(float64(r.Good), float64(pieces))
	r.OEE = r.Availability * r.Performance * r.Quality
	return r
}

// increase is how much a cumulative counter grew between two readings. After a
// reboot, or if the value goes down, the counter restarted from zero, so its
// new value is the increase and pieces are never negative.
func increase(prev, cur uint64, sameBoot bool) uint64 {
	if !sameBoot || cur < prev {
		return cur
	}
	return cur - prev
}

// ratio divides and limits the result to [0, 1]. With nothing to divide by,
// such as no running time or no pieces, the factor is 0.
func ratio(num, den float64) float64 {
	if den <= 0 {
		return 0
	}
	return min(max(num/den, 0), 1)
}
