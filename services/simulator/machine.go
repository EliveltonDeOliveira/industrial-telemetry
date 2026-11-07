package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

// profile describes how a simulated machine behaves while healthy.
type profile struct {
	cycle     time.Duration // mean time per piece, a bit slower than ideal_cycle_s
	stopEvery time.Duration // mean running time between short stops
	tempC     float64       // temperature while running
	vibration float64       // vibration RMS in g while running
}

// profiles match maq-01 to maq-03 in 0002_seed_machines.sql (ideal_cycle_s 4, 6 and 2.5).
// Machine N uses profiles[(N-1) % len(profiles)].
var profiles = []profile{
	{cycle: 4500 * time.Millisecond, stopEvery: 15 * time.Minute, tempC: 48, vibration: 0.45},
	{cycle: 6600 * time.Millisecond, stopEvery: 8 * time.Minute, tempC: 62, vibration: 0.30},
	{cycle: 2800 * time.Millisecond, stopEvery: 25 * time.Minute, tempC: 40, vibration: 0.80},
}

const (
	ambientTempC    = 25.0
	thermalTau      = 3 * time.Minute // time constant of heating and cooling
	tempNoiseC      = 0.3
	stoppedVib      = 0.02
	vibrationNoise  = 0.05 // relative to the current level
	scrapRate       = 0.03
	minStop         = 20 * time.Second
	maxStop         = 2 * time.Minute // below the 5 minute long stop alert
	overheatC       = 35.0
	vibrationFactor = 4.0
)

// machine holds the state of one simulated machine. step and inject may be
// called from different goroutines.
type machine struct {
	mu  sync.Mutex
	id  string
	p   profile
	rng *rand.Rand

	bootID         uint32
	seq            uint64
	last           time.Time
	state          contract.State
	progress       float64 // fraction of the current piece
	good, scrap    uint64
	tempC          float64
	stopUntil      time.Time // stopped while now is before it, by a short stop or a parada_longa fault
	overheatUntil  time.Time
	vibrationUntil time.Time
}

func newMachine(id string, p profile, rng *rand.Rand, now time.Time) *machine {
	return &machine{
		id:     id,
		p:      p,
		rng:    rng,
		bootID: rng.Uint32(),
		last:   now,
		state:  contract.StateRunning,
		tempC:  p.tempC,
	}
}

// step advances the simulation to now and returns the reading for that instant.
func (m *machine) step(now time.Time) contract.Telemetry {
	m.mu.Lock()
	defer m.mu.Unlock()

	dt := now.Sub(m.last)
	m.last = now

	// Pieces made since the previous reading count only if it was running,
	// the same rule the OEE uses for running time.
	if m.state == contract.StateRunning {
		m.progress += dt.Seconds() / m.p.cycle.Seconds()
		for m.progress >= 1 {
			m.progress--
			if m.rng.Float64() < scrapRate {
				m.scrap++
			} else {
				m.good++
			}
		}
	}

	if !now.Before(m.stopUntil) && m.rng.Float64() < chance(dt, m.p.stopEvery) {
		m.stopUntil = now.Add(minStop + time.Duration(m.rng.Int64N(int64(maxStop-minStop))))
	}
	m.state = contract.StateRunning
	if now.Before(m.stopUntil) {
		m.state = contract.StateStopped
	}

	targetC := ambientTempC
	if m.state == contract.StateRunning {
		targetC = m.p.tempC
	}
	if now.Before(m.overheatUntil) {
		targetC = m.p.tempC + overheatC
	}
	// Exponential approach to the target, as a real machine heats and cools.
	m.tempC += (targetC - m.tempC) * (1 - math.Exp(-dt.Seconds()/thermalTau.Seconds()))

	vibration := stoppedVib
	if m.state == contract.StateRunning {
		vibration = m.p.vibration
		if now.Before(m.vibrationUntil) {
			vibration *= vibrationFactor
		}
	}

	t := contract.Telemetry{
		SchemaVersion: contract.SchemaVersion,
		MachineID:     m.id,
		BootID:        m.bootID,
		Seq:           m.seq,
		TS:            now.UTC().Truncate(time.Millisecond),
		State:         m.state,
		GoodTotal:     m.good,
		ScrapTotal:    m.scrap,
		TemperatureC:  round(clamp(m.tempC+m.rng.NormFloat64()*tempNoiseC, contract.MinTemperatureC, contract.MaxTemperatureC), 2),
		VibrationRMS:  round(clamp(vibration*(1+m.rng.NormFloat64()*vibrationNoise), 0, contract.MaxVibrationRMS), 3),
	}
	m.seq++
	return t
}

// inject starts a fault that lasts for d from now.
func (m *machine) inject(fault contract.AlertType, d time.Duration, now time.Time) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	until := now.Add(d)
	switch fault {
	case contract.AlertHighTemperature:
		m.overheatUntil = until
	case contract.AlertHighVibration:
		m.vibrationUntil = until
	case contract.AlertLongStop:
		m.stopUntil = until
	default:
		return time.Time{}, fmt.Errorf("type: %q is not one of [%s %s %s]", fault,
			contract.AlertHighTemperature, contract.AlertHighVibration, contract.AlertLongStop)
	}
	return until, nil
}

// chance returns the probability that an event with the given mean interval
// happens within dt.
func chance(dt, mean time.Duration) float64 {
	return 1 - math.Exp(-dt.Seconds()/mean.Seconds())
}

func clamp(v, lo, hi float64) float64 {
	return min(max(v, lo), hi)
}

func round(v float64, decimals int) float64 {
	p := math.Pow(10, float64(decimals))
	return math.Round(v*p) / p
}
