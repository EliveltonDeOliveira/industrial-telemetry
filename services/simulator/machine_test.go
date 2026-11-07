package main

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

var t0 = time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)

// noStops never stops on its own, so tests can check exact values.
var noStops = profile{cycle: 2 * time.Second, stopEvery: 1_000_000 * time.Hour, tempC: 50, vibration: 0.5}

// newTestMachine uses a fixed seed, so every run produces the same readings.
func newTestMachine(p profile) *machine {
	return newMachine("maq-01", p, rand.New(rand.NewPCG(1, 2)), t0)
}

// runFor steps m once per second for d, starting one second after t0.
func runFor(m *machine, d time.Duration) []contract.Telemetry {
	var readings []contract.Telemetry
	for s := time.Second; s <= d; s += time.Second {
		readings = append(readings, m.step(t0.Add(s)))
	}
	return readings
}

func TestReadingsAreValidAndConsistent(t *testing.T) {
	for i, p := range profiles {
		readings := runFor(newMachine("maq-01", p, rand.New(rand.NewPCG(uint64(i), 0)), t0), 8*time.Hour)
		stopped := 0
		for j, r := range readings {
			if err := r.Validate(); err != nil {
				t.Fatalf("profile %d, reading %d: %v", i, j, err)
			}
			if r.Seq != uint64(j) || r.BootID != readings[0].BootID {
				t.Fatalf("profile %d, reading %d: seq %d boot_id %d, want seq %d boot_id %d", i, j, r.Seq, r.BootID, j, readings[0].BootID)
			}
			if j > 0 {
				prev := readings[j-1]
				if r.GoodTotal < prev.GoodTotal || r.ScrapTotal < prev.ScrapTotal || !r.TS.After(prev.TS) {
					t.Fatalf("profile %d, reading %d: counters or ts went back: %+v after %+v", i, j, r, prev)
				}
			}
			if r.State == contract.StateStopped {
				stopped++
			}
		}
		last := readings[len(readings)-1]
		if stopped == 0 || last.GoodTotal == 0 || last.ScrapTotal == 0 {
			t.Errorf("profile %d: over 8 hours want some stops, good and scrap, got %d stopped readings, %d good, %d scrap",
				i, stopped, last.GoodTotal, last.ScrapTotal)
		}
	}
}

func TestPiecesFollowCycleTime(t *testing.T) {
	readings := runFor(newTestMachine(noStops), 10*time.Minute)
	last := readings[len(readings)-1]
	// 600 seconds running at 2 seconds per piece.
	if got := last.GoodTotal + last.ScrapTotal; got != 300 {
		t.Errorf("pieces after 10 minutes = %d, want 300", got)
	}
}

func TestFaults(t *testing.T) {
	tests := []struct {
		fault  contract.AlertType
		during func(contract.Telemetry) bool // reading 2 minutes into the fault
		after  func(contract.Telemetry) bool // reading 30 minutes after the fault ended
	}{
		{
			fault:  contract.AlertHighTemperature,
			during: func(r contract.Telemetry) bool { return r.TemperatureC > noStops.tempC+10 },
			after:  func(r contract.Telemetry) bool { return r.TemperatureC < noStops.tempC+2 },
		},
		{
			fault:  contract.AlertHighVibration,
			during: func(r contract.Telemetry) bool { return r.VibrationRMS > 2*noStops.vibration },
			after:  func(r contract.Telemetry) bool { return r.VibrationRMS < 1.5*noStops.vibration },
		},
		{
			fault:  contract.AlertLongStop,
			during: func(r contract.Telemetry) bool { return r.State == contract.StateStopped },
			after:  func(r contract.Telemetry) bool { return r.State == contract.StateRunning },
		},
	}
	for _, tt := range tests {
		t.Run(string(tt.fault), func(t *testing.T) {
			m := newTestMachine(noStops)
			if _, err := m.inject(tt.fault, 5*time.Minute, t0); err != nil {
				t.Fatal(err)
			}
			readings := runFor(m, 35*time.Minute)
			if r := readings[2*60-1]; !tt.during(r) {
				t.Errorf("2 minutes into the fault, unexpected reading %+v", r)
			}
			if r := readings[len(readings)-1]; !tt.after(r) {
				t.Errorf("30 minutes after the fault, unexpected reading %+v", r)
			}
		})
	}
}

func TestLongStopMakesNoPieces(t *testing.T) {
	m := newTestMachine(noStops)
	if _, err := m.inject(contract.AlertLongStop, 5*time.Minute, t0); err != nil {
		t.Fatal(err)
	}
	readings := runFor(m, 5*time.Minute)
	first, last := readings[0], readings[len(readings)-2] // the last one is at the end of the stop
	if first.GoodTotal+first.ScrapTotal != last.GoodTotal+last.ScrapTotal {
		t.Errorf("pieces changed during the stop: %+v, then %+v", first, last)
	}
}

func TestInjectRejectsUnknownType(t *testing.T) {
	if _, err := newTestMachine(noStops).inject("incendio", time.Minute, t0); err == nil {
		t.Error("inject accepted an unknown fault type")
	}
}
