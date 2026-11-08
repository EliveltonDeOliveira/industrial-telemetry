package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/httpjson"
)

const maxFaultDurationS = 3600

type faultRequest struct {
	MachineID string             `json:"machine_id"`
	Type      contract.AlertType `json:"type"`
	DurationS int                `json:"duration_s"`
}

type faultResponse struct {
	MachineID string             `json:"machine_id"`
	Type      contract.AlertType `json:"type"`
	Until     time.Time          `json:"until"`
}

// faultsHandler serves POST /faults, which injects a fault into one machine:
//
//	{"machine_id": "maq-01", "type": "temperatura_alta", "duration_s": 120}
//
// The fault types are the alert types the anomaly service should raise.
func faultsHandler(machines map[string]*machine, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req faultRequest
		if err := httpjson.Decode(w, r, 1<<10, &req); err != nil {
			httpjson.Error(w, http.StatusBadRequest, err)
			return
		}
		// Checked before converting, so a huge value cannot overflow into a valid duration.
		if req.DurationS < 1 || req.DurationS > maxFaultDurationS {
			httpjson.Error(w, http.StatusBadRequest, fmt.Errorf("duration_s: must be between 1 and %d", maxFaultDurationS))
			return
		}
		m, ok := machines[req.MachineID]
		if !ok {
			httpjson.Error(w, http.StatusNotFound, fmt.Errorf("machine_id: unknown machine %q", req.MachineID))
			return
		}
		until, err := m.inject(req.Type, time.Duration(req.DurationS)*time.Second, now())
		if err != nil {
			httpjson.Error(w, http.StatusBadRequest, err)
			return
		}
		slog.Info("fault injected", "machine_id", m.id, "type", req.Type, "until", until.UTC())
		httpjson.Write(w, http.StatusAccepted, faultResponse{MachineID: m.id, Type: req.Type, Until: until.UTC().Truncate(time.Millisecond)})
	})
}
