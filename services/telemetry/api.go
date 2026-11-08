package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/httpjson"
	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/oee"
)

const (
	defaultPeriod = time.Hour
	// maxPeriod bounds the readings loaded per request: a week at one reading
	// per second is about 600 thousand (ADR 0009).
	maxPeriod    = 7 * 24 * time.Hour
	queryTimeout = 10 * time.Second
)

// store is what the API reads. dbStore is the real one; tests use a fake.
type store interface {
	Machines(ctx context.Context) ([]machine, error)
	Machine(ctx context.Context, id string) (machine, error)
	LastReadings(ctx context.Context) (map[string]lastReading, error)
	Readings(ctx context.Context, machineID string, from, to time.Time) ([]oee.Reading, error)
}

type api struct {
	store store
	now   func() time.Time
}

func (a *api) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /machines", a.listMachines)
	mux.HandleFunc("GET /machines/{id}/oee", a.machineOEE)
}

type machineJSON struct {
	MachineID   string       `json:"machine_id"`
	Name        string       `json:"name"`
	IdealCycleS float64      `json:"ideal_cycle_s"`
	LastReading *lastReading `json:"last_reading"` // null before the first reading
}

// listMachines returns the registered machines with their latest reading.
func (a *api) listMachines(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	machines, err := a.store.Machines(ctx)
	if err != nil {
		httpjson.InternalError(w, r, err)
		return
	}
	last, err := a.store.LastReadings(ctx)
	if err != nil {
		httpjson.InternalError(w, r, err)
		return
	}
	resp := make([]machineJSON, 0, len(machines))
	for _, m := range machines {
		mj := machineJSON{MachineID: m.ID, Name: m.Name, IdealCycleS: m.IdealCycle.Seconds()}
		if l, ok := last[m.ID]; ok {
			mj.LastReading = &l
		}
		resp = append(resp, mj)
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"machines": resp})
}

type oeeJSON struct {
	MachineID    string    `json:"machine_id"`
	From         time.Time `json:"from"`
	To           time.Time `json:"to"`
	PlannedS     float64   `json:"planned_s"`
	RunningS     float64   `json:"running_s"`
	Good         uint64    `json:"good"`
	Scrap        uint64    `json:"scrap"`
	Availability float64   `json:"availability"`
	Performance  float64   `json:"performance"`
	Quality      float64   `json:"quality"`
	OEE          float64   `json:"oee"`
}

// machineOEE returns the OEE of a machine in the period given by from and to.
func (a *api) machineOEE(w http.ResponseWriter, r *http.Request) {
	from, to, err := parsePeriod(r.URL.Query(), a.now())
	if err != nil {
		httpjson.Error(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	m, err := a.store.Machine(ctx, r.PathValue("id"))
	if errors.Is(err, errMachineNotFound) {
		httpjson.Error(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		httpjson.InternalError(w, r, err)
		return
	}
	readings, err := a.store.Readings(ctx, m.ID, from, to)
	if err != nil {
		httpjson.InternalError(w, r, err)
		return
	}
	res := oee.Compute(readings, to.Sub(from), m.IdealCycle)
	httpjson.Write(w, http.StatusOK, oeeJSON{
		MachineID: m.ID, From: from, To: to,
		PlannedS: res.Planned.Seconds(), RunningS: res.Running.Seconds(),
		Good: res.Good, Scrap: res.Scrap,
		Availability: res.Availability, Performance: res.Performance, Quality: res.Quality, OEE: res.OEE,
	})
}

// parsePeriod reads from and to as RFC 3339, in milliseconds like ts. Without
// to, the period ends now; without from, it starts defaultPeriod before to. A
// to in the future becomes now, because time that has not happened cannot
// count as planned time.
func parsePeriod(q url.Values, now time.Time) (from, to time.Time, err error) {
	now = now.UTC().Truncate(time.Millisecond)
	to = now
	if s := q.Get("to"); s != "" {
		if to, err = time.Parse(time.RFC3339, s); err != nil {
			return from, to, errors.New("to: want RFC 3339, such as 2026-11-07T14:00:00Z")
		}
		if to = to.UTC().Truncate(time.Millisecond); to.After(now) {
			to = now
		}
	}
	from = to.Add(-defaultPeriod)
	if s := q.Get("from"); s != "" {
		if from, err = time.Parse(time.RFC3339, s); err != nil {
			return from, to, errors.New("from: want RFC 3339, such as 2026-11-07T14:00:00Z")
		}
		from = from.UTC().Truncate(time.Millisecond)
	}
	switch {
	case !from.Before(to):
		return from, to, errors.New("from: must be before to and before now")
	case to.Sub(from) > maxPeriod:
		return from, to, fmt.Errorf("from: period longer than %d days", maxPeriod/(24*time.Hour))
	}
	return from, to, nil
}
